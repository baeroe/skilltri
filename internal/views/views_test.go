package views

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/skills"
)

// skillDir creates a skill folder with a SKILL.md and returns its path.
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

func entry(id, source, plugin, dir string) catalog.Entry {
	name := id[strings.LastIndex(id, ":")+1:]
	return catalog.Entry{ID: id, Source: source, Plugin: plugin, Skill: skills.Skill{Name: name, Description: "Demo.", Dir: dir}}
}

// demoEntries returns skills from two plugins (one of them with a marketplace entry name that differs
// from its plugin.json name) and one local skill, sorted by ID like catalog.Active.
func demoEntries(t *testing.T) []catalog.Entry {
	src := t.TempDir()
	return []catalog.Entry{
		entry("alpha", "demo-local", "", skillDir(t, src, "alpha")),
		entry("review:checklist", "demo-market", "review-entry", skillDir(t, src, "checklist")),
		entry("writing:outline", "demo-market", "writing", skillDir(t, src, "outline")),
	}
}

func TestClaudeView(t *testing.T) {
	cache := t.TempDir()
	active := demoEntries(t)
	dirs, err := Claude(cache, active)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 3 {
		t.Fatalf("dirs = %v", dirs)
	}
	var names []string
	for _, d := range dirs {
		names = append(names, filepath.Base(d))
		if filepath.Base(filepath.Dir(d)) != "claude" {
			t.Errorf("%s not under a claude dir", d)
		}
	}
	if want := []string{"review", "skilltri", "writing"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("plugins = %v, want %v", names, want)
	}
	view := filepath.Dir(filepath.Dir(dirs[0]))
	if filepath.Dir(view) != filepath.Join(cache, "views") || len(filepath.Base(view)) != 16 {
		t.Fatalf("view dir = %s", view)
	}

	writing := dirs[2]
	b, err := os.ReadFile(filepath.Join(writing, ".claude-plugin", "plugin.json"))
	if err != nil || string(b) != `{"name":"writing"}` {
		t.Fatalf("plugin.json = %q, %v", b, err)
	}
	link := filepath.Join(writing, "skills", "outline")
	st, err := os.Lstat(link)
	if err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("outline is not a symlink: %v %v", st, err)
	}
	if target, _ := os.Readlink(link); target != active[2].Skill.Dir {
		t.Fatalf("outline -> %s, want %s", target, active[2].Skill.Dir)
	}
	if target, _ := os.Readlink(filepath.Join(dirs[1], "skills", "alpha")); target != active[0].Skill.Dir {
		t.Fatalf("alpha -> %s", target)
	}
	if target, _ := os.Readlink(filepath.Join(dirs[0], "skills", "checklist")); target != active[1].Skill.Dir {
		t.Fatalf("checklist -> %s", target)
	}

	// second call: same dirs, nothing rewritten, .used touched
	before, _ := os.Lstat(link)
	used := filepath.Join(view, ".used")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(used, old, old); err != nil {
		t.Fatal(err)
	}
	again, err := Claude(cache, active)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, dirs) {
		t.Fatalf("second call = %v, want %v", again, dirs)
	}
	after, _ := os.Lstat(link)
	if !os.SameFile(before, after) || !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("view was rewritten")
	}
	if st, err := os.Stat(used); err != nil || !st.ModTime().After(old.Add(time.Minute)) {
		t.Fatalf(".used not touched: %v %v", st, err)
	}
	entries, _ := os.ReadDir(filepath.Join(cache, "views"))
	if len(entries) != 1 {
		t.Fatalf("views = %v, want one", entries)
	}

	// a different set gets a different view; an empty set has no plugin dirs
	other, err := Claude(cache, active[:1])
	if err != nil || len(other) != 1 || filepath.Dir(filepath.Dir(other[0])) == view {
		t.Fatalf("other set = %v, %v", other, err)
	}
	none, err := Claude(cache, nil)
	if err != nil || len(none) != 0 {
		t.Fatalf("empty set = %v, %v", none, err)
	}
}

func TestClaudeViewConcurrent(t *testing.T) {
	cache := t.TempDir()
	active := demoEntries(t)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = Claude(cache, active)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(cache, "views"))
	if len(entries) != 1 {
		t.Fatalf("views = %v, want one (no temp dirs left)", entries)
	}
}

func TestClaudeViewUnsafeNames(t *testing.T) {
	cache := t.TempDir()
	src := t.TempDir()
	active := []catalog.Entry{
		entry("..", "demo-local", "", skillDir(t, src, "dots")),
		entry("ok", "demo-local", "", skillDir(t, src, "ok")),
	}
	dirs, err := Claude(cache, active)
	if err != nil || len(dirs) != 1 {
		t.Fatalf("dirs = %v, %v", dirs, err)
	}
	entries, _ := os.ReadDir(filepath.Join(dirs[0], "skills"))
	if len(entries) != 1 || entries[0].Name() != "ok" {
		t.Fatalf("skills = %v", entries)
	}
}

func TestGC(t *testing.T) {
	cache := t.TempDir()
	active := demoEntries(t)
	oldDirs, err := Claude(cache, active)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := Claude(cache, active[:1])
	if err != nil {
		t.Fatal(err)
	}
	oldView := filepath.Dir(filepath.Dir(oldDirs[0]))
	freshView := filepath.Dir(filepath.Dir(fresh[0]))
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(oldView, ".used"), past, past); err != nil {
		t.Fatal(err)
	}
	// a view without .used falls back to its dir mtime
	bare := filepath.Join(cache, "views", "0123456789abcdef")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(bare, past, past); err != nil {
		t.Fatal(err)
	}

	if err := GC(cache, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldView); !os.IsNotExist(err) {
		t.Fatalf("old view kept: %v", err)
	}
	if _, err := os.Stat(bare); !os.IsNotExist(err) {
		t.Fatalf("bare view kept: %v", err)
	}
	if _, err := os.Stat(freshView); err != nil {
		t.Fatalf("fresh view removed: %v", err)
	}
	for _, e := range active { // GC removes the symlinks, never their targets
		if _, err := os.Stat(filepath.Join(e.Skill.Dir, "SKILL.md")); err != nil {
			t.Fatalf("skill source %s removed: %v", e.ID, err)
		}
	}
	if err := GC(t.TempDir(), time.Hour); err != nil { // no views dir yet
		t.Fatal(err)
	}
}

func TestClaudeViewRebuildsAfterGCRace(t *testing.T) {
	cache := t.TempDir()
	active := demoEntries(t)
	calls := 0
	beforeTouch = func(view string) { // GC removes the view between the existence check and the touch
		calls++
		if calls == 1 {
			os.RemoveAll(view)
		}
	}
	t.Cleanup(func() { beforeTouch = nil })
	dirs, err := Claude(cache, active)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("touch attempts = %d, want 2", calls)
	}
	if _, err := os.Stat(filepath.Join(dirs[0], ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("view not rebuilt: %v", err)
	}
}

func TestClaudeViewRelativeSkillDir(t *testing.T) {
	cache := t.TempDir()
	src := t.TempDir()
	skillDir(t, src, "alpha")
	t.Chdir(src)
	dirs, err := Claude(cache, []catalog.Entry{entry("alpha", "demo-local", "", "alpha")})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.Readlink(filepath.Join(dirs[0], "skills", "alpha"))
	if err != nil || !filepath.IsAbs(got) {
		t.Fatalf("link target = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dirs[0], "skills", "alpha", "SKILL.md")); err != nil {
		t.Fatalf("link does not resolve: %v", err)
	}
}
