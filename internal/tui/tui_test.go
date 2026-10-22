package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/baeroe/skilltri/internal/actions"
	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/daemon"
	"github.com/baeroe/skilltri/internal/downstream"
	"github.com/baeroe/skilltri/internal/skills"
	"github.com/baeroe/skilltri/internal/sources"
)

// harness drives a Model in a sandbox: fake HOME and skilltri dirs, a fake daemon (Control) and a
// clock the test advances.
type harness struct {
	t      *testing.T
	m      *Model
	dir    string
	now    time.Time
	status *daemon.StatusReply // nil: the fake daemon is not running
	ctlErr error               // returned when status is nil (default: connection refused)
	calls  int
	which  *daemon.WhichReply // the running fake daemon's which reply
	hellos []daemon.Hello     // every control request
	cmdErr map[string]error   // per control command error of the running fake daemon
	svc    *svcFake           // the fake login service
}

// svcFake is an in-memory daemon service (Options.Service).
type svcFake struct {
	installed  bool
	installs   []string // exe of every Install
	uninstalls int
	err        error // returned by Install and Uninstall
}

func (f *svcFake) service() Service {
	return Service{
		File:      func() (string, error) { return "/home/demo/Library/LaunchAgents/io.github.baeroe.skilltri.plist", nil },
		Installed: func() bool { return f.installed },
		Install: func(exe string) error {
			f.installs = append(f.installs, exe)
			if f.err != nil {
				return f.err
			}
			f.installed = true
			return nil
		},
		Uninstall: func() error {
			f.uninstalls++
			if f.err != nil {
				return f.err
			}
			f.installed = false
			return nil
		},
	}
}

func newHarness(t *testing.T, tab Tab) *harness {
	t.Helper()
	dir := t.TempDir()
	for _, v := range []string{"HOME", "SKILLTRI_CONFIG_DIR", "SKILLTRI_STATE_DIR", "SKILLTRI_DATA_DIR", "SKILLTRI_CACHE_DIR"} {
		t.Setenv(v, filepath.Join(dir, strings.ToLower(v)))
	}
	h := &harness{t: t, dir: dir, now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC), svc: &svcFake{}}
	env := actions.Env{
		ConfigPath:  filepath.Join(dir, "config", "config.json"),
		CatalogPath: filepath.Join(dir, "cache", "catalog.json"),
		Socket:      filepath.Join(dir, "daemon.sock"),
		Builder:     &catalog.Builder{Fetcher: &sources.Fetcher{Root: filepath.Join(dir, "data", "sources")}, Lock: filepath.Join(dir, "state", "skills.lock")},
	}
	h.m = New(Options{
		Tab: tab, Env: env, Home: filepath.Join(dir, "home"), Bin: "/opt/demo/bin/skilltri", Cwd: "/work/demo",
		NoTick:  true,
		Now:     func() time.Time { return h.now },
		Environ: []string{"SHELL=/bin/zsh"},
		Service: h.svc.service(), // never launchctl or systemctl
		Control: func(hello daemon.Hello, reply any) error {
			h.calls++
			h.hellos = append(h.hellos, hello)
			if h.status == nil {
				if h.ctlErr != nil {
					return h.ctlErr
				}
				return &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}
			}
			if err := h.cmdErr[hello.Cmd]; err != nil {
				return err
			}
			switch r := reply.(type) {
			case *daemon.StatusReply:
				*r = *h.status
			case *daemon.WhichReply:
				if h.which != nil {
					*r = *h.which
				}
			}
			return nil
		},
	})
	h.size(120, 30)
	return h
}

func (h *harness) size(w, ht int) { h.m.Update(tea.WindowSizeMsg{Width: w, Height: ht}) }

// writeConfig writes cfg to the sandbox config file.
func (h *harness) writeConfig(cfg *config.Config) {
	h.t.Helper()
	b, err := json.Marshal(cfg)
	if err != nil {
		h.t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(h.m.opts.Env.ConfigPath), 0o700)
	if err := os.WriteFile(h.m.opts.Env.ConfigPath, b, 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) writeCatalog(c *catalog.Catalog) {
	h.t.Helper()
	if err := catalog.Save(h.m.opts.Env.CatalogPath, c); err != nil {
		h.t.Fatal(err)
	}
}

// load runs Init (the first snapshot) synchronously.
func (h *harness) load() { h.run(h.m.Init()) }

func (h *harness) key(keys ...string) {
	for _, k := range keys {
		_, cmd := h.m.Update(keyMsg(k))
		h.run(cmd)
	}
}

// keyMsg is the key message of a key name ("esc", "ctrl+s", " ") or of typed text.
func keyMsg(k string) tea.KeyMsg {
	switch k {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "ctrl+s":
		return tea.KeyMsg{Type: tea.KeyCtrlS}
	case "ctrl+k":
		return tea.KeyMsg{Type: tea.KeyCtrlK}
	case "ctrl+u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

// run executes cmd and feeds the resulting messages back (batches included). Ticks are not
// followed; quit is ignored.
func (h *harness) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	switch msg := msg.(type) {
	case nil, tea.QuitMsg, tickMsg:
		return
	case tea.BatchMsg:
		for _, c := range msg {
			h.run(c)
		}
		return
	}
	_, next := h.m.Update(msg)
	h.run(next)
}

// tick advances the clock by d and delivers a tick (with its status refresh).
func (h *harness) tick(d time.Duration) {
	h.now = h.now.Add(d)
	_, cmd := h.m.Update(tickMsg(h.now))
	h.run(cmd)
}

func demoConfig(n int) *config.Config {
	cfg := &config.Config{Version: 1, Servers: map[string]*config.Server{}, Sources: map[string]*config.Source{}}
	for i := 0; i < n; i++ {
		cfg.Servers[fmt.Sprintf("demo-%d", i+1)] = &config.Server{Type: "stdio", Command: "demo-server"}
	}
	return cfg
}

func readyStatus(ready, total int) *daemon.StatusReply {
	r := &daemon.StatusReply{PID: 4242, Version: "0.1.0"}
	for i := 0; i < total; i++ {
		st := "stopped"
		if i < ready {
			st = "ready"
		}
		r.Servers = append(r.Servers, downstream.ServerStatus{Name: fmt.Sprintf("demo-%d", i+1), State: st})
	}
	return r
}

func header(h *harness) string { return ansi.Strip(strings.SplitN(h.m.View(), "\n", 2)[0]) }

func TestTabsSwitch(t *testing.T) {
	h := newHarness(t, TabServers)
	h.load()
	if !strings.Contains(header(h), "Servers") {
		t.Fatalf("header: %q", header(h))
	}
	h.key("tab")
	if h.m.tab != TabSkills {
		t.Fatalf("tab → %v", h.m.tab)
	}
	h.key("2")
	if h.m.tab != TabSkills {
		t.Fatalf("2 → %v", h.m.tab)
	}
	h.key("5")
	if h.m.tab != TabDoctor {
		t.Fatalf("5 → %v", h.m.tab)
	}
	h.key("tab")
	if h.m.tab != TabServers {
		t.Fatalf("tab wraps → %v", h.m.tab)
	}
	h.key("shift+tab")
	if h.m.tab != TabDoctor {
		t.Fatalf("shift+tab wraps → %v", h.m.tab)
	}
	for i, name := range tabNames {
		h.key(fmt.Sprint(i + 1))
		if !strings.Contains(ansi.Strip(h.m.View()), name) {
			t.Errorf("tab %s not rendered", name)
		}
	}
	if tb, ok := ParseTab("preview"); !ok || tb != TabPreview {
		t.Fatalf("ParseTab: %v %v", tb, ok)
	}
	if _, ok := ParseTab("nope"); ok {
		t.Fatal("ParseTab accepted an unknown tab")
	}
}

func TestHeaderChips(t *testing.T) {
	h := newHarness(t, TabServers)
	h.writeConfig(demoConfig(9))
	h.writeCatalog(&catalog.Catalog{Skills: []catalog.Entry{{ID: "demo-a", Source: "demo"}, {ID: "demo-b", Source: "demo"}}})
	h.load()
	hd := header(h)
	if !strings.Contains(hd, "○ stopped") || strings.Contains(hd, "● running") {
		t.Fatalf("stopped daemon: %q", hd)
	}
	if !strings.Contains(hd, "skills 2") {
		t.Fatalf("skills chip: %q", hd)
	}
	h.status = readyStatus(7, 9)
	h.tick(2 * time.Second)
	hd = header(h)
	if !strings.Contains(hd, "● running") || !strings.Contains(hd, "7/9") {
		t.Fatalf("running daemon: %q", hd)
	}
	if strings.Contains(hd, "!") {
		t.Fatalf("no warnings expected: %q", hd)
	}
	// warnings: a config error from the daemon, a failing source, a server needing review
	h.status.ConfigError = "parse config.json: unexpected end"
	cfg := demoConfig(9)
	cfg.Sources["demo-src"] = &config.Source{Type: "marketplace", URL: "demo/market"}
	cfg.Servers["demo-plugin"] = &config.Server{Type: "stdio", Command: "demo", Origin: &config.Origin{Source: "demo-src", Plugin: "p", Server: "s", DefHash: "old"}}
	h.writeConfig(cfg)
	h.writeCatalog(&catalog.Catalog{
		Sources: []catalog.SourceState{{Name: "demo-src", Commit: "abc", Error: "fetch failed"}},
		Servers: []catalog.PluginServer{{Source: "demo-src", Plugin: "p", Name: "s", DefHash: "new"}},
	})
	h.key("R")
	if hd = header(h); !strings.Contains(hd, "! 3") {
		t.Fatalf("warnings chip: %q", hd)
	}
}

func TestReloadOnFileChange(t *testing.T) {
	h := newHarness(t, TabServers)
	h.writeConfig(demoConfig(2))
	h.load()
	if !strings.Contains(header(h), "servers 2") {
		t.Fatalf("offline server chip: %q", header(h))
	}
	h.writeConfig(demoConfig(5))
	future := h.now.Add(time.Minute)
	os.Chtimes(h.m.opts.Env.ConfigPath, future, future)
	h.tick(2 * time.Second)
	if !strings.Contains(header(h), "servers 5") {
		t.Fatalf("config change not picked up: %q", header(h))
	}
}

func TestFlashClearsAfterFiveSeconds(t *testing.T) {
	h := newHarness(t, TabServers)
	h.load()
	h.m.flash("demo saved", false)
	if !strings.Contains(ansi.Strip(h.m.View()), "demo saved") {
		t.Fatal("status not shown")
	}
	h.tick(4 * time.Second)
	if !strings.Contains(ansi.Strip(h.m.View()), "demo saved") {
		t.Fatal("status cleared too early")
	}
	h.tick(2 * time.Second)
	if strings.Contains(ansi.Strip(h.m.View()), "demo saved") {
		t.Fatal("status not cleared after 5 s")
	}
}

func TestHelpOverlay(t *testing.T) {
	h := newHarness(t, TabServers)
	h.load()
	h.key("?")
	if len(h.m.modals) != 1 || !strings.Contains(ansi.Strip(h.m.View()), "Keys") {
		t.Fatal("help not open")
	}
	_, cmd := h.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if isQuit(cmd) {
		t.Fatal("q quit while the help overlay is open")
	}
	h.key("?")
	h.key("2") // keys go to the modal, not to the tabs
	if h.m.tab != TabServers {
		t.Fatal("tab switched under the help overlay")
	}
	h.key("esc")
	if len(h.m.modals) != 0 {
		t.Fatal("esc did not close the help")
	}
	h.key("?", "?")
	if len(h.m.modals) != 0 {
		t.Fatal("? did not close the help")
	}
	_, cmd = h.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if !isQuit(cmd) {
		t.Fatal("q should quit without a modal")
	}
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// TestNoOverflow renders every tab, with and without the help overlay, at small, borderline and huge
// sizes with long names and paths: no line may be wider than the terminal or the screen taller.
func TestNoOverflow(t *testing.T) {
	h := newHarness(t, TabServers)
	long := strings.Repeat("n", 60)
	path := "/" + strings.Repeat("deep/", 39) + "end"
	cfg := demoConfig(3)
	cfg.Servers[long] = &config.Server{Type: "stdio", Command: path, Cwd: path}
	cfg.Sources[long] = &config.Source{Type: "local", Path: path}
	h.writeConfig(cfg)
	h.writeCatalog(&catalog.Catalog{
		Skills:  []catalog.Entry{{ID: long, Source: long, Skill: skills.Skill{Name: long, Dir: path}}},
		Sources: []catalog.SourceState{{Name: long, Error: "failed to read " + path}},
	})
	h.m.opts.Bin, h.m.opts.Cwd = path, path
	h.status = readyStatus(2, 4)
	h.status.Servers[0].Name = long
	h.load()
	h.m.flash("could not save "+path, true)
	for _, sz := range [][2]int{{40, 12}, {89, 24}, {90, 24}, {300, 50}} {
		h.size(sz[0], sz[1])
		for tb := Tab(0); tb < numTabs; tb++ {
			h.key(fmt.Sprint(int(tb) + 1))
			checkScreen(t, h.m.View(), sz[0], sz[1], tabNames[tb])
			h.key("?")
			checkScreen(t, h.m.View(), sz[0], sz[1], tabNames[tb]+" help")
			h.key("esc")
		}
	}
}

func checkScreen(t *testing.T, v string, w, ht int, what string) {
	t.Helper()
	lines := strings.Split(v, "\n")
	if len(lines) > ht {
		t.Fatalf("%s at %dx%d: %d lines", what, w, ht, len(lines))
	}
	for i, l := range lines {
		if lw := ansi.StringWidth(l); lw > w {
			t.Fatalf("%s at %dx%d: line %d is %d wide: %q", what, w, ht, i, lw, ansi.Strip(l))
		}
	}
}

func TestDaemonGoesAway(t *testing.T) {
	h := newHarness(t, TabServers)
	h.writeConfig(demoConfig(3))
	h.status = readyStatus(3, 3)
	h.load()
	if !strings.Contains(header(h), "● running") {
		t.Fatalf("header: %q", header(h))
	}
	h.status, h.ctlErr = nil, &os.SyscallError{Syscall: "connect", Err: syscall.ENOENT}
	h.tick(2 * time.Second)
	if hd := header(h); !strings.Contains(hd, "○ stopped") || !strings.Contains(hd, "servers 3") {
		t.Fatalf("daemon gone: %q", hd)
	}
	if h.m.snap.Config == nil || len(h.m.snap.Config.Servers) != 3 {
		t.Fatal("offline data lost")
	}
	// a hung daemon is not "stopped"
	h.ctlErr = fmt.Errorf("read: %w", os.ErrDeadlineExceeded)
	h.tick(2 * time.Second)
	if hd := header(h); !strings.Contains(hd, "○ no answer") {
		t.Fatalf("hung daemon: %q", hd)
	}
	// a stale status result (sent before the error) must not flip it back
	_, _ = h.m.Update(statusMsg{seq: 1, status: readyStatus(3, 3)})
	if strings.Contains(header(h), "● running") {
		t.Fatal("stale status applied")
	}
	h.status = readyStatus(1, 3)
	h.tick(2 * time.Second)
	if hd := header(h); !strings.Contains(hd, "● running") || !strings.Contains(hd, "1/3") {
		t.Fatalf("daemon back: %q", hd)
	}
}

// TestQuitWaitsForNothing: a snapshot load blocked on the daemon must not keep the program from quitting.
func TestQuitWaitsForNothing(t *testing.T) {
	h := newHarness(t, TabServers)
	block := make(chan struct{})
	defer close(block)
	h.m.opts.Control = func(daemon.Hello, any) error { <-block; return nil }
	p := tea.NewProgram(h.m, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutSignalHandler(), tea.WithoutRenderer())
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	time.Sleep(50 * time.Millisecond) // Init's load is now blocked in Control
	p.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		p.Kill()
		t.Fatal("quit blocked on a pending command")
	}
}

func TestLayoutHelpers(t *testing.T) {
	if got := fit("abcdef", 4); got != "abc…" {
		t.Errorf("fit: %q", got)
	}
	if got := fit("ab", 4); got != "ab  " {
		t.Errorf("fit pad: %q", got)
	}
	if got := padLeft("ab", 4); got != "  ab" {
		t.Errorf("padLeft: %q", got)
	}
	if got := ellipsizeMiddle("/very/long/path/to/file", 10); width(got) != 10 || !strings.Contains(got, "…") || !strings.HasSuffix(got, "file") {
		t.Errorf("ellipsizeMiddle: %q", got)
	}
	if got := ellipsizeMiddle(strings.Repeat("日本語", 10), 11); width(got) > 11 || !strings.Contains(got, "…") || width(got) < 10 {
		t.Errorf("ellipsizeMiddle CJK: %q (%d cells)", got, width(got))
	}
	if got := tildify("/demo/home/x", "/demo/home"); got != "~/x" {
		t.Errorf("tildify: %q", got)
	}
	if got := tildify("/demo/home", "/demo/home"); got != "~" {
		t.Errorf("tildify home: %q", got)
	}
	if got := tildify("/demo/homes/x", "/demo/home"); got != "/demo/homes/x" {
		t.Errorf("tildify prefix: %q", got)
	}
	if got := san("a\x1b[31mb\tc\x07"); got != "ab    c?" {
		t.Errorf("san: %q", got)
	}
	if got := oneLine(" a \n b "); got != "a b" {
		t.Errorf("oneLine: %q", got)
	}
	for _, l := range wrap("one two three "+strings.Repeat("x", 25), 10) {
		if width(l) > 10 {
			t.Errorf("wrap: %q", l)
		}
	}
	p := panel("Title", []string{strings.Repeat("y", 50)}, 20, 4, true, "")
	for _, l := range strings.Split(p, "\n") {
		if width(l) != 20 {
			t.Errorf("panel line %d wide: %q", width(l), l)
		}
	}
	if n := len(strings.Split(p, "\n")); n != 4 {
		t.Errorf("panel height %d", n)
	}
	o := overlay("bg\nbg\nbg", modalBox("M", []string{"x"}, 10, cAccent), 30, 3)
	for _, l := range strings.Split(o, "\n") {
		if width(l) != 30 {
			t.Errorf("overlay line %d wide", width(l))
		}
	}
	if got := ansi.Strip(keyHints("a", "add", "d", "delete")); got != "a add  d delete" {
		t.Errorf("keyHints: %q", got)
	}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for d, want := range map[time.Duration]string{5 * time.Second: "now", 30 * time.Second: "30s ago", 5 * time.Minute: "5m ago", 3 * time.Hour: "3h ago", 72 * time.Hour: "3d ago"} {
		if got := ago(now.Add(-d), now); got != want {
			t.Errorf("ago(%v) = %q, want %q", d, got, want)
		}
	}
	if ago(time.Time{}, now) != "never" {
		t.Error("ago zero")
	}
}

// TestReloadAfterInterleavedStatus: a status refresh issued while a load runs must not leave the
// model thinking the load is still running (which would disable file-change reloads).
func TestReloadAfterInterleavedStatus(t *testing.T) {
	h := newHarness(t, TabServers)
	h.writeConfig(demoConfig(2))
	load := h.m.Init()
	h.now = h.now.Add(2 * time.Second)
	_, status := h.m.Update(tickMsg(h.now))
	h.m.Update(status())
	h.m.Update(load())
	if !strings.Contains(header(h), "servers 2") {
		t.Fatalf("snapshot not applied: %q", header(h))
	}
	h.writeConfig(demoConfig(5))
	future := h.now.Add(time.Minute)
	os.Chtimes(h.m.opts.Env.ConfigPath, future, future)
	h.tick(2 * time.Second)
	if !strings.Contains(header(h), "servers 5") {
		t.Fatalf("file change after an interleaved status not reloaded: %q", header(h))
	}
}

type fakeModal struct{ closedN int }

func (f *fakeModal) update(*Model, tea.Msg) (tea.Cmd, bool) { return nil, false }
func (f *fakeModal) view(*Model, int, int) string           { return "fake" }
func (f *fakeModal) closed(*Model)                          { f.closedN++ }

func TestModalClosedHook(t *testing.T) {
	h := newHarness(t, TabServers)
	h.load()
	a, b := &fakeModal{}, &fakeModal{}
	h.m.openModal(a)
	h.m.openModal(b)
	h.key("esc")
	if b.closedN != 1 || a.closedN != 0 || len(h.m.modals) != 1 {
		t.Fatalf("esc: a=%d b=%d open=%d", a.closedN, b.closedN, len(h.m.modals))
	}
	h.key("ctrl+c")
	if a.closedN != 1 || b.closedN != 1 {
		t.Fatalf("quit: a=%d b=%d", a.closedN, b.closedN)
	}
}

func TestActionsEnvUsesTUIFolderAndShortTimeout(t *testing.T) {
	h := newHarness(t, TabServers)
	if e := h.m.opts.Env; e.Dir != "/work/demo" || e.ControlTimeout != controlTimeout {
		t.Fatalf("actions env Dir=%q ControlTimeout=%v, want /work/demo and %v", e.Dir, e.ControlTimeout, controlTimeout)
	}
}
