package hook

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/rules"
)

// fixture is a fake HOME with skilltri's config, state, data and cache dirs, and a local source
// "demo" with the skills alpha (everywhere except X) and beta (only P).
type fixture struct {
	home, src string
	p, q, x   string
}

func setDirs(t *testing.T) string {
	t.Helper()
	home := rules.Normalize(t.TempDir())
	t.Setenv("HOME", home)
	for env, sub := range map[string]string{
		"SKILLTRI_CONFIG_DIR": ".config/skilltri",
		"SKILLTRI_STATE_DIR":  ".local/state/skilltri",
		"SKILLTRI_DATA_DIR":   ".local/share/skilltri",
		"SKILLTRI_CACHE_DIR":  ".cache/skilltri",
	} {
		t.Setenv(env, filepath.Join(home, sub))
	}
	return home
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{home: setDirs(t)}
	f.src = filepath.Join(f.home, "skills")
	f.p, f.q, f.x = mkdir(t, f.home, "p"), mkdir(t, f.home, "q"), mkdir(t, f.home, "x")
	skill(t, f.src, "alpha")
	skill(t, f.src, "beta")
	f.writeConfig(t, nil)
	return f
}

// writeConfig writes the demo config with the given enabled adapters (nil → default).
func (f *fixture) writeConfig(t *testing.T, adapters []string) {
	t.Helper()
	cfg := &config.Config{
		Version:  1,
		Servers:  map[string]*config.Server{},
		Settings: config.Settings{Adapters: adapters},
		Sources: map[string]*config.Source{"demo": {
			Type:   "local",
			Path:   f.src,
			Rules:  []rules.Rule{{Path: f.x, Mode: rules.NotTree}},
			Skills: map[string]*config.Node{"beta": {Rules: []rules.Rule{{Path: f.p, Mode: rules.Only}}}},
		}},
	}
	writeJSON(t, filepath.Join(f.home, ".config/skilltri/config.json"), cfg)
}

func mkdir(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func skill(t *testing.T, root, name string) {
	t.Helper()
	dir := mkdir(t, root, name)
	content := "---\nname: " + name + "\ndescription: Demo skill " + name + ".\n---\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// viewSkills returns the skill link names of every plugin dir in a CLAUDE_CODE_PLUGIN_DIRS value,
// skipping the entries in skip.
func viewSkills(t *testing.T, value string, skip ...string) []string {
	t.Helper()
	var names []string
	for _, d := range strings.Split(value, ":") {
		if d == "" || contains(skip, d) {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(d, "skills"))
		if err != nil {
			t.Fatalf("plugin dir %s: %v", d, err)
		}
		for _, e := range entries {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// onlyMarkers checks that e degrades to "no skill env" with the given user base.
func onlyMarkers(t *testing.T, e Env, base string) {
	t.Helper()
	want := map[string]string{BaseVar: base}
	if base != "" {
		want[PluginDirsVar] = base
	}
	if !reflect.DeepEqual(e.Set, want) {
		t.Fatalf("Set = %v, want %v", e.Set, want)
	}
	wantUnset := []string{NativeVar}
	if base == "" {
		wantUnset = []string{PluginDirsVar, NativeVar}
	}
	sort.Strings(wantUnset)
	if !reflect.DeepEqual(e.Unset, wantUnset) {
		t.Fatalf("Unset = %v, want %v", e.Unset, wantUnset)
	}
}

func TestComputeClaude(t *testing.T) {
	f := newFixture(t)
	e := Compute(f.p, nil)
	if len(e.Notes) != 0 {
		t.Fatalf("notes: %v", e.Notes)
	}
	dirs := e.Set[PluginDirsVar]
	if strings.Contains(dirs, ":") || dirs == "" {
		t.Fatalf("%s = %q, want one view dir", PluginDirsVar, dirs)
	}
	if got := viewSkills(t, dirs); !reflect.DeepEqual(got, []string{"alpha", "beta"}) {
		t.Fatalf("P skills = %v", got)
	}
	if e.Set[NativeVar] != "claude-code" || e.Set[BaseVar] != "" {
		t.Fatalf("Set = %v", e.Set)
	}
	if _, err := os.Stat(catalog.Path()); err != nil {
		t.Fatalf("catalog not saved: %v", err)
	}

	e = Compute(f.q, nil)
	if got := viewSkills(t, e.Set[PluginDirsVar]); !reflect.DeepEqual(got, []string{"alpha"}) {
		t.Fatalf("Q skills = %v", got)
	}
}

func TestComputeRebuildsStaleCatalog(t *testing.T) {
	f := newFixture(t)
	if got := viewSkills(t, Compute(f.q, nil).Set[PluginDirsVar]); !reflect.DeepEqual(got, []string{"alpha"}) {
		t.Fatalf("Q skills = %v", got)
	}
	// A config edit (here: a new source) after the catalog was saved makes the cache stale.
	more := filepath.Join(f.home, "more")
	skill(t, more, "gamma")
	cfgPath := filepath.Join(f.home, ".config/skilltri/config.json")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Sources["more"] = &config.Source{Type: "local", Path: more}
	writeJSON(t, cfgPath, cfg)
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(catalog.Path(), old, old); err != nil {
		t.Fatal(err)
	}
	if got := viewSkills(t, Compute(f.q, nil).Set[PluginDirsVar]); !reflect.DeepEqual(got, []string{"alpha", "gamma"}) {
		t.Fatalf("Q skills after the config change = %v", got)
	}
	c, err := catalog.Load(catalog.Path())
	if err != nil || len(c.Skills) != 3 {
		t.Fatalf("rebuilt catalog not saved: %v, %+v", err, c)
	}
	Compute(f.q, nil) // the catalog is now newer than the config: reused, not rebuilt
	if again, err := catalog.Load(catalog.Path()); err != nil || !again.Built.Equal(c.Built) {
		t.Fatalf("fresh catalog rebuilt: %v", err)
	}
}

func TestComputeKeepsUserBase(t *testing.T) {
	f := newFixture(t)
	env := []string{PluginDirsVar + "=/mine"}
	e := Compute(f.p, env)
	got := e.Set[PluginDirsVar]
	if !strings.HasPrefix(got, "/mine:") || e.Set[BaseVar] != "/mine" {
		t.Fatalf("first use: Set = %v", e.Set)
	}
	if names := viewSkills(t, got, "/mine"); !reflect.DeepEqual(names, []string{"alpha", "beta"}) {
		t.Fatalf("skills = %v", names)
	}

	// later runs see our own value plus the saved base: the base is not duplicated
	env = []string{PluginDirsVar + "=" + got, BaseVar + "=/mine"}
	e = Compute(f.q, env)
	if v := e.Set[PluginDirsVar]; strings.Count(v, "/mine") != 1 || !strings.HasPrefix(v, "/mine:") {
		t.Fatalf("second run: %s = %q", PluginDirsVar, v)
	}

	// leaving for a folder without skills restores exactly the user's value
	env = []string{PluginDirsVar + "=" + e.Set[PluginDirsVar], BaseVar + "=/mine"}
	e = Compute(f.x, env)
	if e.Set[PluginDirsVar] != "/mine" || e.Set[BaseVar] != "/mine" || !contains(e.Unset, NativeVar) {
		t.Fatalf("no skills: %+v", e)
	}
}

func TestComputeNoSkills(t *testing.T) {
	f := newFixture(t)
	e := Compute(f.x, []string{PluginDirsVar + "=/stale", BaseVar + "="})
	onlyMarkers(t, e, "")
	if len(e.Notes) != 0 {
		t.Fatalf("notes: %v", e.Notes)
	}
}

func TestComputeRobust(t *testing.T) {
	t.Run("broken config", func(t *testing.T) {
		f := newFixture(t)
		if err := os.WriteFile(filepath.Join(f.home, ".config/skilltri/config.json"), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		e := Compute(f.p, []string{PluginDirsVar + "=/mine"})
		onlyMarkers(t, e, "/mine")
		if len(e.Notes) == 0 {
			t.Fatal("no note for the broken config")
		}
		checkShell(t, e)
	})
	t.Run("deleted folder", func(t *testing.T) {
		f := newFixture(t)
		gone := filepath.Join(f.home, "gone")
		e := Compute(gone, nil)
		onlyMarkers(t, e, "")
		checkShell(t, e)
	})
	t.Run("empty folder argument", func(t *testing.T) {
		newFixture(t)
		onlyMarkers(t, Compute("", nil), "")
	})
	t.Run("corrupt catalog is rebuilt", func(t *testing.T) {
		f := newFixture(t)
		if err := os.MkdirAll(filepath.Dir(catalog.Path()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(catalog.Path(), []byte("not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		e := Compute(f.q, nil)
		if got := viewSkills(t, e.Set[PluginDirsVar]); !reflect.DeepEqual(got, []string{"alpha"}) {
			t.Fatalf("skills = %v", got)
		}
		if _, err := catalog.Load(catalog.Path()); err != nil {
			t.Fatalf("catalog not rebuilt: %v", err)
		}
	})
	t.Run("never fetched git source, no git, no network", func(t *testing.T) {
		home := setDirs(t)
		t.Setenv("PATH", t.TempDir()) // any git or network call would fail
		cfg := &config.Config{Version: 1, Servers: map[string]*config.Server{}, Sources: map[string]*config.Source{
			"remote": {Type: "git", URL: "https://example.com/demo/skills.git"},
		}}
		writeJSON(t, filepath.Join(home, ".config/skilltri/config.json"), cfg)
		start := time.Now()
		e := Compute(home, nil)
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("Compute took %v", d)
		}
		onlyMarkers(t, e, "")
		checkShell(t, e)
		if _, err := os.Stat(filepath.Join(home, ".local/share/skilltri/sources")); !os.IsNotExist(err) {
			t.Fatalf("sources dir touched: %v", err)
		}
	})
}

// checkShell renders e for every shell and lets the shell (if installed) parse it.
func checkShell(t *testing.T, e Env) {
	t.Helper()
	for _, sh := range []string{"zsh", "bash", "fish"} {
		out := Render(e, sh)
		for _, n := range e.Notes {
			if strings.Contains(out, n) {
				t.Fatalf("%s output contains a note: %q", sh, out)
			}
		}
		bin, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		if b, err := exec.Command(bin, "-n", "-c", out).CombinedOutput(); err != nil {
			t.Fatalf("%s rejects %q: %v\n%s", sh, out, err, b)
		}
	}
}

func TestComputeProjectDirOptIn(t *testing.T) {
	f := newFixture(t)
	e := Compute(f.p, nil)
	if _, err := os.Lstat(filepath.Join(f.p, ".agents")); !os.IsNotExist(err) {
		t.Fatalf("codex not enabled but .agents exists: %v", err)
	}
	if e.Set[NativeVar] != "claude-code" {
		t.Fatalf("native = %q", e.Set[NativeVar])
	}

	f.writeConfig(t, []string{"claude-code", "codex", "gemini-cli", "nope"})
	// outside a git work tree nothing is linked
	e = Compute(f.p, nil)
	if _, err := os.Lstat(filepath.Join(f.p, ".agents")); !os.IsNotExist(err) || e.Set[NativeVar] != "claude-code" {
		t.Fatalf("linked outside a work tree: %v %+v", err, e)
	}
	// never in HOME, even when HOME is a (dotfiles) work tree
	for _, d := range []string{f.p, f.q, f.home} {
		mkdir(t, d, ".git")
	}
	e = Compute(f.home, nil)
	if _, err := os.Lstat(filepath.Join(f.home, ".agents")); !os.IsNotExist(err) || e.Set[NativeVar] != "claude-code" {
		t.Fatalf("linked into HOME: %v %+v", err, e)
	}
	e = Compute(f.p, nil)
	for _, name := range []string{"alpha", "beta"} {
		target, err := os.Readlink(filepath.Join(f.p, ".agents/skills", name))
		if err != nil || target != filepath.Join(f.src, name) {
			t.Fatalf("link %s = %q, %v", name, target, err)
		}
	}
	if e.Set[NativeVar] != "claude-code,codex,gemini-cli" {
		t.Fatalf("native = %q", e.Set[NativeVar])
	}
	if len(e.Notes) != 1 || !strings.Contains(e.Notes[0], "nope") {
		t.Fatalf("notes = %v", e.Notes)
	}

	// only codex: no Claude view
	f.writeConfig(t, []string{"codex"})
	e = Compute(f.q, nil)
	if _, ok := e.Set[PluginDirsVar]; ok || !contains(e.Unset, PluginDirsVar) || e.Set[NativeVar] != "codex" {
		t.Fatalf("codex only: %+v", e)
	}

	// a failing sync (read-only folder) becomes a note, and codex did not deliver
	if os.Geteuid() == 0 {
		t.Skip("root ignores folder permissions")
	}
	ro := mkdir(t, f.home, "ro")
	mkdir(t, ro, ".git")
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ro, 0o755) })
	e = Compute(ro, nil)
	if len(e.Notes) == 0 || !contains(e.Unset, NativeVar) {
		t.Fatalf("read-only: %+v", e)
	}
}

func TestRender(t *testing.T) {
	e := Env{Set: map[string]string{"B": "it's", "A": `a\b`}, Unset: []string{"C"}, Notes: []string{"oops"}}
	posix := "export A='a\\b'\nexport B='it'\\''s'\nunset C\n"
	for _, sh := range []string{"zsh", "bash"} {
		if got := Render(e, sh); got != posix {
			t.Fatalf("%s:\n%s\nwant\n%s", sh, got, posix)
		}
	}
	fish := "set -gx A 'a\\\\b'\nset -gx B 'it\\'s'\nset -e C\n"
	if got := Render(e, "fish"); got != fish {
		t.Fatalf("fish:\n%s\nwant\n%s", got, fish)
	}
	if got := Render(Env{}, "zsh"); got != "" {
		t.Fatalf("empty env = %q", got)
	}
	// the shells themselves read the values back unchanged
	for _, sh := range []string{"bash", "zsh", "fish"} {
		bin, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		out, err := exec.Command(bin, "-c", Render(e, sh)+"\nprintf '%s|%s' \"$A\" \"$B\"").CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != `a\b|it's` {
			t.Fatalf("%s: %q, %v", sh, out, err)
		}
	}
}

func TestInit(t *testing.T) {
	for sh, mechanism := range map[string]string{
		"zsh":  "chpwd_functions",
		"bash": "PROMPT_COMMAND",
		"fish": "--on-variable PWD",
	} {
		s, err := Init(sh)
		if err != nil {
			t.Fatalf("%s: %v", sh, err)
		}
		for _, want := range []string{mechanism, "SKILLTRI_HOOK", sh, "command skilltri env --shell " + sh + " 2>/dev/null"} {
			if !strings.Contains(s, want) {
				t.Errorf("%s script lacks %q:\n%s", sh, want, s)
			}
		}
		if bin, err := exec.LookPath(sh); err == nil {
			if b, err := exec.Command(bin, "-n", "-c", s).CombinedOutput(); err != nil {
				t.Errorf("%s rejects its init script: %v\n%s", sh, err, b)
			}
		}
	}
	if _, err := Init("tcsh"); err == nil {
		t.Fatal("unknown shell accepted")
	}
}

// TestInitHookRuns sources the bash and zsh hooks with a fake skilltri and checks that a cd
// re-runs it and exports its output.
func TestInitHookRuns(t *testing.T) {
	bin := t.TempDir()
	fake := "#!/bin/sh\necho \"export DEMO_DIR='$PWD'\"\necho noise >&2\n"
	if err := os.WriteFile(filepath.Join(bin, "skilltri"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := rules.Normalize(t.TempDir())
	for _, sh := range []string{"bash", "zsh"} {
		path, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		s, err := Init(sh)
		if err != nil {
			t.Fatal(err)
		}
		script := s + "\ncd " + dir + "\n"
		if sh == "bash" {
			script += "eval \"$PROMPT_COMMAND\"\n" // what bash runs before the next prompt
		}
		script += "echo \"$SKILLTRI_HOOK|$DEMO_DIR\"\n"
		cmd := exec.Command(path, "-c", script)
		cmd.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin")
		out, err := cmd.Output()
		if want := sh + "|" + dir; err != nil || strings.TrimSpace(string(out)) != want {
			t.Fatalf("%s: %q, %v, want %q", sh, out, err, want)
		}
	}
}

func TestComputeFast(t *testing.T) {
	home := setDirs(t)
	src := filepath.Join(home, "skills")
	for i := range 200 {
		skill(t, src, fmt.Sprintf("skill-%03d", i))
	}
	cfg := &config.Config{Version: 1, Servers: map[string]*config.Server{}, Sources: map[string]*config.Source{
		"demo": {Type: "local", Path: src},
	}}
	writeJSON(t, filepath.Join(home, ".config/skilltri/config.json"), cfg)
	if e := Compute(home, nil); len(viewSkills(t, e.Set[PluginDirsVar])) != 200 { // builds catalog and view
		t.Fatalf("warm-up: %+v", e)
	}
	limit := 50 * time.Millisecond
	if raceEnabled {
		limit = 250 * time.Millisecond
	}
	best := time.Hour
	for range 3 {
		start := time.Now()
		Compute(home, nil)
		best = min(best, time.Since(start))
	}
	t.Logf("best of 3: %v (limit %v)", best, limit)
	if best > limit {
		t.Fatalf("Compute with a cached catalog of 200 skills took %v, limit %v", best, limit)
	}
}

func TestStampAndWatchVars(t *testing.T) {
	f := newFixture(t)
	e := Compute(f.p, nil)
	if e.Local[ViewVar] == "" || e.Local[ViewVar] != e.Set[PluginDirsVar] {
		t.Fatalf("view var = %q, plugin dirs %q", e.Local[ViewVar], e.Set[PluginDirsVar])
	}
	if e := Compute(f.x, nil); e.Local[ViewVar] != "" { // no skills in X: no view to watch
		t.Fatalf("view var without skills = %q", e.Local[ViewVar])
	}

	stamps := filepath.Join(os.Getenv("SKILLTRI_CACHE_DIR"), "hook")
	old, recent := filepath.Join(stamps, "1.stamp"), filepath.Join(stamps, "2.stamp")
	for _, p := range []string{old, recent} {
		writeJSON(t, p, nil)
	}
	month := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(old, month, month); err != nil {
		t.Fatal(err)
	}
	if err := Stamp(&e, "4242"); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(stamps, "4242.stamp")
	if st, err := os.Stat(want); err != nil || time.Since(st.ModTime()) > time.Minute {
		t.Fatalf("stamp: %v %v", st, err)
	}
	if e.Local[StampVar] != want || e.Local[ConfigVar] != filepath.Join(f.home, ".config/skilltri/config.json") ||
		e.Local[CatalogVar] != catalog.Path() {
		t.Fatalf("local = %v", e.Local)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("month-old stamp kept: %v", err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Errorf("recent stamp removed: %v", err)
	}
	for _, bad := range []string{"", "../x", "12a", "12345678901"} {
		e := Env{Local: map[string]string{StampVar: "stale"}}
		if err := Stamp(&e, bad); err == nil || e.Local[StampVar] != "" {
			t.Errorf("Stamp(%q) = %v, local %v", bad, err, e.Local)
		}
	}
}

func TestRenderLocal(t *testing.T) {
	e := Env{Set: map[string]string{"A": "1"}, Local: map[string]string{"_L": "it's", "_GONE": ""}}
	if got, want := Render(e, "zsh"), "export A='1'\nunset _GONE\n_L='it'\\''s'\n"; got != want {
		t.Fatalf("zsh:\n%s\nwant\n%s", got, want)
	}
	if got, want := Render(e, "fish"), "set -gx A '1'\nset -e _GONE\nset -g _L 'it\\'s'\n"; got != want {
		t.Fatalf("fish:\n%s\nwant\n%s", got, want)
	}
	for _, sh := range []string{"bash", "zsh"} { // set, not exported
		bin, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		out, err := exec.Command(bin, "-c", Render(e, sh)+"\nprintf '%s|' \"$_L\"; env | grep -c '^_L=' || true").CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != "it's|0" {
			t.Fatalf("%s: %q, %v", sh, out, err)
		}
	}
}

// TestInitHookRerunsOnChange sources the bash and zsh hooks with a fake skilltri that logs each
// call and prints the watch variables, then runs prompts: nothing changed → no call; a newer
// config, a newer catalog or a missing view → one call each.
func TestInitHookRerunsOnChange(t *testing.T) {
	bin, work := t.TempDir(), rules.Normalize(t.TempDir())
	cfg, cat, view := filepath.Join(work, "config.json"), filepath.Join(work, "catalog.json"), filepath.Join(work, "view")
	fake := `#!/bin/sh
echo "$SKILLTRI_HOOK_PID" >> "$DEMO_LOG"
mkdir -p "$DEMO_VIEW"
touch "$DEMO_STAMP"
echo "_SKILLTRI_STAMP='$DEMO_STAMP'"
echo "_SKILLTRI_CONFIG='$DEMO_CONFIG'"
echo "_SKILLTRI_CATALOG='$DEMO_CATALOG'"
echo "_SKILLTRI_VIEW='$DEMO_VIEW'"
`
	if err := os.WriteFile(filepath.Join(bin, "skilltri"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, sh := range []string{"bash", "zsh"} {
		path, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		s, err := Init(sh)
		if err != nil {
			t.Fatal(err)
		}
		log := filepath.Join(work, sh+".log")
		prompt := `for f in $precmd_functions; do $f; done` // what zsh runs before a prompt
		if sh == "bash" {
			prompt = `eval "$PROMPT_COMMAND"`
		}
		past, future := "touch -t 201901010000 ", "touch -t 203001010000 "
		script := "set -u\n" + past + cfg + " " + cat + "\n" + s + "\n" + prompt + "\n" + prompt + "\n" + // initial call only
			future + cfg + "\n" + prompt + "\n" + past + cfg + "\n" + prompt + "\n" + // config changed: one call
			future + cat + "\n" + prompt + "\n" + past + cat + "\n" + prompt + "\n" + // catalog changed: one call
			"rm -rf " + view + "\n" + prompt + "\n" + prompt + "\n" + // view gone: one call
			"echo \"exit=$?\"\n"
		cmd := exec.Command(path, "-c", script)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin", "DEMO_LOG="+log, "DEMO_STAMP="+filepath.Join(work, sh+".stamp"),
			"DEMO_CONFIG="+cfg, "DEMO_CATALOG="+cat, "DEMO_VIEW="+view)
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "exit=0") {
			t.Fatalf("%s: %v\n%s", sh, err, out)
		}
		b, _ := os.ReadFile(log)
		calls := strings.Fields(string(b))
		if len(calls) != 4 {
			t.Fatalf("%s: %d calls, want 4 (initial, config, catalog, view): %q", sh, len(calls), b)
		}
		if calls[0] == "" || calls[0] != calls[3] {
			t.Errorf("%s: shell pid not passed: %q", sh, calls)
		}
	}
}
