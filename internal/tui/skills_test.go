package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/daemon"
)

// writeSkill writes root/name/SKILL.md with description desc.
func writeSkill(t *testing.T, root, name, desc string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + desc + "\n---\nBody.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// demoSkills writes a config with the plan-2 testdata marketplace (plus hooks and agents folders in
// its writing plugin) and a local source with two skills.
func demoSkills(h *harness) *config.Config {
	t := h.t
	t.Helper()
	market := filepath.Join(h.dir, "market")
	if err := os.CopyFS(market, os.DirFS("../marketplace/testdata/demo-market")); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"hooks", "agents"} {
		os.MkdirAll(filepath.Join(market, "plugins", "writing", d), 0o755)
	}
	local := filepath.Join(h.dir, "local-skills")
	writeSkill(t, local, "demo-alpha", "Alpha demo skill.")
	writeSkill(t, local, "demo-beta", "Beta demo skill.")
	cfg := &config.Config{Version: 1, Servers: map[string]*config.Server{}, Sources: map[string]*config.Source{
		"demo-local":  {Type: "local", Path: local},
		"demo-market": {Type: "marketplace", Path: market},
	}}
	h.writeConfig(cfg)
	return cfg
}

// addMany adds the local source demo-many with n skills.
func addMany(h *harness, cfg *config.Config, n int) {
	root := filepath.Join(h.dir, "many")
	for i := 1; i <= n; i++ {
		writeSkill(h.t, root, fmt.Sprintf("demo-many-%02d", i), "Many demo skill.")
	}
	cfg.Sources["demo-many"] = &config.Source{Type: "local", Path: root}
	h.writeConfig(cfg)
}

func (h *harness) skills() *skillsTab { return h.m.tabs[TabSkills].(*skillsTab) }

// selectNode selects the tree node key (it must be shown).
func (h *harness) selectNode(key string) {
	h.t.Helper()
	t := h.skills()
	if !slices.ContainsFunc(t.rows(h.m), func(r treeRow) bool { return r.node.key == key }) {
		h.t.Fatalf("node %s not shown:\n%s", key, screen(h))
	}
	t.sel = key
}

// rawLine is the rendered (styled) line whose plain text contains s.
func rawLine(h *harness, s string) string {
	for _, l := range strings.Split(h.m.View(), "\n") {
		if strings.Contains(ansi.Strip(l), s) {
			return l
		}
	}
	return ""
}

func TestSkillsTree(t *testing.T) {
	h := newHarness(t, TabSkills)
	cfg := demoSkills(h)
	addMany(h, cfg, 11)
	h.size(160, 40)
	h.load()
	v := screen(h)
	for _, want := range []string{
		"▾ demo-local", "✓ demo-alpha", "Alpha demo skill.",
		"▾ demo-market", "marketplace",
		"▾ writing (writing-tools)", "2 skills", "agents hooks",
		"server notes — available", "server docs — available",
		"✓ writing-tools:outline", "Draft an outline for a text.",
		"remote-one", "not fetched yet",
		"▸ demo-many", "11 skills",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("tree misses %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "demo-many-01") {
		t.Errorf("demo-many (11 children) is not collapsed:\n%s", v)
	}

	// enter expands, enter again collapses
	h.selectNode(srcKey("demo-many"))
	h.key("enter")
	if v = screen(h); !strings.Contains(v, "▾ demo-many") || !strings.Contains(v, "demo-many-11") {
		t.Errorf("not expanded:\n%s", v)
	}
	h.key("enter")
	if v = screen(h); strings.Contains(v, "demo-many-01") {
		t.Errorf("not collapsed:\n%s", v)
	}

	// details of a source, a plugin and a skill
	h.selectNode(srcKey("demo-market"))
	h.key("k", "j") // re-render through a key
	v = screen(h)
	for _, want := range []string{"Type", "marketplace", "Path", "market", "Interval", "1h (default)", "Plugins", "enabled by default"} {
		if !strings.Contains(v, want) {
			t.Errorf("source details miss %q:\n%s", want, v)
		}
	}
	h.selectNode(pluginKey("demo-market", "writing"))
	h.key("k", "j")
	v = screen(h)
	for _, want := range []string{"writing-tools", "plugins/writing", "Not delivered", "agents, hooks", "notes", "available"} {
		if !strings.Contains(v, want) {
			t.Errorf("plugin details miss %q:\n%s", want, v)
		}
	}
	h.selectNode(skillKey("demo-market", "writing-tools:outline"))
	h.key("k", "j")
	v = screen(h)
	for _, want := range []string{"skills/outline", "Draft an outline for a text.", "everywhere"} {
		if !strings.Contains(v, want) {
			t.Errorf("skill details miss %q:\n%s", want, v)
		}
	}

	// a disabled plugin is dimmed and says so (styles rendered although stdout is no terminal)
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(2) // termenv.ANSI
	defer lipgloss.SetColorProfile(old)
	cfg.Sources["demo-market"].Plugins = map[string]*config.Node{"writing": {Disabled: true}}
	h.writeConfig(cfg)
	h.run(h.m.reload())
	l := rawLine(h, "writing (writing-tools)")
	if !strings.Contains(ansi.Strip(l), "disabled") || !strings.Contains(l, "\x1b[2m") {
		t.Errorf("disabled plugin row: %q", l)
	}
	if v = screen(h); strings.Contains(v, "writing-tools:outline") {
		t.Errorf("a disabled plugin delivers skills:\n%s", v)
	}
}

func TestFilterExpands(t *testing.T) {
	h := newHarness(t, TabSkills)
	cfg := demoSkills(h)
	addMany(h, cfg, 11)
	h.load()
	h.key("/", "many-07", "enter")
	v := screen(h)
	if !strings.Contains(v, "demo-many-07") || strings.Contains(v, "demo-many-03") || strings.Contains(v, "demo-alpha") {
		t.Errorf("filter by ID:\n%s", v)
	}
	if h.skills().sel != skillKey("demo-many", "demo-many-07") {
		t.Errorf("selection = %q, want the match", h.skills().sel)
	}
	h.key("esc")
	h.key("/", "an outline", "enter") // description
	if v = screen(h); !strings.Contains(v, "writing-tools:outline") || strings.Contains(v, "proofread") || strings.Contains(v, "demo-alpha") {
		t.Errorf("filter by description:\n%s", v)
	}
	h.key("esc")
	if v = screen(h); strings.Contains(v, "demo-many-07") || !strings.Contains(v, "demo-alpha") {
		t.Errorf("esc did not clear the filter:\n%s", v)
	}
}

func TestFilterIgnoresExpandOnForcedAncestors(t *testing.T) {
	h := newHarness(t, TabSkills)
	demoSkills(h)
	h.load()
	h.key("/", "alpha", "enter")
	h.selectNode(srcKey("demo-local")) // shown open because a child matches
	h.key("enter", "left", "h")
	if _, set := h.skills().open[srcKey("demo-local")]; set {
		t.Errorf("expand keys changed the hidden state of a forced-open ancestor: %v", h.skills().open)
	}
	if h.skills().sel != srcKey("demo-local") {
		t.Errorf("selection moved to %q", h.skills().sel)
	}
	h.key("esc")
	if v := screen(h); !strings.Contains(v, "▾ demo-local") || !strings.Contains(v, "demo-beta") {
		t.Errorf("demo-local collapsed after clearing the filter:\n%s", v)
	}

	// left on a forced row still moves to its parent
	h.key("/", "outline", "enter")
	h.selectNode(pluginKey("demo-market", "writing"))
	h.key("left")
	if h.skills().sel != srcKey("demo-market") || len(h.skills().open) != 0 {
		t.Errorf("left on a forced row: sel %q, open %v", h.skills().sel, h.skills().open)
	}
}

// choose selects value in the choice field key of the top form.
func (h *harness) choose(key, value string) {
	h.t.Helper()
	fl := h.topForm().get(key)
	i := slices.Index(fl.choices, value)
	if i < 0 {
		h.t.Fatalf("%s has no choice %q", key, value)
	}
	fl.choice = i
}

func TestAddSourceForm(t *testing.T) {
	h := newHarness(t, TabSkills)
	demoSkills(h)
	h.load()
	before := h.rawConfig()
	h.key("a")
	h.topForm()
	for _, c := range []struct {
		typ                                         string
		name, path, url, sub, every, field, wantErr string
	}{
		{"local", "bad name!", "/demo/skills", "", "", "", "name", "letters, digits and '-'"},
		{"local", "demo-new", "", "", "", "", "path", "required"},
		{"local", "demo-new", "rel/skills", "", "", "", "path", "absolute path"},
		{"git", "demo-new", "", "", "", "", "url", "required"},
		{"git", "demo-new", "", "https://example.com/demo.git", "../up", "", "subpath", "relative folder inside"},
		{"git", "demo-new", "", "https://example.com/demo.git", "", "soon", "every", "a duration"},
		{"marketplace", "demo-new", "/demo/market", "demo/market", "", "", "url", "a path or a url, not both"},
		{"marketplace", "demo-new", "", "", "", "", "url", "a path or a url"},
		{"marketplace", "demo-new", "demo/market", "", "", "", "path", "absolute path"},
	} {
		h.choose("type", c.typ)
		h.set("name", c.name)
		h.set("path", c.path)
		h.set("url", c.url)
		h.set("subpath", c.sub)
		h.set("every", c.every)
		h.key("ctrl+s")
		f := h.topForm()
		if fl := f.get(c.field); !strings.Contains(fl.err, c.wantErr) {
			t.Errorf("%+v: %s error = %q, want %q", c, c.field, fl.err, c.wantErr)
		}
		if v := screen(h); !strings.Contains(v, "✗ ") {
			t.Errorf("%+v: no inline error:\n%s", c, v)
		}
		if f.current().key != c.field {
			t.Errorf("%+v: focus on %s, want %s", c, f.current().key, c.field)
		}
		if string(h.rawConfig()) != string(before) {
			t.Fatalf("%+v: config written", c)
		}
	}
	// a taken name comes back from the action
	h.choose("type", "local")
	h.set("name", "demo-local")
	h.set("path", "/demo/skills")
	h.key("ctrl+s")
	if fl := h.topForm().get("name"); !strings.Contains(fl.err, "already exists") {
		t.Errorf("taken name: %q", fl.err)
	}
	if string(h.rawConfig()) != string(before) {
		t.Fatal("config written for a taken name")
	}

	// marketplace from a URL with plugins off by default
	h.choose("type", "marketplace")
	h.set("name", "demo-new")
	h.set("path", "")
	h.set("url", "demo/market")
	h.set("ref", "v1")
	h.choose("plugins", "disabled")
	h.key("ctrl+s")
	if len(h.m.modals) != 0 {
		t.Fatalf("form still open:\n%s", screen(h))
	}
	if s := h.config().Sources["demo-new"]; s == nil || s.Type != "marketplace" || s.URL != "demo/market" || s.Ref != "v1" || s.PluginsDefault != config.PluginsDisabled || s.Path != "" {
		t.Fatalf("saved marketplace: %+v", s)
	}
	if v := screen(h); !strings.Contains(v, "added source demo-new") {
		t.Errorf("status:\n%s", v)
	}

	// git with interval and subpath; local with ~
	h.key("a")
	h.choose("type", "git")
	h.set("name", "demo-git")
	h.set("url", "https://example.com/demo.git")
	h.set("subpath", "skills")
	h.set("every", "30m")
	h.key("ctrl+s")
	if s := h.config().Sources["demo-git"]; s == nil || s.Type != "git" || s.Subpath != "skills" || s.UpdateEvery != "30m" || s.PluginsDefault != "" {
		t.Fatalf("saved git: %+v", s)
	}
	h.key("a")
	h.set("name", "demo-home")
	h.set("path", "~/skills")
	h.key("ctrl+s")
	if s := h.config().Sources["demo-home"]; s == nil || s.Type != "local" || s.Path != filepath.Join(h.m.opts.Home, "skills") {
		t.Fatalf("saved local: %+v", s)
	}

	// edit keeps the disabled flag and the rules
	cfg := h.config()
	cfg.Sources["demo-git"].Disabled = true
	h.writeConfig(cfg)
	h.run(h.m.reload())
	h.selectNode(srcKey("demo-git"))
	h.key("e")
	if fl := h.topForm().get("url"); fl.value() != "https://example.com/demo.git" {
		t.Fatalf("edit form url = %q", fl.value())
	}
	h.set("ref", "main")
	h.key("ctrl+s")
	if s := h.config().Sources["demo-git"]; s == nil || s.Ref != "main" || !s.Disabled || s.UpdateEvery != "30m" {
		t.Fatalf("edited git: %+v", s)
	}

	// enabled elsewhere while the form is open (no reload yet): saving keeps it enabled
	h.key("e")
	cfg = h.config()
	cfg.Sources["demo-git"].Disabled = false
	h.writeConfig(cfg)
	h.set("ref", "v2")
	h.key("ctrl+s")
	if s := h.config().Sources["demo-git"]; s == nil || s.Ref != "v2" || s.Disabled {
		t.Fatalf("save re-disabled the source: %+v", s)
	}
}

func TestNewSkillOpensEditor(t *testing.T) {
	h := newHarness(t, TabSkills)
	demoSkills(h)
	var edited []string
	h.m.opts.Editor = func(p string) *exec.Cmd { edited = append(edited, p); return exec.Command("true") }
	h.load()
	h.key("n")
	h.set("name", "Bad Name")
	h.key("enter")
	if fl := h.topForm().get("name"); !strings.Contains(fl.err, "invalid skill name") || len(edited) != 0 {
		t.Fatalf("invalid name: err %q, edited %v", fl.err, edited)
	}
	h.set("name", "demo-new")
	h.key("enter")
	want := filepath.Join(filepath.Dir(h.m.opts.Env.ConfigPath), "skills", "demo-new", "SKILL.md")
	if !slices.Equal(edited, []string{want}) {
		t.Fatalf("editor got %v, want %s", edited, want)
	}
	b, err := os.ReadFile(want)
	if err != nil || !strings.Contains(string(b), "---\nname: demo-new\n") {
		t.Fatalf("SKILL.md: %q, %v", b, err)
	}
	if len(h.m.modals) != 0 {
		t.Fatalf("prompt still open:\n%s", screen(h))
	}
	if h.config().Sources["local"] == nil {
		t.Fatal("no local source")
	}

	// the editor returning reloads; a failing editor is reported
	h.run(func() tea.Msg { return editorDoneMsg{path: want} })
	if v := screen(h); !strings.Contains(v, "✓ demo-new") {
		t.Errorf("new skill not shown after the editor:\n%s", v)
	}
	h.run(func() tea.Msg { return editorDoneMsg{path: want, err: errors.New("exit status 1")} })
	if v := screen(h); !strings.Contains(v, "editor: exit status 1") {
		t.Errorf("editor error:\n%s", v)
	}

	// o opens the selected skill
	h.selectNode(skillKey("demo-local", "demo-alpha"))
	h.key("o")
	if got := edited[len(edited)-1]; got != filepath.Join(h.dir, "local-skills", "demo-alpha", "SKILL.md") {
		t.Errorf("o opened %s", got)
	}
	h.selectNode(srcKey("demo-local"))
	n := len(edited)
	h.key("o")
	if v := screen(h); len(edited) != n || !strings.Contains(v, "select a skill") {
		t.Errorf("o on a source:\n%s", v)
	}
}

// collect runs cmd and returns its messages, batches flattened.
func collect(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if b, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range b {
			out = append(out, collect(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// startBlocked presses key and runs its command in the background; the fake Update must report
// on started.
func startBlocked(t *testing.T, h *harness, key string, started chan []string) chan []tea.Msg {
	t.Helper()
	_, cmd := h.m.Update(keyMsg(key))
	out := make(chan []tea.Msg, 1)
	go func() { out <- collect(cmd) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("update did not start")
	}
	return out
}

func waitMsgs(t *testing.T, ch chan []tea.Msg) []tea.Msg {
	t.Helper()
	select {
	case msgs := <-ch:
		return msgs
	case <-time.After(5 * time.Second):
		t.Fatal("update did not return after cancel")
	}
	return nil
}

// noLeak waits until the goroutine count is back to at most before+1.
func noLeak(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+1 {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines: %d before, %d after", before, runtime.NumGoroutine())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestUpdateAllCancellable(t *testing.T) {
	h := newHarness(t, TabSkills)
	demoSkills(h)
	h.load()
	started := make(chan []string, 1)
	var via []bool
	h.m.opts.Update = func(ctx context.Context, names []string, viaDaemon bool) (daemon.UpdateReply, error) {
		via = append(via, viaDaemon)
		started <- names
		<-ctx.Done()
		return daemon.UpdateReply{}, ctx.Err()
	}
	before := runtime.NumGoroutine()
	out := startBlocked(t, h, "U", started)
	if v := screen(h); !strings.Contains(v, "updating all sources") || !strings.Contains(v, "esc on Skills cancels") {
		t.Errorf("no busy line:\n%s", v)
	}
	h.key("j", "k") // the UI stays responsive
	h.key("esc")
	if v := screen(h); !strings.Contains(v, "update cancelled") {
		t.Errorf("cancel not reported:\n%s", v)
	}
	for _, msg := range waitMsgs(t, out) {
		_, cmd := h.m.Update(msg)
		h.run(cmd)
	}
	if v := screen(h); !strings.Contains(v, "update cancelled") || strings.Contains(v, "updating") {
		t.Errorf("the late result changed the status line:\n%s", v)
	}
	noLeak(t, before)

	// through the daemon: esc stops waiting
	h.status = readyStatus(0, 0)
	h.run(h.m.reload())
	out = startBlocked(t, h, "U", started)
	h.key("esc")
	if v := screen(h); !strings.Contains(v, "stopped waiting for the daemon") {
		t.Errorf("daemon cancel:\n%s", v)
	}
	for _, msg := range waitMsgs(t, out) { // the stopped run ends; a new update may start
		_, cmd := h.m.Update(msg)
		h.run(cmd)
	}
	if !slices.Equal(via, []bool{false, true}) {
		t.Errorf("viaDaemon = %v", via)
	}

	// quitting cancels
	out = startBlocked(t, h, "U", started)
	h.m.Update(keyMsg("ctrl+c"))
	waitMsgs(t, out)
	noLeak(t, before)
}

func TestUpdateResults(t *testing.T) {
	h := newHarness(t, TabSkills)
	demoSkills(h)
	h.load()
	var got [][]string
	h.m.opts.Update = func(ctx context.Context, names []string, viaDaemon bool) (daemon.UpdateReply, error) {
		got = append(got, names)
		return daemon.UpdateReply{Sources: []catalog.SourceState{
			{Name: "demo-market", Commit: "abc1234def"},
			{Name: "demo-market/plugins/remote-one", Error: "clone failed"},
		}}, nil
	}
	h.selectNode(pluginKey("demo-market", "writing"))
	h.key("u")
	if v := screen(h); !strings.Contains(v, "updated 2 sources, 1 failed: demo-market/plugins/remote-one: clone failed") {
		t.Errorf("results:\n%s", v)
	}
	h.key("U")
	if !slices.EqualFunc(got, [][]string{{"demo-market"}, nil}, slices.Equal) {
		t.Errorf("names = %q", got)
	}
	h.selectNode(srcKey("demo-local"))
	h.key("u")
	if v := screen(h); !strings.Contains(v, "demo-local is a local folder: nothing to update") || len(got) != 2 {
		t.Errorf("update of a local source:\n%s", v)
	}
}

func TestTogglePluginAndSkill(t *testing.T) {
	h := newHarness(t, TabSkills)
	cfg := demoSkills(h)
	h.load()
	h.selectNode(pluginKey("demo-market", "writing"))
	h.key(" ")
	if n := h.config().Sources["demo-market"].Plugins["writing"]; n == nil || !n.Disabled {
		t.Fatalf("plugin node after disable: %+v", n)
	}
	if v := screen(h); !strings.Contains(v, "disabled plugin writing") {
		t.Errorf("status:\n%s", v)
	}
	h.key(" ")
	if n := h.config().Sources["demo-market"].Plugins["writing"]; n == nil || n.Disabled {
		t.Fatalf("plugin node after enable: %+v", n)
	}

	// plugins off by default: a plugin without a node shows disabled; space enables it
	cfg = h.config()
	cfg.Sources["demo-market"].PluginsDefault = config.PluginsDisabled
	delete(cfg.Sources["demo-market"].Plugins, "writing")
	h.writeConfig(cfg)
	h.run(h.m.reload())
	if l := ansi.Strip(rawLine(h, "remote-one")); !strings.Contains(l, "disabled") {
		t.Errorf("remote-one row: %q", l)
	}
	h.selectNode(pluginKey("demo-market", "remote-one"))
	h.key(" ")
	if n := h.config().Sources["demo-market"].Plugins["remote-one"]; n == nil || n.Disabled {
		t.Fatalf("remote-one node: %+v", n)
	}

	h.selectNode(skillKey("demo-local", "demo-alpha"))
	h.key(" ")
	if n := h.config().Sources["demo-local"].Skills["demo-alpha"]; n == nil || !n.Disabled {
		t.Fatalf("skill node: %+v", n)
	}
	if v := screen(h); !strings.Contains(v, "✗ demo-alpha") {
		t.Errorf("disabled skill row:\n%s", v)
	}

	h.selectNode(srcKey("demo-local"))
	h.key(" ")
	if s := h.config().Sources["demo-local"]; !s.Disabled {
		t.Fatal("source not disabled")
	}
	if l := ansi.Strip(rawLine(h, "demo-local  local")); !strings.Contains(l, "disabled") {
		t.Errorf("disabled source row: %q", l)
	}
}

func TestEnablePluginServer(t *testing.T) {
	h := newHarness(t, TabSkills)
	demoSkills(h)
	h.load()
	h.selectNode(skillKey("demo-local", "demo-alpha"))
	h.key("E")
	if v := screen(h); len(h.m.modals) != 0 || !strings.Contains(v, "select a plugin server") {
		t.Errorf("E on a skill:\n%s", v)
	}
	h.selectNode(serverKey("demo-market", "writing", "notes"))
	h.key("E")
	if fl := h.topForm().get("name"); fl.value() != "writing-notes" {
		t.Fatalf("prefilled name %q", fl.value())
	}
	h.key("enter")
	s := h.config().Servers["writing-notes"]
	if s == nil || s.Origin == nil || s.Origin.Source != "demo-market" || s.Origin.Plugin != "writing" || s.Origin.Server != "notes" {
		t.Fatalf("enabled server: %+v", s)
	}
	if v := screen(h); !strings.Contains(v, "server notes — enabled as writing-notes") || !strings.Contains(v, "enabled notes as writing-notes") {
		t.Errorf("after enabling:\n%s", v)
	}
	h.key("E")
	if v := screen(h); len(h.m.modals) != 0 || !strings.Contains(v, "already enabled as writing-notes") {
		t.Errorf("E twice:\n%s", v)
	}

	h.selectNode(serverKey("demo-market", "writing", "docs"))
	h.key("E")
	h.set("name", "writing-notes")
	h.key("enter")
	if fl := h.topForm().get("name"); !strings.Contains(fl.err, "already exists") {
		t.Errorf("taken name: %q", fl.err)
	}
	h.set("name", "bad name!")
	h.key("enter")
	if fl := h.topForm().get("name"); !strings.Contains(fl.err, "letters, digits and '-'") {
		t.Errorf("bad name: %q", fl.err)
	}
	if h.config().Servers["bad name!"] != nil || len(h.config().Servers) != 1 {
		t.Fatal("server written for an invalid name")
	}
}

func TestRemoveSource(t *testing.T) {
	h := newHarness(t, TabSkills)
	demoSkills(h)
	h.load()
	h.selectNode(srcKey("demo-local"))
	h.key("d")
	if v := screen(h); !strings.Contains(v, "Remove source") {
		t.Fatalf("no confirmation:\n%s", v)
	}
	h.key("enter") // only y confirms
	if h.config().Sources["demo-local"] == nil {
		t.Fatal("enter removed the source")
	}
	h.key("d", "y")
	if h.config().Sources["demo-local"] != nil {
		t.Fatal("source not removed")
	}
	if v := screen(h); !strings.Contains(v, "removed source demo-local") || strings.Contains(v, "demo-alpha") {
		t.Errorf("after remove:\n%s", v)
	}
}

func TestPassiveBusyKeepsFlash(t *testing.T) {
	h := newHarness(t, TabSkills)
	h.load()
	h.m.beginPassive("demo:poll", "polling demo …")
	if v := screen(h); !strings.Contains(v, "polling demo") {
		t.Errorf("passive work not shown when idle:\n%s", v)
	}
	h.m.flash("demo flash", false)
	if v := screen(h); !strings.Contains(v, "demo flash") || strings.Contains(v, "polling demo") {
		t.Errorf("passive work hides the flash:\n%s", v)
	}
	h.m.begin("demo:work", "working demo …")
	if v := screen(h); !strings.Contains(v, "working demo") {
		t.Errorf("active work not shown over the flash:\n%s", v)
	}
}

// TestSkillsNoOverflow: long names, descriptions and paths, dialogs and the filter at every size.
func TestSkillsNoOverflow(t *testing.T) {
	h := newHarness(t, TabSkills)
	cfg := demoSkills(h)
	deep := filepath.Join(h.dir, strings.Repeat("deep/", 40))
	longSkill := "demo-" + strings.Repeat("x", 55)
	writeSkill(t, deep, longSkill, strings.Repeat("A long description. ", 15))
	longSrc := strings.Repeat("s", 32)
	cfg.Sources[longSrc] = &config.Source{Type: "local", Path: deep}
	h.writeConfig(cfg)
	h.load()
	for _, sz := range [][2]int{{40, 12}, {89, 24}, {90, 24}, {300, 50}} {
		h.size(sz[0], sz[1])
		for _, sel := range []string{srcKey(longSrc), skillKey(longSrc, longSkill), pluginKey("demo-market", "writing"), serverKey("demo-market", "writing", "notes")} {
			h.selectNode(sel)
			what := sel
			checkScreen(t, h.m.View(), sz[0], sz[1], "tree "+what)
			h.key("a")
			h.set("name", strings.Repeat("n", 60))
			h.set("path", deep)
			h.key("ctrl+s")
			checkScreen(t, h.m.View(), sz[0], sz[1], "source form "+what)
			h.key("esc", "n")
			checkScreen(t, h.m.View(), sz[0], sz[1], "prompt "+what)
			h.key("esc", "d")
			checkScreen(t, h.m.View(), sz[0], sz[1], "confirm "+what)
			if len(h.m.modals) > 0 {
				h.key("n")
			}
			h.key("/", strings.Repeat("q", 80))
			checkScreen(t, h.m.View(), sz[0], sz[1], "filter "+what)
			h.key("esc")
		}
	}
}

// TestDefaultUpdateSources runs the default Options.Update offline (no daemon socket): the catalog
// is saved after a finished and after a cancelled update, so catalog.json matches the checkouts.
func TestDefaultUpdateSources(t *testing.T) {
	h := newHarness(t, TabSkills)
	local := filepath.Join(h.dir, "local-skills")
	writeSkill(t, local, "demo-alpha", "Alpha demo skill.")
	h.writeConfig(&config.Config{Version: 1, Sources: map[string]*config.Source{"demo-local": {Type: "local", Path: local}}})
	o, catPath := h.m.opts, h.m.opts.Env.CatalogPath
	hasAlpha := func(what string) {
		t.Helper()
		c, err := catalog.Load(catPath)
		if err != nil {
			t.Fatalf("%s: catalog not written: %v", what, err)
		}
		if len(c.Skills) != 1 || c.Skills[0].ID != "demo-alpha" {
			t.Errorf("%s: catalog skills = %+v", what, c.Skills)
		}
	}

	if _, err := o.updateSources(context.Background(), nil, true); err != nil { // falls back to offline
		t.Fatalf("update: %v", err)
	}
	hasAlpha("finished")

	os.Remove(catPath)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := o.updateSources(ctx, nil, false); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled update err = %v, want context.Canceled", err)
	}
	hasAlpha("cancelled")
}

// TestUpdateAfterCancelWaitsForTheStoppingRun: a cancelled offline update still syncs, builds and
// saves until it notices; a new update must not start on the same checkouts meanwhile.
func TestUpdateAfterCancelWaitsForTheStoppingRun(t *testing.T) {
	h := newHarness(t, TabSkills)
	demoSkills(h)
	h.load()
	started := make(chan []string, 2)
	release := make(chan struct{})
	calls := 0
	h.m.opts.Update = func(ctx context.Context, names []string, viaDaemon bool) (daemon.UpdateReply, error) {
		calls++
		started <- names
		<-ctx.Done()
		<-release // still saving the catalog
		return daemon.UpdateReply{}, ctx.Err()
	}
	out := startBlocked(t, h, "U", started)
	h.key("esc")
	h.key("U")
	if v := screen(h); !strings.Contains(v, "previous update still stopping") {
		t.Errorf("second update not refused while the first stops:\n%s", v)
	}
	h.key("u")
	if calls != 1 {
		t.Fatalf("update started %d times while the cancelled one was still running", calls)
	}
	close(release)
	for _, msg := range waitMsgs(t, out) {
		_, cmd := h.m.Update(msg)
		h.run(cmd)
	}
	out = startBlocked(t, h, "U", started)
	if calls != 2 {
		t.Fatalf("update did not start after the cancelled one ended (calls %d)", calls)
	}
	h.m.Update(keyMsg("ctrl+c"))
	waitMsgs(t, out)
}
