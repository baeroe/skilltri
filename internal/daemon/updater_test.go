package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
)

// gitEnv isolates every git call from the user's git config.
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

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// upstream is a local git repo with the skill "alpha"; commit adds a file and returns HEAD.
type upstream struct {
	t   *testing.T
	dir string
}

func newUpstream(t *testing.T) *upstream {
	t.Helper()
	gitEnv(t)
	u := &upstream{t: t, dir: filepath.Join(t.TempDir(), "upstream")}
	if err := os.MkdirAll(filepath.Join(u.dir, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, u.dir, "init", "-q", "-b", "main")
	u.write("alpha/SKILL.md", "---\nname: alpha\ndescription: Demo skill alpha.\n---\nBody.\n")
	return u
}

func (u *upstream) write(file, content string) string {
	u.t.Helper()
	if err := os.WriteFile(filepath.Join(u.dir, file), []byte(content), 0o644); err != nil {
		u.t.Fatal(err)
	}
	git(u.t, u.dir, "add", "-A")
	git(u.t, u.dir, "commit", "-q", "-m", "change "+file)
	return git(u.t, u.dir, "rev-parse", "HEAD")
}

func (u *upstream) head() string { return git(u.t, u.dir, "rev-parse", "HEAD") }

func gitConfig(u *upstream, every string) *config.Config {
	return &config.Config{Version: 1, Servers: map[string]*config.Server{}, Sources: map[string]*config.Source{
		"demo": {Type: "git", URL: "file://" + u.dir, UpdateEvery: every},
	}}
}

// savedState returns the saved catalog's state of source name (zero if missing).
func savedState(name string) (catalog.SourceState, *catalog.Catalog) {
	c, err := catalog.Load(catalog.Path())
	if err != nil {
		return catalog.SourceState{}, nil
	}
	for _, s := range c.Sources {
		if s.Name == name {
			return s, c
		}
	}
	return catalog.SourceState{}, c
}

func hasSkill(c *catalog.Catalog, id string) bool {
	if c == nil {
		return false
	}
	for _, e := range c.Skills {
		if e.ID == id {
			return true
		}
	}
	return false
}

func (e env) update(t *testing.T, names ...string) UpdateReply {
	t.Helper()
	var r UpdateReply
	if err := Control(e.sock, Hello{Cmd: "update", Args: names}, &r); err != nil {
		t.Fatalf("update: %v", err)
	}
	return r
}

func TestUpdaterSyncsDueSources(t *testing.T) {
	u := newUpstream(t)
	e := newEnv(t)
	e.writeConfig(t, gitConfig(u, "100ms"))
	e.tick = poll
	e.start(t)
	first := u.head()
	eventually(t, 5*time.Second, "first commit in the catalog", func() bool {
		st, c := savedState("demo")
		return st.Commit == first && hasSkill(c, "alpha")
	})
	second := u.write("alpha/notes.md", "more\n")
	eventually(t, 5*time.Second, "second commit in the catalog", func() bool {
		st, _ := savedState("demo")
		return st.Commit == second
	})
	if c := e.daemonCatalog(t); c == nil {
		t.Fatal("no catalog")
	}
}

func TestUpdaterManualSource(t *testing.T) {
	u := newUpstream(t)
	e := newEnv(t)
	e.writeConfig(t, gitConfig(u, "0"))
	e.tick = poll
	e.start(t)
	first := u.head()
	eventually(t, 5*time.Second, "first fetch of a manual source", func() bool {
		st, _ := savedState("demo")
		return st.Commit == first
	})
	second := u.write("alpha/notes.md", "more\n")
	time.Sleep(8 * poll)
	if st, _ := savedState("demo"); st.Commit != first {
		t.Fatalf("tick synced a manual source: commit %s", st.Commit)
	}
	r := e.update(t, "demo")
	if len(r.Sources) != 1 || r.Sources[0].Commit != second || r.Sources[0].Error != "" {
		t.Fatalf("reply = %+v", r)
	}
	if st, _ := savedState("demo"); st.Commit != second {
		t.Fatalf("catalog commit = %s, want %s", st.Commit, second)
	}
}

func TestUpdateFailureKeepsSkills(t *testing.T) {
	u := newUpstream(t)
	e := newEnv(t)
	e.writeConfig(t, gitConfig(u, "0"))
	e.tick = poll
	e.start(t)
	first := u.head()
	eventually(t, 5*time.Second, "first fetch", func() bool {
		st, _ := savedState("demo")
		return st.Commit == first
	})
	if err := os.RemoveAll(u.dir); err != nil {
		t.Fatal(err)
	}
	r := e.update(t) // all sources
	if len(r.Sources) != 1 || r.Sources[0].Name != "demo" || r.Sources[0].Error == "" || r.Sources[0].Commit != first {
		t.Fatalf("reply = %+v", r)
	}
	st, c := savedState("demo")
	if st.Error == "" || st.Commit != first {
		t.Fatalf("saved state = %+v", st)
	}
	if !hasSkill(c, "alpha") {
		t.Fatalf("skill alpha gone after a failed update: %+v", c.Skills)
	}
	if !hasSkill(e.daemonCatalog(t), "alpha") {
		t.Fatal("daemon catalog lost alpha")
	}
}

func TestControlUpdate(t *testing.T) {
	u := newUpstream(t)
	e := newEnv(t)
	e.writeConfig(t, gitConfig(u, "0"))
	e.start(t) // default tick: only the start sync
	r := e.update(t)
	if len(r.Sources) != 1 || r.Sources[0].Name != "demo" || r.Sources[0].Commit != u.head() || r.Sources[0].FetchedAt.IsZero() {
		t.Fatalf("reply = %+v", r)
	}
	r = e.update(t, "nope")
	if len(r.Sources) != 1 || r.Sources[0].Name != "nope" || r.Sources[0].Error == "" {
		t.Fatalf("unknown source reply = %+v", r)
	}
}

func TestConfigChangeRebuildsCatalog(t *testing.T) {
	gitEnv(t)
	e := newEnv(t)
	src := filepath.Join(e.dir, "skills")
	if err := os.MkdirAll(filepath.Join(src, "beta"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "beta", "SKILL.md"), []byte("---\nname: beta\ndescription: Demo skill beta.\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.writeConfig(t, cfgOf(map[string]*config.Server{}))
	e.start(t)
	if hasSkill(e.daemonCatalog(t), "beta") {
		t.Fatal("beta before its source was configured")
	}
	c := cfgOf(map[string]*config.Server{})
	c.Sources = map[string]*config.Source{"mine": {Type: "local", Path: src}}
	e.writeConfig(t, c)
	eventually(t, 5*time.Second, "beta after adding the source", func() bool {
		_, saved := savedState("mine")
		return hasSkill(saved, "beta") && hasSkill(e.daemonCatalog(t), "beta")
	})
}

// daemonCatalog is the running daemon's Catalog().
func (e env) daemonCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	if e.d == nil || *e.d == nil {
		t.Fatal("daemon not started")
	}
	return (*e.d).Catalog()
}

func TestBrokenConfigAtStartKeepsCachedCatalog(t *testing.T) {
	e := newEnv(t)
	cached := &catalog.Catalog{Skills: []catalog.Entry{{ID: "alpha", Source: "demo"}}}
	if err := catalog.Save(catalog.Path(), cached); err != nil {
		t.Fatal(err)
	}
	e.writeRaw(t, "{broken")
	e.tick = poll
	e.start(t)
	time.Sleep(4 * poll)
	if _, c := savedState("demo"); !hasSkill(c, "alpha") {
		t.Fatalf("cached catalog overwritten: %+v", c)
	}
	if !hasSkill(e.daemonCatalog(t), "alpha") {
		t.Fatal("daemon does not serve the cached catalog")
	}
	if err := Control(e.sock, Hello{Cmd: "update"}, nil); err == nil {
		t.Fatal("update without a valid config succeeded")
	}
}
