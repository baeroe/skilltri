package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/daemon"
	"github.com/baeroe/skilltri/internal/rules"
)

func (h *harness) preview() *previewTab { return h.m.tabs[TabPreview].(*previewTab) }

// typeFolder replaces the folder input by typing, then fires the debounce message the way the
// (NoTick) model does not.
func (h *harness) typeFolder(dir string) {
	h.t.Helper()
	p := h.preview()
	h.key("/")
	p.input.SetValue("")
	h.key(dir)
	if !p.editing {
		h.t.Fatal("the folder input is not being edited")
	}
	h.key("esc")
	h.fireDebounce()
}

func (h *harness) fireDebounce() {
	_, cmd := h.m.Update(previewDebounceMsg{seq: h.preview().inSeq})
	h.run(cmd)
}

// previewFolders makes two folders and a config with different rules for each.
func previewSetup(h *harness) (dirA, dirB string, cfg *config.Config) {
	h.t.Helper()
	dirA, dirB = rules.Normalize(filepath.Join(h.dir, "work", "a")), rules.Normalize(filepath.Join(h.dir, "work", "b"))
	os.MkdirAll(dirA, 0o755)
	os.MkdirAll(dirB, 0o755)
	cfg = demoSkills(h)
	cfg.Servers = map[string]*config.Server{
		"demo-a": {Type: "stdio", Command: "demo", Rules: []rules.Rule{{Path: dirA, Mode: rules.Tree}}},
		"demo-b": {Type: "stdio", Command: "demo", Rules: []rules.Rule{{Path: dirB, Mode: rules.Tree}}},
	}
	cfg.Sources["demo-local"].Skills = map[string]*config.Node{"demo-beta": {Rules: []rules.Rule{{Path: dirA, Mode: rules.Only}}}}
	h.writeConfig(cfg)
	return
}

func TestPreviewOffline(t *testing.T) {
	h := newHarness(t, TabPreview)
	dirA, dirB, _ := previewSetup(h)
	h.size(140, 36)
	h.load()
	h.typeFolder(dirA)
	v := screen(h)
	for _, want := range []string{"Servers 1/2", "✓ demo-a", "✗ demo-b", "Skills", "✓ demo-alpha", "✓ demo-beta", "offline: no tool lists"} {
		if !strings.Contains(v, want) {
			t.Errorf("folder A misses %q:\n%s", want, v)
		}
	}
	if len(h.hellos) != 1 || h.hellos[0].Cmd != "status" {
		t.Errorf("offline preview must not ask the daemon for which: %+v", h.hellos)
	}
	h.typeFolder(dirB)
	v = screen(h)
	for _, want := range []string{"✗ demo-a", "✓ demo-b", "✓ demo-alpha", "✗ demo-beta"} {
		if !strings.Contains(v, want) {
			t.Errorf("folder B misses %q:\n%s", want, v)
		}
	}
	// the folder is shown with ~ when under home
	home := h.m.opts.Home
	os.MkdirAll(filepath.Join(home, "proj"), 0o755)
	h.typeFolder("~/proj")
	if v = screen(h); !strings.Contains(v, "~/proj") || strings.Contains(v, rules.Normalize(home)) {
		t.Errorf("folder not shown with ~:\n%s", v)
	}
	// a relative folder is refused inline
	h.typeFolder("proj")
	if v = screen(h); !strings.Contains(v, "enter an absolute folder") || strings.Contains(v, "demo-a") {
		t.Errorf("relative folder:\n%s", v)
	}
}

func TestPreviewDaemon(t *testing.T) {
	h := newHarness(t, TabPreview)
	dirA, _, _ := previewSetup(h)
	h.status = readyStatus(1, 2)
	h.which = &daemon.WhichReply{Dir: dirA, Servers: []daemon.WhichServer{
		{Name: "demo-a", State: "ready", Decision: rules.Decision{Active: true, Reason: "rule tree " + dirA}, Tools: []daemon.WhichTool{
			{Name: "search", Exposed: "demo-a_search", Decision: rules.Decision{Active: true}},
			{Name: "write", Exposed: "demo-a_write", Decision: rules.Decision{Reason: "tool: disabled"}},
		}},
		{Name: "demo-b", State: "stopped", Decision: rules.Decision{Reason: "no rule matches"}},
	}, Skills: []daemon.WhichSkill{{ID: "demo-alpha", Source: "demo-local", Decision: rules.Decision{Active: true}}}}
	h.size(140, 36)
	h.load()
	h.typeFolder(dirA)
	v := screen(h)
	for _, want := range []string{"Servers 1/2 · tools 1/2", "✓ demo-a", "ready", "  ✓ demo-a_search", "  ✗ demo-a_write", "tool: disabled", "✗ demo-b", "no rule matches", "from the daemon", "Skills 1/1"} {
		if !strings.Contains(v, want) {
			t.Errorf("daemon preview misses %q:\n%s", want, v)
		}
	}
	last := h.hellos[len(h.hellos)-1]
	if last.Cmd != "which" || len(last.Args) != 1 || last.Args[0] != dirA {
		t.Errorf("last control call = %+v", last)
	}
	// a daemon that does not answer which falls back to the files
	h.cmdErr = map[string]error{"which": os.ErrDeadlineExceeded}
	h.typeFolder(dirA)
	if v = screen(h); !strings.Contains(v, "offline: no tool lists") {
		t.Errorf("no fallback:\n%s", v)
	}
}

func TestPreviewOnlyActive(t *testing.T) {
	h := newHarness(t, TabPreview)
	dirA, _, _ := previewSetup(h)
	h.size(140, 36)
	h.load()
	h.typeFolder(dirA)
	h.key("f")
	v := screen(h)
	if strings.Contains(v, "✗") || strings.Contains(v, "demo-b ") {
		t.Errorf("inactive items shown:\n%s", v)
	}
	if !strings.Contains(v, "Servers 1/2") || !strings.Contains(v, "✓ demo-a") {
		t.Errorf("counts or active items missing:\n%s", v)
	}
	h.key("f")
	if v = screen(h); !strings.Contains(v, "✗ demo-b") {
		t.Errorf("filter not off:\n%s", v)
	}
}

func TestPreviewJump(t *testing.T) {
	h := newHarness(t, TabPreview)
	dirA, _, _ := previewSetup(h)
	h.size(140, 36)
	h.load()
	h.typeFolder(dirA)
	h.key("j") // demo-b
	h.key("enter")
	if h.m.tab != TabServers || h.serversTab().sel != "demo-b" {
		t.Fatalf("tab %v sel %q", h.m.tab, h.serversTab().sel)
	}
	h.key("3")
	for h.preview().selected() == nil || h.preview().selected().label != "demo-beta" {
		h.key("j")
		if h.preview().idx >= len(h.preview().rows)-1 {
			break
		}
	}
	h.key("enter")
	if h.m.tab != TabSkills || h.skills().sel != skillKey("demo-local", "demo-beta") {
		t.Fatalf("tab %v sel %q", h.m.tab, h.skills().sel)
	}
	// a plugin skill opens its collapsed parents
	h.key("3")
	h.skills().open = map[string]bool{srcKey("demo-market"): false}
	for h.preview().selected().kind != pvSkill || h.preview().selected().skill.Plugin == "" {
		h.key("j")
	}
	want := skillKey(h.preview().selected().skill.Source, h.preview().selected().skill.ID)
	h.key("enter")
	if h.skills().sel != want || h.skills().selected(h.m) == nil || h.skills().selected(h.m).node.key != want {
		t.Errorf("plugin skill not selected (%q != %q)", h.skills().sel, want)
	}
}

func TestPreviewDebounceAndStale(t *testing.T) {
	h := newHarness(t, TabPreview)
	dirA, dirB, _ := previewSetup(h)
	h.size(140, 36)
	h.load()
	h.typeFolder(dirA)
	p := h.preview()
	// typing alone does not query
	h.key("/")
	p.input.SetValue("")
	h.key(dirB)
	seq := p.seq
	if p.seq != seq || !strings.Contains(screen(h), "✓ demo-a") {
		t.Fatal("query before the debounce")
	}
	// an outdated debounce message does nothing, the current one queries
	_, cmd := h.m.Update(previewDebounceMsg{seq: p.inSeq - 1})
	h.run(cmd)
	if p.seq != seq {
		t.Error("stale debounce queried")
	}
	h.fireDebounce()
	if p.seq != seq+1 || !strings.Contains(screen(h), "✓ demo-b") {
		t.Errorf("debounce did not query:\n%s", screen(h))
	}
	// a stale reply is dropped
	old := daemon.WhichOffline(h.m.snap.Config, h.m.snap.Catalog, dirA)
	h.m.Update(previewMsg{seq: p.seq - 1, reply: &old, offline: true})
	if p.reply.Dir != dirB {
		t.Errorf("stale reply applied: %s", p.reply.Dir)
	}
	// enter queries at once
	p.input.SetValue(dirA)
	h.key("enter")
	if p.reply.Dir != dirA {
		t.Errorf("enter did not query: %s", p.reply.Dir)
	}
}

func TestPreviewCopy(t *testing.T) {
	h := newHarness(t, TabPreview)
	dirA, _, _ := previewSetup(h)
	var clip string
	h.m.opts.Clipboard = func(s string) error { clip = s; return nil }
	h.load()
	h.typeFolder(dirA)
	h.key("c")
	if want := `eval "$(skilltri env ` + dirA + ` --shell zsh)"`; clip != want {
		t.Errorf("clipboard = %q, want %q", clip, want)
	}
	if !strings.Contains(screen(h), "copied") {
		t.Errorf("no status:\n%s", screen(h))
	}
	if q := shellQuote("/a b/it's"); q != `'/a b/it'\''s'` {
		t.Errorf("quote = %s", q)
	}
}

func TestPreviewOverflow(t *testing.T) {
	h := newHarness(t, TabPreview)
	dirA, _, cfg := previewSetup(h)
	long := strings.Repeat("n", 60)
	cfg.Servers[long] = &config.Server{Type: "stdio", Command: "demo", Rules: []rules.Rule{{Path: "/" + strings.Repeat("deep/", 40), Mode: rules.Tree}}}
	h.writeConfig(cfg)
	h.status = readyStatus(1, 2)
	var tools []daemon.WhichTool
	for i := 0; i < 30; i++ {
		tools = append(tools, daemon.WhichTool{Name: "t", Exposed: long + "_" + strings.Repeat("tool", 20), Decision: rules.Decision{Reason: strings.Repeat("reason ", 30)}})
	}
	h.which = &daemon.WhichReply{Dir: dirA, Servers: []daemon.WhichServer{{Name: long, State: "ready", Decision: rules.Decision{Active: true, Reason: strings.Repeat("r", 200)}, Tools: tools}}}
	h.load()
	h.typeFolder("/" + strings.Repeat("folder/", 30))
	for _, sz := range [][2]int{{40, 12}, {89, 20}, {90, 20}, {300, 50}, {60, 30}} {
		h.size(sz[0], sz[1])
		for _, keys := range [][]string{nil, {"j", "j"}, {"/"}, {"esc"}} {
			h.key(keys...)
			v := ansi.Strip(h.m.View())
			lines := strings.Split(v, "\n")
			if len(lines) != sz[1] {
				t.Errorf("%dx%d: %d lines", sz[0], sz[1], len(lines))
			}
			for _, l := range lines {
				if width(l) > sz[0] {
					t.Errorf("%dx%d: line of %d cells: %q", sz[0], sz[1], width(l), l)
				}
			}
		}
	}
}
