package sources

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// gitEnv isolates every git call (test helpers and the Fetcher) from the user's git config.
func gitEnv(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Demo Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "demo@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Demo Author")
	t.Setenv("GIT_COMMITTER_EMAIL", "demo@example.com")
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// testRepo is a local upstream repository.
type testRepo struct {
	t   *testing.T
	dir string
}

// newRepo creates a repo with branch main, one commit (file a.txt = "A") and tag v1 on it.
func newRepo(t *testing.T) *testRepo {
	t.Helper()
	gitEnv(t)
	r := &testRepo{t: t, dir: filepath.Join(t.TempDir(), "upstream")}
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, r.dir, "init", "-q", "-b", "main")
	run(t, r.dir, "config", "uploadpack.allowFilter", "true")
	r.commit("a.txt", "A")
	run(t, r.dir, "tag", "v1")
	return r
}

func (r *testRepo) url() string { return "file://" + r.dir }

// commit writes file=content and commits it, returning the new commit.
func (r *testRepo) commit(file, content string) string {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, file), []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
	run(r.t, r.dir, "add", file)
	run(r.t, r.dir, "commit", "-q", "-m", "set "+file+" to "+content)
	return r.head()
}

func (r *testRepo) head() string { return run(r.t, r.dir, "rev-parse", "HEAD") }

func newFetcher(t *testing.T) *Fetcher {
	return &Fetcher{Root: filepath.Join(t.TempDir(), "sources")}
}

func mustSync(t *testing.T, f *Fetcher, key, url, ref string) Checkout {
	t.Helper()
	co, err := f.Sync(context.Background(), key, url, ref)
	if err != nil {
		t.Fatalf("Sync(%q, %q): %v", key, ref, err)
	}
	return co
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSyncCloneAndUpdate(t *testing.T) {
	r := newRepo(t)
	a := r.head()
	f := newFetcher(t)

	co := mustSync(t, f, "demo", r.url(), "")
	if co.Commit != a || co.Dir != f.Dir("demo") {
		t.Fatalf("clone: got %+v, want commit %s in %s", co, a, f.Dir("demo"))
	}
	if got := readFile(t, filepath.Join(co.Dir, "a.txt")); got != "A" {
		t.Fatalf("a.txt = %q", got)
	}
	if entries, _ := os.ReadDir(filepath.Dir(co.Dir)); len(entries) != 1 {
		t.Fatalf("leftovers next to the checkout: %v", entries)
	}

	b := r.commit("a.txt", "B")
	co = mustSync(t, f, "demo", r.url(), "main")
	if co.Commit != b {
		t.Fatalf("update: commit %s, want %s", co.Commit, b)
	}
	if got := readFile(t, filepath.Join(co.Dir, "a.txt")); got != "B" {
		t.Fatalf("a.txt = %q", got)
	}
	cur, ok := f.Current("demo")
	if !ok || cur != co {
		t.Fatalf("Current = %+v, %v; want %+v", cur, ok, co)
	}
}

func TestSyncRefs(t *testing.T) {
	r := newRepo(t)
	a := r.head()
	run(t, r.dir, "checkout", "-q", "-b", "feature")
	feat := r.commit("f.txt", "F")
	run(t, r.dir, "checkout", "-q", "main")
	b := r.commit("a.txt", "B")
	f := newFetcher(t)

	for _, tc := range []struct{ name, ref, want string }{
		{"default", "", b},
		{"tag", "v1", a},
		{"branch", "feature", feat},
		{"main", "main", b},
		{"pinned", a, a},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if co := mustSync(t, f, "refs", r.url(), tc.ref); co.Commit != tc.want {
				t.Fatalf("ref %q: commit %s, want %s", tc.ref, co.Commit, tc.want)
			}
		})
	}

	// a pinned commit that is already present needs no fetch: an unreachable remote does not matter
	missing := "file://" + filepath.Join(t.TempDir(), "gone")
	if co := mustSync(t, f, "refs", missing, feat); co.Commit != feat {
		t.Fatalf("pinned offline: commit %s, want %s", co.Commit, feat)
	}
	// an unknown ref is an error and keeps the checkout
	if _, err := f.Sync(context.Background(), "refs", r.url(), "no-such-branch"); err == nil {
		t.Fatal("unknown ref: no error")
	}
	if cur, ok := f.Current("refs"); !ok || cur.Commit != feat {
		t.Fatalf("after unknown ref: Current = %+v, %v", cur, ok)
	}
}

func TestSyncFailureKeepsCheckout(t *testing.T) {
	r := newRepo(t)
	a := r.head()
	f := newFetcher(t)
	mustSync(t, f, "demo", r.url(), "main")
	r.commit("a.txt", "B")

	missing := "file://" + filepath.Join(t.TempDir(), "gone")
	if _, err := f.Sync(context.Background(), "demo", missing, "main"); err == nil {
		t.Fatal("unreachable remote: no error")
	}
	cur, ok := f.Current("demo")
	if !ok || cur.Commit != a {
		t.Fatalf("Current = %+v, %v; want commit %s", cur, ok, a)
	}
	if got := readFile(t, filepath.Join(cur.Dir, "a.txt")); got != "A" {
		t.Fatalf("a.txt = %q", got)
	}

	// a failed first clone leaves nothing behind
	if _, err := f.Sync(context.Background(), "fresh", missing, ""); err == nil {
		t.Fatal("clone of missing remote: no error")
	}
	if _, ok := f.Current("fresh"); ok {
		t.Fatal("Current reports a checkout after a failed clone")
	}
	if entries, _ := os.ReadDir(filepath.Join(f.Root, "fresh")); len(entries) != 0 {
		t.Fatalf("leftovers after a failed clone: %v", entries)
	}
}

func TestSyncForcePush(t *testing.T) {
	r := newRepo(t)
	f := newFetcher(t)
	mustSync(t, f, "demo", r.url(), "main")
	r.commit("a.txt", "B")
	mustSync(t, f, "demo", r.url(), "main")

	run(t, r.dir, "checkout", "-q", "--orphan", "rewrite")
	run(t, r.dir, "rm", "-rq", "--cached", ".")
	unrelated := r.commit("b.txt", "X")
	run(t, r.dir, "branch", "-f", "main", unrelated)

	co := mustSync(t, f, "demo", r.url(), "main")
	if co.Commit != unrelated {
		t.Fatalf("commit %s, want %s", co.Commit, unrelated)
	}
	if got := readFile(t, filepath.Join(co.Dir, "b.txt")); got != "X" {
		t.Fatalf("b.txt = %q", got)
	}
}

func TestSyncPluginKeyAndConcurrency(t *testing.T) {
	r := newRepo(t)
	f := newFetcher(t)
	key := "market/plugins/demo-plugin"
	if got, want := f.Dir(key), filepath.Join(f.Root, "market", "plugins", "demo-plugin", "repo"); got != want {
		t.Fatalf("Dir = %s, want %s", got, want)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Go(func() {
			if _, err := f.Sync(context.Background(), key, r.url(), "main"); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if cur, ok := f.Current(key); !ok || cur.Commit != r.head() {
		t.Fatalf("Current = %+v, %v", cur, ok)
	}
}

func TestRepoURL(t *testing.T) {
	for in, want := range map[string]string{
		"owner/repo":                      "https://github.com/owner/repo.git",
		"https://example.com/x/y.git":     "https://example.com/x/y.git",
		"git@example.com:owner/repo.git":  "git@example.com:owner/repo.git",
		"file:///tmp/demo":                "file:///tmp/demo",
		"owner/repo/extra":                "owner/repo/extra",
		"/abs/owner":                      "/abs/owner",
		"./owner/repo":                    "./owner/repo",
		"example.com:owner/repo":          "example.com:owner/repo",
		"demo-org/demo.skills":            "https://github.com/demo-org/demo.skills.git",
		"https://github.com/owner/repo":   "https://github.com/owner/repo",
		"ssh://git@example.com/owner/rep": "ssh://git@example.com/owner/rep",
	} {
		if got := RepoURL(in); got != want {
			t.Errorf("RepoURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPinned(t *testing.T) {
	for in, want := range map[string]bool{
		"0123456789abcdef0123456789abcdef01234567":  true,
		"0123456789ABCDEF0123456789abcdef01234567":  true,
		"0123456789abcdef0123456789abcdef0123456":   false,
		"0123456789abcdef0123456789abcdef012345678": false,
		"0123456789abcdef0123456789abcdef0123456g":  false,
		"main": false,
		"v1":   false,
		"":     false,
	} {
		if got := Pinned(in); got != want {
			t.Errorf("Pinned(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestSyncDeletedBranchKeepsCheckout(t *testing.T) {
	r := newRepo(t)
	f := newFetcher(t)
	mustSync(t, f, "demo", r.url(), "main")
	b := r.commit("a.txt", "B")
	mustSync(t, f, "demo", r.url(), "main")

	run(t, r.dir, "branch", "-m", "main", "trunk")
	if co, err := f.Sync(context.Background(), "demo", r.url(), "main"); err == nil {
		t.Fatalf("deleted branch: no error, checkout %+v", co)
	}
	if cur, ok := f.Current("demo"); !ok || cur.Commit != b {
		t.Fatalf("Current = %+v, %v; want commit %s", cur, ok, b)
	}
}

func TestSyncRejectsBadKeys(t *testing.T) {
	gitEnv(t)
	f := newFetcher(t)
	for _, key := range []string{"", "..", "../x", "a/../b", "a//b", "/abs", "a/", "./a", `a\b`} {
		if _, err := f.Sync(context.Background(), key, "file:///nowhere", ""); err == nil {
			t.Errorf("Sync(key %q): no error", key)
		}
		if _, ok := f.Current(key); ok {
			t.Errorf("Current(key %q) reports a checkout", key)
		}
	}
	if entries, _ := os.ReadDir(filepath.Dir(f.Root)); len(entries) != 0 {
		t.Fatalf("bad keys created files: %v", entries)
	}
}

func TestCommandEnv(t *testing.T) {
	has := func(env []string, kv string) bool { return slices.Contains(env, kv) }
	t.Setenv("GIT_SSH_COMMAND", "")
	env := commandEnv()
	for _, kv := range []string{"GIT_TERMINAL_PROMPT=0", "SSH_ASKPASS_REQUIRE=never", "GIT_SSH_COMMAND=ssh -o BatchMode=yes"} {
		if !has(env, kv) {
			t.Errorf("missing %s", kv)
		}
	}
	t.Setenv("GIT_SSH_COMMAND", "demo-ssh")
	if env := commandEnv(); has(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes") || !has(env, "GIT_SSH_COMMAND=demo-ssh") {
		t.Errorf("user GIT_SSH_COMMAND overridden: %v", env)
	}
}
