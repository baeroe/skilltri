package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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
	for id, bin := range map[string]string{"claude-code": "claude", "codex": "codex", "gemini-cli": "gemini", "opencode": "opencode",
		"cursor": "cursor-agent", "github-copilot": "copilot", "amp": "amp"} {
		if a, ok := ByID(id); !ok || a.Binary != bin {
			t.Errorf("adapter %s: %+v, want binary %s", id, a, bin)
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
	var m struct{ Links map[string]string }
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var names []string
	for name, target := range m.Links {
		if !filepath.IsAbs(target) {
			t.Errorf("manifest target of %s not absolute: %q", name, target)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSyncForeignSymlink(t *testing.T) {
	session := t.TempDir()
	target := filepath.Join(session, rel)
	src := t.TempDir()
	beta, gamma := skillDir(t, src, "beta"), skillDir(t, src, "gamma")
	foreign := skillDir(t, t.TempDir(), "foreign")
	// the manifest claims beta and gamma, but both are the user's links to a foreign dir
	writeFile(t, filepath.Join(target, ".skilltri.json"), `{"links":{"beta":"`+beta+`","gamma":"`+gamma+`"}}`)
	for _, name := range []string{"beta", "gamma"} {
		if err := os.Symlink(foreign, filepath.Join(target, name)); err != nil {
			t.Fatal(err)
		}
	}
	res, err := SyncProjectDir(session, rel, []catalog.Entry{entry("beta", "", beta)}) // (a) beta active, (b) gamma not
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Linked) != 0 || len(res.Removed) != 0 || !reflect.DeepEqual(res.Skipped, []string{"beta", "gamma"}) {
		t.Fatalf("sync = %+v", res)
	}
	for _, name := range []string{"beta", "gamma"} {
		if !isLinkTo(t, filepath.Join(target, name), foreign) {
			t.Fatalf("user link %s touched", name)
		}
	}
	if _, err := os.Stat(filepath.Join(target, ".skilltri.json")); !os.IsNotExist(err) {
		t.Fatalf("manifest kept without owned links: %v", err)
	}
}

func TestSyncLegacyManifest(t *testing.T) {
	session := t.TempDir()
	target := filepath.Join(session, rel)
	src := t.TempDir()
	beta := skillDir(t, src, "beta")
	foreign := skillDir(t, t.TempDir(), "foreign")
	writeFile(t, filepath.Join(target, ".skilltri.json"), `{"links":["beta","old"]}`)
	if err := os.Symlink(beta, filepath.Join(target, "beta")); err != nil { // matches the wanted target: ours
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, filepath.Join(target, "old")); err != nil { // no recorded target: not ours
		t.Fatal(err)
	}
	res, err := SyncProjectDir(session, rel, []catalog.Entry{entry("beta", "", beta)})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Linked, []string{"beta"}) || len(res.Removed) != 0 || !reflect.DeepEqual(res.Skipped, []string{"old"}) {
		t.Fatalf("sync = %+v", res)
	}
	if !isLinkTo(t, filepath.Join(target, "old"), foreign) {
		t.Fatal("legacy entry without target touched")
	}
	if got := manifestLinks(t, target); !reflect.DeepEqual(got, []string{"beta"}) {
		t.Fatalf("manifest = %v", got)
	}
}

func TestSyncExcludeBlock(t *testing.T) {
	root := gitRepo(t)
	exc := filepath.Join(root, ".git", "info", "exclude")
	writeFile(t, exc, "# skilltri\n/other\n\n*.log\n")
	src := t.TempDir()
	if _, err := SyncProjectDir(root, rel, []catalog.Entry{entry("alpha", "", skillDir(t, src, "alpha"))}); err != nil {
		t.Fatal(err)
	}
	want := "# skilltri\n/other\n/.agents/skills/.skilltri.json\n/.agents/skills/alpha\n\n*.log\n"
	if got := readExclude(t, filepath.Join(root, ".git")); got != want {
		t.Fatalf("exclude =\n%s\nwant\n%s", got, want)
	}
}

func TestSyncBogusGitDir(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(t.TempDir(), "gone.git")
	writeFile(t, filepath.Join(root, ".git"), "gitdir: "+missing+"\n")
	src := t.TempDir()
	res, err := SyncProjectDir(root, rel, []catalog.Entry{entry("alpha", "", skillDir(t, src, "alpha"))})
	if err != nil || !reflect.DeepEqual(res.Linked, []string{"alpha"}) {
		t.Fatalf("sync = %+v, %v", res, err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("bogus gitdir created: %v", err)
	}
}

func TestSyncRelativeSkillDir(t *testing.T) {
	session := t.TempDir()
	src := t.TempDir()
	skillDir(t, src, "alpha")
	t.Chdir(src)
	if _, err := SyncProjectDir(session, rel, []catalog.Entry{entry("alpha", "", "alpha")}); err != nil {
		t.Fatal(err)
	}
	got, err := os.Readlink(filepath.Join(session, rel, "alpha"))
	if err != nil || !filepath.IsAbs(got) || filepath.Base(got) != "alpha" {
		t.Fatalf("link target = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(session, rel, "alpha", "SKILL.md")); err != nil {
		t.Fatalf("link does not resolve: %v", err)
	}
}

func TestSyncExcludeLongLine(t *testing.T) {
	root := gitRepo(t)
	exc := filepath.Join(root, ".git", "info", "exclude")
	long := "/" + strings.Repeat("x", 100*1024)
	writeFile(t, exc, "*.log\n"+long+"\n/tail\n")
	src := t.TempDir()
	if _, err := SyncProjectDir(root, rel, []catalog.Entry{entry("alpha", "", skillDir(t, src, "alpha"))}); err != nil {
		t.Fatal(err)
	}
	want := "*.log\n" + long + "\n/tail\n# skilltri\n/.agents/skills/.skilltri.json\n/.agents/skills/alpha\n"
	if got := readExclude(t, filepath.Join(root, ".git")); got != want {
		t.Fatalf("exclude lost content: got %d bytes, want %d", len(got), len(want))
	}
}

func TestProjectDirAllowed(t *testing.T) {
	root := t.TempDir()
	mk := func(base, rel string, git bool) string {
		t.Helper()
		d := filepath.Join(base, rel)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if git {
			if err := os.MkdirAll(filepath.Join(d, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		return d
	}
	mk(root, "", true)             // an ancestor of home that is a work tree
	home := mk(root, "home", true) // a dotfiles repo in home
	link := filepath.Join(root, "link-to-home")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	outside := mk(t.TempDir(), "plain", false)
	for dir, want := range map[string]bool{
		home:                                 false,
		root:                                 false,
		link:                                 false,
		mk(home, "dev/p", true):              true,
		mk(home, "plain", false):             true, // inside the dotfiles work tree
		mk(root, "other", false):             true, // inside root's work tree, not an ancestor of home
		outside:                              false,
		filepath.Join(home, "dev/p/missing"): false,
	} {
		if got := ProjectDirAllowed(dir, home); got != want {
			t.Errorf("ProjectDirAllowed(%s) = %v, want %v", dir, got, want)
		}
	}
}
