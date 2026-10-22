package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/skills"
)

const rel = ".agents/skills"

func TestRegistry(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range Registry {
		if a.ID == "" || a.Display == "" || seen[a.ID] {
			t.Errorf("bad or duplicate adapter %+v", a)
		}
		seen[a.ID] = true
		if len(a.DetectPaths) == 0 {
			t.Errorf("%s has no detect paths", a.ID)
		}
		if a.ID != "claude-code" && (a.Method != "project-dir" || a.ProjectDir != rel) {
			t.Errorf("%s: method %q dir %q", a.ID, a.Method, a.ProjectDir)
		}
	}
	for _, id := range []string{"claude-code", "codex", "gemini-cli", "opencode", "cursor", "github-copilot", "amp"} {
		if !seen[id] {
			t.Errorf("missing adapter %s", id)
		}
	}
	cc, ok := ByID("claude-code")
	if !ok || cc.Method != "claude-plugin-dirs" || cc.ProjectDir != "" || !reflect.DeepEqual(cc.DetectPaths, []string{".claude"}) {
		t.Fatalf("claude-code = %+v, %v", cc, ok)
	}
	if a, ok := ForClient("claude-code"); !ok || a.ID != "claude-code" {
		t.Fatalf("ForClient = %+v, %v", a, ok)
	}
	if _, ok := ForClient("unknown-client"); ok {
		t.Fatal("unknown client matched")
	}
	if _, ok := ByID("nope"); ok {
		t.Fatal("unknown id matched")
	}
}

func TestDetected(t *testing.T) {
	home := t.TempDir()
	if got := Detected(home); len(got) != 0 {
		t.Fatalf("empty home detected %v", got)
	}
	if err := os.Mkdir(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".config", "amp"), 0o755); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, a := range Detected(home) {
		ids = append(ids, a.ID)
	}
	if want := []string{"codex", "amp"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("detected %v, want %v", ids, want)
	}
}

// skillDir creates a skill folder and returns its path.
func skillDir(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: Demo.\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func entry(id, plugin, dir string) catalog.Entry {
	name := id[strings.LastIndex(id, ":")+1:]
	return catalog.Entry{ID: id, Source: "demo", Plugin: plugin, Skill: skills.Skill{Name: name, Dir: dir}}
}

func gitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func readExclude(t *testing.T, gitDir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(gitDir, "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func manifestLinks(t *testing.T, dir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".skilltri.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct{ Links []string }
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m.Links
}

func isLinkTo(t *testing.T, path, target string) bool {
	t.Helper()
	st, err := os.Lstat(path)
	if err != nil || st.Mode()&os.ModeSymlink == 0 {
		return false
	}
	got, _ := os.Readlink(path)
	return got == target
}

func TestSyncProjectDir(t *testing.T) {
	root := gitRepo(t)
	session := filepath.Join(root, "sub")
	target := filepath.Join(session, rel)
	if err := os.MkdirAll(filepath.Join(target, "alpha"), 0o755); err != nil { // the user's own skill
		t.Fatal(err)
	}
	userFile := filepath.Join(target, "alpha", "SKILL.md")
	if err := os.WriteFile(userFile, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte("# git ls-files --others --exclude-from=.git/info/exclude\n*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	active := []catalog.Entry{
		entry("alpha", "", skillDir(t, src, "alpha")),
		entry("beta", "", skillDir(t, src, "beta")),
		entry("writing:outline", "writing", skillDir(t, src, "outline")),
	}

	res, err := SyncProjectDir(session, rel, active)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Linked, []string{"beta", "outline"}) || len(res.Removed) != 0 || !reflect.DeepEqual(res.Skipped, []string{"alpha"}) {
		t.Fatalf("first sync = %+v", res)
	}
	if !isLinkTo(t, filepath.Join(target, "beta"), active[1].Skill.Dir) || !isLinkTo(t, filepath.Join(target, "outline"), active[2].Skill.Dir) {
		t.Fatal("links missing")
	}
	if b, err := os.ReadFile(userFile); err != nil || string(b) != "mine" {
		t.Fatalf("user skill touched: %q %v", b, err)
	}
	if got := manifestLinks(t, target); !reflect.DeepEqual(got, []string{"beta", "outline"}) {
		t.Fatalf("manifest = %v", got)
	}
	wantExclude := "# git ls-files --others --exclude-from=.git/info/exclude\n*.log\n# skilltri\n" +
		"/sub/.agents/skills/.skilltri.json\n/sub/.agents/skills/beta\n/sub/.agents/skills/outline\n"
	if got := readExclude(t, filepath.Join(root, ".git")); got != wantExclude {
		t.Fatalf("exclude =\n%s\nwant\n%s", got, wantExclude)
	}

	// same set again: nothing changes, exclude lines are not duplicated
	if _, err := SyncProjectDir(session, rel, active); err != nil {
		t.Fatal(err)
	}
	if got := readExclude(t, filepath.Join(root, ".git")); got != wantExclude {
		t.Fatalf("exclude after resync =\n%s", got)
	}

	// fewer skills: only the owned link that is no longer active goes
	if err := os.WriteFile(filepath.Join(target, "notes.txt"), []byte("user"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = SyncProjectDir(session, rel, active[:2])
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Linked, []string{"beta"}) || !reflect.DeepEqual(res.Removed, []string{"outline"}) || !reflect.DeepEqual(res.Skipped, []string{"alpha"}) {
		t.Fatalf("second sync = %+v", res)
	}
	if _, err := os.Lstat(filepath.Join(target, "outline")); !os.IsNotExist(err) {
		t.Fatalf("outline still there: %v", err)
	}
	for _, p := range []string{userFile, filepath.Join(target, "notes.txt"), filepath.Join(active[2].Skill.Dir, "SKILL.md")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s removed: %v", p, err)
		}
	}
	if got := manifestLinks(t, target); !reflect.DeepEqual(got, []string{"beta"}) {
		t.Fatalf("manifest = %v", got)
	}

	// an owned link the user replaced with a real folder is left alone and no longer owned
	if err := os.Remove(filepath.Join(target, "beta")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(target, "beta"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err = SyncProjectDir(session, rel, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Linked) != 0 || len(res.Removed) != 0 {
		t.Fatalf("third sync = %+v", res)
	}
	if st, err := os.Lstat(filepath.Join(target, "beta")); err != nil || !st.IsDir() {
		t.Fatalf("user beta folder touched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, ".skilltri.json")); !os.IsNotExist(err) {
		t.Fatalf("manifest kept without links: %v", err)
	}
}

func TestSyncCreatesDirOnlyWithLinks(t *testing.T) {
	session := t.TempDir()
	res, err := SyncProjectDir(session, rel, nil)
	if err != nil || len(res.Linked) != 0 {
		t.Fatalf("sync = %+v, %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(session, ".agents")); !os.IsNotExist(err) {
		t.Fatalf(".agents created without links: %v", err)
	}
}

func TestSyncNoGitRepo(t *testing.T) {
	session := t.TempDir()
	src := t.TempDir()
	res, err := SyncProjectDir(session, rel, []catalog.Entry{entry("alpha", "", skillDir(t, src, "alpha"))})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Linked, []string{"alpha"}) || !isLinkTo(t, filepath.Join(session, rel, "alpha"), filepath.Join(src, "alpha")) {
		t.Fatalf("sync = %+v", res)
	}
}

func TestSyncGitFile(t *testing.T) {
	// a worktree: .git is a file pointing at <common>/worktrees/<name>, info/ lives in the common dir
	common := filepath.Join(t.TempDir(), "main.git")
	wtGit := filepath.Join(common, "worktrees", "wt")
	if err := os.MkdirAll(wtGit, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtGit, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+wtGit+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	if _, err := SyncProjectDir(root, rel, []catalog.Entry{entry("alpha", "", skillDir(t, src, "alpha"))}); err != nil {
		t.Fatal(err)
	}
	want := "# skilltri\n/.agents/skills/.skilltri.json\n/.agents/skills/alpha\n"
	if got := readExclude(t, common); got != want {
		t.Fatalf("exclude =\n%s", got)
	}

	// a submodule: relative gitdir without commondir
	super := t.TempDir()
	modGit := filepath.Join(super, ".git", "modules", "lib")
	if err := os.MkdirAll(modGit, 0o755); err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(super, "lib")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lib, ".git"), []byte("gitdir: ../.git/modules/lib\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncProjectDir(lib, rel, []catalog.Entry{entry("alpha", "", skillDir(t, src, "alpha"))}); err != nil {
		t.Fatal(err)
	}
	if got := readExclude(t, modGit); got != want {
		t.Fatalf("submodule exclude =\n%s", got)
	}
}

func TestSyncReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	session := t.TempDir()
	if err := os.Chmod(session, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(session, 0o755) })
	src := t.TempDir()
	if _, err := SyncProjectDir(session, rel, []catalog.Entry{entry("alpha", "", skillDir(t, src, "alpha"))}); err == nil {
		t.Fatal("want an error for a read-only folder")
	}
}

func TestSyncNameCollision(t *testing.T) {
	session := t.TempDir()
	src := t.TempDir()
	active := []catalog.Entry{
		entry("outline", "", skillDir(t, src, "local-outline")),
		entry("writing:outline", "writing", skillDir(t, src, "outline")),
	}
	res, err := SyncProjectDir(session, rel, active)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Linked, []string{"outline", "writing-outline"}) {
		t.Fatalf("linked = %v", res.Linked)
	}
	target := filepath.Join(session, rel)
	if !isLinkTo(t, filepath.Join(target, "outline"), active[0].Skill.Dir) || !isLinkTo(t, filepath.Join(target, "writing-outline"), active[1].Skill.Dir) {
		t.Fatal("collision links wrong")
	}
}
