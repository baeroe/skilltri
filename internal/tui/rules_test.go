package tui

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/rules"
	"github.com/baeroe/skilltri/internal/target"
)

// rulesDialog is the open rules dialog (fails the test when there is none).
func (h *harness) rulesDialog() *rulesModal {
	h.t.Helper()
	for i := len(h.m.modals) - 1; i >= 0; i-- {
		if d, ok := h.m.modals[i].(*rulesModal); ok {
			return d
		}
	}
	h.t.Fatalf("no rules dialog open:\n%s", screen(h))
	return nil
}

func wantScreen(t *testing.T, h *harness, what string, wants ...string) {
	t.Helper()
	v := screen(h)
	for _, w := range wants {
		if !strings.Contains(v, w) {
			t.Errorf("%s: screen misses %q:\n%s", what, w, v)
		}
	}
}

func TestRulesDialogSkill(t *testing.T) {
	h := newHarness(t, TabSkills)
	cfg := demoSkills(h)
	home := h.m.opts.Home
	cfg.Sources["demo-local"].Rules = []rules.Rule{{Path: filepath.Join(home, "dev", "client-a"), Mode: rules.Tree}}
	h.writeConfig(cfg)
	h.size(120, 40)
	h.load()
	h.selectNode(skillKey("demo-local", "demo-alpha"))
	h.key("r")
	wantScreen(t, h, "open", "Rules of skill demo-alpha", "no own rules", "inherited from source demo-local",
		"~/dev/client-a", "active only in ~/dev/client-a and below",
		"/work/demo", "✗ demo-local: no include matches")

	// add tree ~/dev/x
	h.key("a")
	h.choose("mode", "tree")
	h.set("path", "~/dev/x")
	h.key("ctrl+s")
	if got := h.config().Sources["demo-local"].Skills["demo-alpha"]; got == nil || !slices.Equal(got.Rules, []rules.Rule{{Path: "~/dev/x", Mode: rules.Tree}}) {
		t.Fatalf("skill node after add = %+v", got)
	}
	if _, ok := h.m.modals[len(h.m.modals)-1].(*rulesModal); !ok {
		t.Fatalf("the add form did not close:\n%s", screen(h))
	}
	wantScreen(t, h, "after add", "1  tree", "~/dev/x", "active only in ~/dev/x and below", "(overridden by the own rules)")

	// relative path: inline error, nothing written
	before := h.rawConfig()
	h.key("a")
	h.set("path", "dev/y")
	h.key("ctrl+s")
	if fl := h.topForm().get("path"); !strings.Contains(fl.err, "absolute") {
		t.Errorf("relative path error = %q", fl.err)
	}
	wantScreen(t, h, "relative", "✗ must be an absolute path")
	if !bytes.Equal(before, h.rawConfig()) {
		t.Error("config changed by a relative path")
	}
	h.key("esc")

	// a second rule, delete the first, undo puts it back in place
	h.key("a")
	h.choose("mode", "not-tree")
	h.set("path", "~/dev/x/tmp")
	h.key("ctrl+s")
	wantScreen(t, h, "two rules", "2  not-tree", "active only in ~/dev/x and below; except ~/dev/x/tmp and below")
	h.key("k", "d")
	if got := h.config().Sources["demo-local"].Skills["demo-alpha"].Rules; !slices.Equal(got, []rules.Rule{{Path: "~/dev/x/tmp", Mode: rules.NotTree}}) {
		t.Fatalf("after delete = %+v", got)
	}
	wantScreen(t, h, "delete", "u undoes", "active everywhere except ~/dev/x/tmp and below")
	h.key("u")
	want := []rules.Rule{{Path: "~/dev/x", Mode: rules.Tree}, {Path: "~/dev/x/tmp", Mode: rules.NotTree}}
	if got := h.config().Sources["demo-local"].Skills["demo-alpha"].Rules; !slices.Equal(got, want) {
		t.Fatalf("after undo = %+v", got)
	}
	h.key("u") // nothing more to undo
	if got := h.config().Sources["demo-local"].Skills["demo-alpha"].Rules; !slices.Equal(got, want) {
		t.Fatalf("second undo changed the rules: %+v", got)
	}

	// the live check flips with the folder
	os.MkdirAll(filepath.Join(home, "dev", "x", "sub"), 0o755)
	h.key("c", "ctrl+u", "~/dev/x/sub")
	wantScreen(t, h, "check inside", "✓ demo-local/demo-alpha: included by tree")
	h.key("ctrl+u", "~/dev/x/tmp")
	wantScreen(t, h, "check excluded", "✗ demo-local/demo-alpha: excluded by not-tree")
	h.key("ctrl+u", "relative")
	wantScreen(t, h, "check relative", "enter an absolute folder")
	h.key("ctrl+u", "~/dev/x/s", "tab")
	if v := h.rulesDialog().check.Value(); v != "~/dev/x/sub/" {
		t.Errorf("check folder completed to %q", v)
	}
	wantScreen(t, h, "check completed", "~/dev/x/sub/", "✓ demo-local/demo-alpha: included by tree")
	h.key("esc") // leaves the folder input, not the dialog
	h.rulesDialog()
	h.key("esc")
	if len(h.m.modals) != 0 {
		t.Errorf("esc did not close the dialog")
	}
}

func TestRulesDialogDisabledAndGone(t *testing.T) {
	h := newHarness(t, TabSkills)
	cfg := demoSkills(h)
	cfg.Sources["demo-local"].Skills = map[string]*config.Node{"demo-beta": {Disabled: true}}
	h.writeConfig(cfg)
	h.load()
	h.selectNode(skillKey("demo-local", "demo-beta"))
	h.key("r")
	wantScreen(t, h, "disabled", "disabled", "✗ disabled: demo-local/demo-beta")

	// the node disappears while the dialog is open
	delete(cfg.Sources, "demo-local")
	h.writeConfig(cfg)
	h.run(h.m.reload())
	wantScreen(t, h, "gone", "no longer exists")
	before := h.rawConfig()
	h.key("a", "d", "u")
	if _, ok := h.m.modals[len(h.m.modals)-1].(*rulesModal); !ok || !bytes.Equal(before, h.rawConfig()) {
		t.Errorf("editing a gone node:\n%s", screen(h))
	}
}

// TestRuleNodeDisabledAncestor: a disabled server switches its tools off, whatever their rules.
func TestRuleNodeDisabledAncestor(t *testing.T) {
	cfg := demoConfig(1)
	cfg.Servers["demo-1"].Disabled = true
	cfg.Servers["demo-1"].Tools = map[string]*config.Tool{"search": {Rules: []rules.Rule{{Path: "/work", Mode: rules.Tree}}}}
	n, ok := resolveRuleNode(Snapshot{Config: cfg}, target.Target{Kind: "tool", Server: "demo-1", Tool: "search"})
	if !ok || n.explain("/home/demo") != "disabled by server demo-1" {
		t.Fatalf("explain = %q (ok %v)", n.explain("/home/demo"), ok)
	}
	if active, text, _ := checkFolder(n, "/work", "/home/demo"); active || text != "disabled: demo-1" {
		t.Errorf("check = %v %q", active, text)
	}
	if _, ok := resolveRuleNode(Snapshot{Config: cfg}, target.Target{Kind: "server", Server: "none"}); ok {
		t.Error("a missing server resolved")
	}
}

func TestRulesDialogPlugin(t *testing.T) {
	h := newHarness(t, TabSkills)
	cfg := demoSkills(h)
	cfg.Sources["demo-market"].Rules = []rules.Rule{{Path: "/work/official", Mode: rules.Only}}
	h.writeConfig(cfg)
	h.load()
	h.selectNode(pluginKey("demo-market", "writing"))
	h.key("r")
	wantScreen(t, h, "plugin", "Rules of plugin writing", "inherited from source demo-market", "active only in /work/official")
	h.key("a")
	h.choose("mode", "only")
	h.set("path", "/work/demo")
	h.key("enter") // enter on the last field saves
	if n := h.config().Sources["demo-market"].Plugins["writing"]; n == nil || len(n.Rules) != 1 || n.Disabled {
		t.Fatalf("plugin node = %+v", n)
	}
	wantScreen(t, h, "plugin check", "✓ demo-market/writing: included by only /work/demo")

	// a skill of the plugin inherits from the plugin, the nearest level with rules
	h.key("esc")
	h.selectNode(skillKey("demo-market", "writing-tools:outline"))
	h.key("r")
	wantScreen(t, h, "plugin skill", "inherited from plugin writing", "active only in /work/demo")

	// warnings of RuleAdd (a target the catalog does not know) show in the dialog
	_, cmd := h.m.Update(ruleSavedMsg{d: h.rulesDialog(), text: "added rule tree /x", warns: []string{"demo warning: no such skill"}})
	h.run(cmd)
	wantScreen(t, h, "warnings", "! demo warning: no such skill", "added rule tree /x")
}

func TestRulesDialogServers(t *testing.T) {
	h := newHarness(t, TabServers)
	cfg, st, wh := demoServers(h)
	h.writeConfig(cfg)
	h.status, h.which = st, wh
	h.load()
	h.key("r") // demo-broken, the first row
	wantScreen(t, h, "server", "Rules of server demo-broken", "1  tree", "3  not",
		"active only in /work/a and below or /work/b and below; except /work/c")
	h.key("esc")

	h.serversTab().sel = "demo-ready"
	h.key("t", "r")
	wantScreen(t, h, "tool", "Rules of tool search of demo-ready", "inherited from server demo-ready", "active only in ~/x",
		"✗ demo-ready: no include matches")
	h.key("a")
	h.set("path", "/work/demo")
	h.choose("mode", "only")
	h.key("ctrl+s")
	if tl := h.config().Servers["demo-ready"].Tools["search"]; tl == nil || len(tl.Rules) != 1 {
		t.Fatalf("tool node = %+v", tl)
	}
	wantScreen(t, h, "tool check", "✓ demo-ready/search: included by only /work/demo")
}

func (h *harness) serversTab() *serversTab { return h.m.tabs[TabServers].(*serversTab) }

func TestRulesDialogPluginServerRow(t *testing.T) {
	h := newHarness(t, TabSkills)
	cfg := demoSkills(h)
	h.load()
	h.selectNode(serverKey("demo-market", "writing", "notes"))
	h.key("r")
	if len(h.m.modals) != 0 {
		t.Fatal("rules opened for a server that is not enabled")
	}
	wantScreen(t, h, "not enabled", "enable the server first")
	cfg.Servers["demo-notes"] = &config.Server{Type: "stdio", Command: "demo-server",
		Origin: &config.Origin{Source: "demo-market", Plugin: "writing", Server: "notes"}}
	h.writeConfig(cfg)
	h.run(h.m.reload())
	h.selectNode(serverKey("demo-market", "writing", "notes"))
	h.key("r")
	wantScreen(t, h, "enabled", "Rules of server demo-notes")
}

func TestParseRule(t *testing.T) {
	for _, tc := range []struct {
		mode, path      string
		modeErr, pathOK bool
	}{
		{"tree", "~/dev/x", false, true},
		{"only", "/abs", false, true},
		{"bogus", "/abs", true, true},
		{"not", "rel/dir", false, false},
		{"not-tree", "", false, false},
		{"tree", "/work/../etc", false, false},
		{"tree", "~/..", false, false},
		{"tree", "/work/..x", false, true},
	} {
		r, me, pe := parseRule(tc.mode, tc.path)
		if (me != "") != tc.modeErr || (pe == "") != tc.pathOK {
			t.Errorf("parseRule(%q, %q) = %+v, %q, %q", tc.mode, tc.path, r, me, pe)
		}
		if me == "" && pe == "" && (string(r.Mode) != tc.mode || r.Path != tc.path) {
			t.Errorf("parseRule(%q, %q) = %+v", tc.mode, tc.path, r)
		}
	}
}

func TestExplainRules(t *testing.T) {
	home := "/home/demo"
	r := func(m rules.Mode, p string) rules.Rule { return rules.Rule{Path: p, Mode: m} }
	for _, tc := range []struct {
		rs   []rules.Rule
		want string
	}{
		{nil, "active everywhere"},
		{[]rules.Rule{r(rules.Only, "/home/demo/a")}, "active only in ~/a"},
		{[]rules.Rule{r(rules.Tree, "~/dev/client-a")}, "active only in ~/dev/client-a and below"},
		{[]rules.Rule{r(rules.Not, "/home/demo/b")}, "active everywhere except ~/b"},
		{[]rules.Rule{r(rules.NotTree, "~/dev/work")}, "active everywhere except ~/dev/work and below"},
		{[]rules.Rule{r(rules.Not, "/b"), r(rules.NotTree, "/c")}, "active everywhere except /b, /c and below"},
		{[]rules.Rule{r(rules.Tree, "/a"), r(rules.Only, "/b"), r(rules.Not, "/a/x")}, "active only in /a and below or /b; except /a/x"},
	} {
		if got := explainRules(tc.rs, home); got != tc.want {
			t.Errorf("explainRules(%v) = %q, want %q", tc.rs, got, tc.want)
		}
	}
}

func TestPathInputComplete(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{"dev/client-a", "dev/client-b", "dev/other", "docs", ".hidden"} {
		os.MkdirAll(filepath.Join(home, d), 0o755)
	}
	os.WriteFile(filepath.Join(home, "dev", "client-file"), nil, 0o644)
	os.Symlink(filepath.Join(home, "docs"), filepath.Join(home, "dev", "linked"))
	for i := range 10 {
		os.MkdirAll(filepath.Join(home, "many", fmt.Sprintf("x%02d", i)), 0o755)
	}
	p := &pathInput{home: home}
	ti := textinput.New()
	try := func(value, want string, changed bool, cands ...string) {
		t.Helper()
		ti.SetValue(value)
		if got := p.complete(&ti); got != changed || ti.Value() != want || !slices.Equal(p.cands, cands) {
			t.Errorf("complete(%q) = %v, %q, %v; want %v, %q, %v", value, got, ti.Value(), p.cands, changed, want, cands)
		}
	}
	try("~/de", "~/dev/", true)                                    // one folder; ~ kept
	try("~/dev/cl", "~/dev/client-", true, "client-a", "client-b") // common prefix, files skipped
	try("~/dev/client-", "~/dev/client-", true, "client-a", "client-b")
	try("~/dev/client-a", "~/dev/client-a/", true)
	try("~/dev/client-a/", "~/dev/client-a/", false) // no folders below
	try("~/dev/li", "~/dev/linked/", true)           // a symlink to a folder
	try(home+"/do", home+"/docs/", true)             // absolute
	try("~/", "~/", true, "dev", "docs", "many")     // hidden folders only with a "." prefix
	try("~/.h", "~/.hidden/", true)
	try("~/nope/x", "~/nope/x", false)
	try("dev", "dev", false) // relative: nothing to complete
	try("~", "~/", true)
	ti.SetValue("~/many/")
	p.complete(&ti)
	if len(p.cands) != maxPathCands || p.more != 2 || ti.Value() != "~/many/x0" {
		t.Errorf("many: %q %v +%d", ti.Value(), p.cands, p.more)
	}
	if l := strings.Join(p.candLines(80), "\n"); !strings.Contains(l, "x00/") || !strings.Contains(l, "+2 more") {
		t.Errorf("candidate lines = %q", l)
	}
}

func TestSourceFormPathCompletes(t *testing.T) {
	h := newHarness(t, TabSkills)
	demoSkills(h)
	os.MkdirAll(filepath.Join(h.m.opts.Home, "skills"), 0o755)
	os.MkdirAll(filepath.Join(h.m.opts.Home, "skills-old"), 0o755)
	h.load()
	h.key("a")
	h.set("name", "demo-home")
	h.set("path", "~/sk")
	h.key("tab")
	f := h.topForm()
	if v := f.get("path").input.Value(); v != "~/skills" || f.current().key != "path" {
		t.Fatalf("path = %q (focus %s)", v, f.current().key)
	}
	wantScreen(t, h, "candidates", "skills/", "skills-old/")
	h.key("/", "tab") // ~/skills/ has no folders: tab moves on
	if f.current().key == "path" {
		t.Errorf("tab without completion did not move to the next field")
	}
	h.key("ctrl+s")
	if s := h.config().Sources["demo-home"]; s == nil || s.Path != filepath.Join(h.m.opts.Home, "skills") {
		t.Fatalf("saved: %+v", s)
	}
}

// TestRulesNoOverflow: the dialog with long names and paths at every size.
func TestRulesNoOverflow(t *testing.T) {
	h := newHarness(t, TabServers)
	long := strings.Repeat("n", 32)
	cfg := demoConfig(0)
	var rs []rules.Rule
	for i := range 12 {
		rs = append(rs, rules.Rule{Path: "/" + strings.Repeat(fmt.Sprintf("p%d/", i), 50), Mode: rules.NotTree})
	}
	cfg.Servers[long] = &config.Server{Type: "stdio", Command: "demo-server", Rules: rs,
		Tools: map[string]*config.Tool{strings.Repeat("t", 60): {}}}
	h.writeConfig(cfg)
	h.m.opts.Cwd = "/" + strings.Repeat("c/", 100)
	h.load()
	for _, sz := range [][2]int{{40, 12}, {89, 24}, {90, 24}, {300, 50}} {
		h.size(sz[0], sz[1])
		h.key("r")
		checkScreen(t, h.m.View(), sz[0], sz[1], "rules dialog")
		h.key("j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j")
		checkScreen(t, h.m.View(), sz[0], sz[1], "rules dialog scrolled")
		if !strings.Contains(screen(h), "12  not-tree") && sz[1] >= 24 {
			t.Errorf("selected last rule not shown at %v:\n%s", sz, screen(h))
		}
		h.key("a")
		h.set("path", "/"+strings.Repeat("q", 200))
		checkScreen(t, h.m.View(), sz[0], sz[1], "add form")
		h.key("esc", "esc")
	}
}

// runHolding runs cmd like harness.run but keeps the snapshot loads it produces (returned, not
// delivered), so a test sees the dialog before the reload lands.
func (h *harness) runHolding(cmd tea.Cmd) []tea.Cmd {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case snapshotMsg: // loaded now, delivered when the test runs the returned command
		return []tea.Cmd{func() tea.Msg { return msg }}
	case nil, tea.QuitMsg, tickMsg:
		return nil
	case tea.BatchMsg:
		var held []tea.Cmd
		for _, c := range msg {
			held = append(held, h.runHolding(c)...)
		}
		return held
	default:
		_, next := h.m.Update(msg)
		return h.runHolding(next)
	}
}

// keyHolding presses k and holds the snapshot loads it causes.
func (h *harness) keyHolding(k string) []tea.Cmd {
	_, cmd := h.m.Update(keyMsg(k))
	return h.runHolding(cmd)
}

func TestRulesDialogBeforeReload(t *testing.T) {
	h := newHarness(t, TabServers)
	a, b, c := rules.Rule{Path: "/a", Mode: rules.Tree}, rules.Rule{Path: "/b", Mode: rules.Tree}, rules.Rule{Path: "/c", Mode: rules.Not}
	cfg := demoConfig(1)
	cfg.Servers["demo-1"].Rules = []rules.Rule{a, b, c}
	h.writeConfig(cfg)
	h.load()
	got := func() []rules.Rule { return h.config().Servers["demo-1"].Rules }
	h.key("r")

	// d d: the second d waits for the reload instead of deleting by a stale number
	held := h.keyHolding("d")
	if !slices.Equal(got(), []rules.Rule{b, c}) {
		t.Fatalf("after d = %+v", got())
	}
	wantScreen(t, h, "before reload", "1  tree", "/b") // the dialog shows the written list
	if h.keyHolding("d") != nil || !slices.Equal(got(), []rules.Rule{b, c}) {
		t.Fatalf("d before the reload changed the rules: %+v", got())
	}
	for _, cmd := range held {
		h.run(cmd)
	}

	// d u: undo before the reload is ignored, after it restores exactly once
	held = h.keyHolding("d")
	if !slices.Equal(got(), []rules.Rule{c}) {
		t.Fatalf("after second d = %+v", got())
	}
	h.keyHolding("u")
	if !slices.Equal(got(), []rules.Rule{c}) {
		t.Fatalf("u before the reload wrote: %+v", got())
	}
	for _, cmd := range held {
		h.run(cmd)
	}
	h.key("u")
	if !slices.Equal(got(), []rules.Rule{b, c}) {
		t.Fatalf("after undo = %+v", got())
	}
	h.key("u")
	if !slices.Equal(got(), []rules.Rule{b, c}) {
		t.Fatalf("second undo wrote: %+v", got())
	}

	// undo skips a rule that is back already
	h.key("d") // deletes b
	cfg.Servers["demo-1"].Rules = []rules.Rule{c, b}
	h.writeConfig(cfg)
	h.run(h.m.reload())
	h.key("u")
	if !slices.Equal(got(), []rules.Rule{c, b}) {
		t.Fatalf("undo duplicated a rule: %+v", got())
	}
	wantScreen(t, h, "already there", "already there")

	// the config changed elsewhere and was not reloaded yet: delete refuses
	cfg.Servers["demo-1"].Rules = []rules.Rule{a, c, b}
	h.writeConfig(cfg)
	h.key("d")
	if !slices.Equal(got(), []rules.Rule{a, c, b}) {
		t.Fatalf("delete on a stale list wrote: %+v", got())
	}
	wantScreen(t, h, "changed", "changed meanwhile")
}

func TestRulesNeedConfig(t *testing.T) {
	for _, tab := range []Tab{TabServers, TabSkills} {
		h := newHarness(t, tab)
		os.MkdirAll(filepath.Dir(h.m.opts.Env.ConfigPath), 0o700)
		os.WriteFile(h.m.opts.Env.ConfigPath, []byte("{broken"), 0o600)
		h.load()
		h.key("r")
		if len(h.m.modals) != 0 {
			t.Errorf("%v: a dialog opened without a config", tab)
		}
		wantScreen(t, h, "need config", "fix the config first")
	}
}

// TestRulesCheckCached: the live check is evaluated once per folder value and rule set, not on
// every render.
func TestRulesCheckCached(t *testing.T) {
	h := newHarness(t, TabServers)
	h.writeConfig(demoConfig(1))
	h.load()
	h.key("r")
	screen(h)
	d := h.rulesDialog()
	d.memo.text = "demo cached reason"
	wantScreen(t, h, "cached", "demo cached reason")
	h.key("c", "/")
	if v := screen(h); strings.Contains(v, "demo cached reason") || !strings.Contains(v, "no rules: active everywhere") {
		t.Errorf("not re-evaluated for a new folder:\n%s", v)
	}
}
