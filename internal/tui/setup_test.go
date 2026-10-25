package tui

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/zalando/go-keyring"

	"github.com/baeroe/skilltri/internal/config"
)

// writeHome writes content to rel below the fake home.
func (h *harness) writeHome(rel, content string) {
	h.t.Helper()
	p := filepath.Join(h.m.opts.Home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

// copyFixture copies the testdata tree src into the fake home at rel, replacing @HOME@.
func (h *harness) copyFixture(src, rel string) {
	h.t.Helper()
	home := h.m.opts.Home
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		r, _ := filepath.Rel(src, p)
		dst := filepath.Join(home, rel, r)
		if info.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, bytes.ReplaceAll(b, []byte("@HOME@"), []byte(home)), info.Mode().Perm())
	})
	if err != nil {
		h.t.Fatal(err)
	}
}

// selectSetup moves the Setup selection with j/k to the first row for which ok holds.
func (h *harness) selectSetup(ok func(suRow) bool) {
	h.t.Helper()
	st := h.m.tabs[TabSetup].(*setupTab)
	for range st.rows {
		h.key("k")
	}
	for range st.rows {
		if ok(st.rows[st.idx]) {
			return
		}
		h.key("j")
	}
	h.t.Fatal("no such Setup row")
}

func rowKind(k suKind) func(suRow) bool { return func(r suRow) bool { return r.kind == k } }

// serviceRow is the list line of the daemon service.
func serviceRow(h *harness) string { return lineWith(h, "✓ service") + lineWith(h, "✗ service") }

// lineWith is the first list pane line containing s ("" when none).
func lineWith(h *harness, s string) string {
	for _, l := range strings.Split(screen(h), "\n") {
		if l = ansi.Truncate(l, listPaneWidth(h.m.width), ""); strings.Contains(l, s) {
			return l
		}
	}
	return ""
}

func TestSetupRendering(t *testing.T) {
	h := newHarness(t, TabSetup)
	h.writeHome(".claude.json", `{"mcpServers": {"skilltri": {"command": "/opt/demo/bin/skilltri", "args": ["mcp"]}}}`)
	h.writeHome(".codex/config.toml", "[mcp_servers.one]\nurl = \"http://127.0.0.1:3050/mcp\"\n")
	h.m.opts.Environ = []string{"SKILLTRI_HOOK=zsh"}
	h.svc.installed = true
	h.status = readyStatus(1, 1)
	h.writeConfig(demoConfig(1))
	h.size(160, 60)
	h.load()

	for _, c := range []struct{ has, want, not string }{
		{"✓ Claude Code", "✓", "✗"},
		{"Codex CLI ", "✗", "✓"},
		{"Gemini CLI ", "✗", "1MCP"},
		{"zsh", "●", "○"},
		{"bash", "○", "●"},
		{"✓ service", "● running", "not installed"},
		{"Codex  ", "detected · .agents/skills in git repos", "not detected"},
		{"Amp  ", "not detected · .agents/skills in git repos", ""},
		{"from 1MCP", "~/.config/1mcp/mcp.json", ""},
	} {
		l := lineWith(h, c.has)
		if !strings.Contains(l, c.want) || (c.not != "" && strings.Contains(l[:min(len(l), 80)], c.not)) {
			t.Errorf("row %q = %q, want %q without %q", c.has, l, c.want, c.not)
		}
	}
	if l := lineWith(h, "Codex CLI "); !strings.Contains(l, "! 1MCP") {
		t.Errorf("codex 1MCP marker missing: %q", l)
	}
	// the selected harness row shows its snippet in the details
	if !strings.Contains(screen(h), "claude mcp add --scope user skilltri -- /opt/demo/bin/skilltri mcp") {
		t.Errorf("snippet missing:\n%s", screen(h))
	}
	h.selectSetup(func(r suRow) bool { return r.kind == suHarness && r.snip.Name == "Codex CLI (config)" })
	for _, want := range []string{"[mcp_servers.skilltri]", `command = "/opt/demo/bin/skilltri"`, "still registered", "~/.codex/config.toml"} {
		if !strings.Contains(screen(h), want) {
			t.Errorf("codex details lack %q:\n%s", want, screen(h))
		}
	}
	h.selectSetup(rowKind(suShell))
	if !strings.Contains(screen(h), "SKILLTRI_HOOK=zsh") || !strings.Contains(screen(h), "~/.zshrc") {
		t.Errorf("shell details:\n%s", screen(h))
	}
	h.selectSetup(rowKind(suService))
	for _, want := range []string{"installed", "pid 4242", "LaunchAgents"} {
		if !strings.Contains(screen(h), want) {
			t.Errorf("service details lack %q:\n%s", want, screen(h))
		}
	}
	if !strings.Contains(ansi.Strip(h.m.footer(160)), "i install") {
		t.Errorf("service hints: %q", ansi.Strip(h.m.footer(160)))
	}
	// adapters: detection follows the home folders
	h.writeHome(".config/amp/x", "")
	h.key("R")
	if l := lineWith(h, "Amp  "); !strings.Contains(l, "detected · .agents") || strings.Contains(l, "not detected") {
		t.Errorf("amp adapter row: %q", l)
	}
	if l := lineWith(h, "plugin dirs"); !strings.Contains(l, "✓ Claude Code") {
		t.Errorf("claude adapter row: %q", l)
	}
}

func TestSetupCopy(t *testing.T) {
	h := newHarness(t, TabSetup)
	var clip string
	h.m.opts.Clipboard = func(s string) error { clip = s; return nil }
	h.load()
	h.key("c")
	if clip != "claude mcp add --scope user skilltri -- /opt/demo/bin/skilltri mcp" || !strings.Contains(screen(h), "copied the Claude Code snippet") {
		t.Errorf("clip = %q\n%s", clip, screen(h))
	}
	h.selectSetup(func(r suRow) bool { return r.kind == suShell && r.shell == "fish" })
	h.key("c")
	if clip != "/opt/demo/bin/skilltri shell-init fish | source" { // not on PATH: the absolute binary
		t.Errorf("fish clip = %q", clip)
	}
	h.selectSetup(rowKind(suShell))
	h.key("c")
	if clip != `eval "$(/opt/demo/bin/skilltri shell-init zsh)"` {
		t.Errorf("zsh clip = %q", clip)
	}
	h.m.opts.Clipboard = func(string) error { return errors.New("no clipboard tool") }
	h.key("c")
	if !strings.Contains(screen(h), "no clipboard tool") {
		t.Errorf("copy error not shown:\n%s", screen(h))
	}
}

func TestSetupService(t *testing.T) {
	h := newHarness(t, TabSetup)
	h.load()
	h.selectSetup(rowKind(suService))
	if !strings.Contains(serviceRow(h), "not installed") {
		t.Fatalf("service row: %q", serviceRow(h))
	}
	h.key("x")
	if len(h.m.modals) != 0 || h.svc.uninstalls != 0 || !strings.Contains(screen(h), "not installed") {
		t.Fatalf("uninstall of a missing service:\n%s", screen(h))
	}
	h.key("i")
	if !slices.Equal(h.svc.installs, []string{"/opt/demo/bin/skilltri"}) || !strings.Contains(screen(h), "installed the daemon service") {
		t.Fatalf("install: %v\n%s", h.svc.installs, screen(h))
	}
	if l := serviceRow(h); strings.Contains(l, "not installed") || !strings.Contains(l, "✓") {
		t.Errorf("not rescanned after install: %q", l)
	}
	// uninstall asks; any other key cancels
	h.key("x")
	if len(h.m.modals) != 1 || !strings.Contains(screen(h), "Uninstall the daemon service") {
		t.Fatalf("no confirmation:\n%s", screen(h))
	}
	h.key("n")
	if h.svc.uninstalls != 0 || len(h.m.modals) != 0 {
		t.Fatal("cancel uninstalled")
	}
	h.key("x", "y")
	if h.svc.uninstalls != 1 || !strings.Contains(screen(h), "uninstalled the daemon service") || !strings.Contains(serviceRow(h), "not installed") {
		t.Fatalf("uninstall: %d\n%s", h.svc.uninstalls, screen(h))
	}
	h.svc.err = errors.New("launchctl bootstrap: demo failure")
	h.key("i")
	if !strings.Contains(screen(h), "demo failure") {
		t.Errorf("install error not shown:\n%s", screen(h))
	}
	// stop: not running, then running
	h.key("s")
	if !strings.Contains(screen(h), "the daemon is not running") {
		t.Errorf("stop without daemon:\n%s", screen(h))
	}
	h.status = readyStatus(0, 0)
	h.key("R")
	h.hellos = nil
	h.key("s")
	if len(h.hellos) == 0 || h.hellos[0].Cmd != "shutdown" || !strings.Contains(screen(h), "stopped the daemon") {
		t.Errorf("stop: %v\n%s", h.hellos, screen(h))
	}
	// keys of the service row do nothing elsewhere
	h.selectSetup(rowKind(suHarness))
	n := len(h.svc.installs)
	h.key("i")
	if len(h.svc.installs) != n {
		t.Error("i installed from a harness row")
	}
}

func TestSetupAdapterToggle(t *testing.T) {
	h := newHarness(t, TabSetup)
	h.writeConfig(demoConfig(1))
	h.load()
	h.selectSetup(func(r suRow) bool { return r.kind == suAdapter && r.adapter.ID == "codex" })
	h.key(" ")
	if got := h.config().EnabledAdapters(); !slices.Equal(got, []string{"claude-code", "codex"}) {
		t.Fatalf("adapters = %v", got)
	}
	if l := lineWith(h, "Codex  "); !strings.Contains(l, "✓") || !strings.Contains(screen(h), "enabled the Codex adapter") {
		t.Errorf("row after enabling: %q\n%s", l, screen(h))
	}
	h.key(" ")
	if got := h.config().EnabledAdapters(); !slices.Equal(got, []string{"claude-code"}) {
		t.Fatalf("adapters = %v", got)
	}
	h.selectSetup(func(r suRow) bool { return r.kind == suAdapter && r.adapter.ID == "claude-code" })
	h.key(" ")
	if !strings.Contains(screen(h), "at least one adapter must stay enabled") {
		t.Errorf("last adapter disabled:\n%s", screen(h))
	}
}

func TestSetupImport1MCP(t *testing.T) {
	keyring.MockInit()
	h := newHarness(t, TabSetup)
	b, err := os.ReadFile("../importer/testdata/1mcp.json")
	if err != nil {
		t.Fatal(err)
	}
	h.writeHome(".config/1mcp/mcp.json", string(b))
	h.writeHome(".claude.json", `{"mcpServers": {"1mcp": {"type": "http", "url": "http://127.0.0.1:3050/mcp"}}}`)
	cfg := demoConfig(0)
	cfg.Servers["docs"] = &config.Server{Type: "stdio", Command: "demo-docs"}
	h.writeConfig(cfg)
	h.size(120, 50)
	h.load()
	h.selectSetup(func(r suRow) bool { return r.kind == suImport && r.imp == importOneMCP })
	h.key("enter")
	if len(h.m.modals) != 1 {
		t.Fatalf("no preview:\n%s", screen(h))
	}
	v := screen(h)
	for _, want := range []string{"Import from 1MCP", "+ browser: stdio demo-browser-mcp --headless", "- docs: exists, skipped (f overwrites)",
		`skipped "legacy": type sse is not supported`, "secret(s) go to the keychain", "remove the 1MCP entry from Claude Code", "y import"} {
		if !strings.Contains(v, want) {
			t.Errorf("preview lacks %q:\n%s", want, v)
		}
	}
	if c := h.config(); len(c.Servers) != 1 {
		t.Fatal("preview wrote the config")
	}
	h.key("f")
	if !strings.Contains(screen(h), "! docs: overwrites the existing one") {
		t.Errorf("force preview:\n%s", screen(h))
	}
	h.key("f", "y")
	c := h.config()
	if c.Servers["docs"].Command != "demo-docs" || c.Servers["browser"] == nil || c.Servers["notes"] == nil || c.Servers["legacy"] != nil {
		t.Fatalf("servers after import: %v", c.Servers)
	}
	v = screen(h)
	for _, want := range []string{"Imported from 1MCP", "imported server browser", "Next steps", "remove the 1MCP entry from Claude Code"} {
		if !strings.Contains(v, want) {
			t.Errorf("result lacks %q:\n%s", want, v)
		}
	}
	if !strings.Contains(v, "imported 5 server(s) from 1MCP") {
		t.Errorf("status:\n%s", v)
	}
	h.key("enter")
	if len(h.m.modals) != 0 || h.m.snap.Config.Servers["browser"] == nil {
		t.Fatal("result not closed or snapshot not reloaded")
	}
	// again with overwrite
	h.key("enter", "f", "y")
	if c := h.config(); c.Servers["docs"].Type != "http" {
		t.Errorf("overwrite: %+v", c.Servers["docs"])
	}
	// nothing new: y just closes
	h.key("esc", "enter", "y")
	if len(h.m.modals) != 0 || !strings.Contains(screen(h), "nothing to import") {
		t.Errorf("empty import:\n%s", screen(h))
	}
	// a missing file is reported
	os.Remove(filepath.Join(h.m.opts.Home, ".config/1mcp/mcp.json"))
	h.key("enter")
	if len(h.m.modals) != 0 || !strings.Contains(screen(h), "import from 1MCP") {
		t.Errorf("missing file:\n%s", screen(h))
	}
}

func TestSetupImportClaude(t *testing.T) {
	h := newHarness(t, TabSetup)
	h.copyFixture("../importer/testdata/claude-home", "")
	h.writeConfig(demoConfig(0))
	h.size(120, 60)
	h.load()
	h.selectSetup(func(r suRow) bool { return r.kind == suImport && r.imp == importClaude })
	h.key("enter")
	v := screen(h)
	for _, want := range []string{"Import from Claude Code", "+ alpha: marketplace demo/alpha-plugins", "enabled plugins: lsp-only,",
		"+ demo-skill", "+ local: local source for the copied skills", "claude plugin uninstall beta-tool@beta.local", "U on the Skills tab", "E on a plugin server"} {
		if !strings.Contains(v, want) {
			t.Errorf("preview lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "skilltri update") || strings.Contains(v, "skilltri plugin-server list") {
		t.Errorf("preview shows CLI next steps:\n%s", v)
	}
	h.key("esc")
	if len(h.m.modals) != 0 || len(h.config().Sources) != 0 {
		t.Fatal("esc imported")
	}
	h.key("enter", "y")
	c := h.config()
	if c.Sources["alpha"] == nil || c.Sources["beta-local"] == nil || c.Sources["local"] == nil {
		t.Fatalf("sources: %v", c.Sources)
	}
	if _, err := os.Stat(filepath.Join(h.m.opts.skillsDir(), "demo-skill", "SKILL.md")); err != nil {
		t.Errorf("skill not copied: %v", err)
	}
	v = screen(h)
	for _, want := range []string{"Imported from Claude Code", "copied skill demo-skill", "Next steps", "remove ~/.claude/skills/demo-skill", "U on the Skills tab"} {
		if !strings.Contains(v, want) {
			t.Errorf("result lacks %q:\n%s", want, v)
		}
	}
	// the catalog was refreshed offline (no daemon)
	if _, err := os.Stat(h.m.opts.Env.CatalogPath); err != nil {
		t.Errorf("catalog not refreshed: %v", err)
	}
}

// Review Focus 4: esc stops waiting for the import (reading and applying); the result still lands
// in the status line and nothing is left open.
func TestSetupImportCancel(t *testing.T) {
	keyring.MockInit()
	h := newHarness(t, TabSetup)
	h.writeHome(".config/1mcp/mcp.json", `{"mcpServers": {"demo": {"command": "demo-mcp"}}}`)
	h.load()
	h.selectSetup(func(r suRow) bool { return r.kind == suImport && r.imp == importOneMCP })

	// cancel while reading: the late preview is dropped
	_, read := h.m.Update(keyMsg("enter"))
	if !strings.Contains(screen(h), "reading from 1MCP … (esc on Setup cancels)") {
		t.Fatalf("no spinner:\n%s", screen(h))
	}
	h.key("esc")
	if h.m.busy(importWork(importOneMCP)) || !strings.Contains(screen(h), "cancelled") {
		t.Fatalf("esc did not cancel the read:\n%s", screen(h))
	}
	h.run(read)
	if len(h.m.modals) != 0 {
		t.Fatal("cancelled preview opened")
	}

	// cancel while applying: the dialog closes at once, the result lands in the status line
	h.key("enter")
	_, apply := h.m.Update(keyMsg("y"))
	if !strings.Contains(screen(h), "importing") {
		t.Fatalf("no apply spinner:\n%s", screen(h))
	}
	h.key("y") // ignored while applying
	h.key("esc")
	if len(h.m.modals) != 0 {
		t.Fatal("esc did not close the dialog")
	}
	h.key("enter") // a second import cannot start while one is applied
	if len(h.m.modals) != 0 {
		t.Fatal("second import started")
	}
	h.run(apply)
	v := screen(h)
	if len(h.m.modals) != 0 || !strings.Contains(v, "imported 1 server(s)") || !strings.Contains(v, "next steps not shown") {
		t.Errorf("late result:\n%s", v)
	}
	if h.m.busy(importWork(importOneMCP)) || h.config().Servers["demo"] == nil {
		t.Error("apply not finished")
	}
}

func TestSetupOverflow(t *testing.T) {
	keyring.MockInit()
	h := newHarness(t, TabSetup)
	long := strings.Repeat("n", 60)
	h.m.opts.Bin = "/" + strings.Repeat("deep/", 40) + "skilltri"
	h.m.tabs[TabSetup] = newSetupTab(h.m.opts.Bin)
	h.writeHome(".config/1mcp/mcp.json", `{"mcpServers": {"`+long+`": {"command": "`+strings.Repeat("c", 200)+`"}}}`)
	h.load()
	h.selectSetup(func(r suRow) bool { return r.kind == suImport && r.imp == importOneMCP })
	for _, sz := range [][2]int{{40, 12}, {89, 20}, {90, 20}, {300, 50}, {60, 30}} {
		h.size(sz[0], sz[1])
		for _, keys := range [][]string{nil, {"k", "k"}, {"j", "j", "enter"}, {"j", "j"}, {"esc", "k"}} {
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
		h.key("esc")
	}
}

// While one import is applied (its dialog closed), the other cannot start: no stuck spinner.
func TestSetupImportOneAtATime(t *testing.T) {
	keyring.MockInit()
	h := newHarness(t, TabSetup)
	h.writeHome(".config/1mcp/mcp.json", `{"mcpServers": {"demo": {"command": "demo-mcp"}}}`)
	h.copyFixture("../importer/testdata/claude-home", "")
	h.load()
	h.selectSetup(func(r suRow) bool { return r.kind == suImport && r.imp == importOneMCP })
	h.key("enter")
	_, apply := h.m.Update(keyMsg("y"))
	h.key("esc", "j") // close the dialog, select the Claude import
	_, read := h.m.Update(keyMsg("enter"))
	// the refusal shows over the running import's spinner until it expires
	if read != nil || h.m.busy(importWork(importClaude)) || !strings.Contains(screen(h), "another import is running") {
		t.Fatalf("second import started:\n%s", screen(h))
	}
	h.now = h.now.Add(statusTTL)
	if !strings.Contains(screen(h), "importing from 1MCP") {
		t.Fatalf("spinner not back after the flash expired:\n%s", screen(h))
	}
	h.run(apply)
	if h.m.busy(importWork(importOneMCP)) || h.m.busy(importWork(importClaude)) || len(h.m.busyKeys) != 0 {
		t.Fatalf("busy keys left: %v", h.m.busyKeys)
	}
	h.key("enter")
	if len(h.m.modals) != 1 || !strings.Contains(screen(h), "Import from Claude Code") {
		t.Fatalf("Claude import after the first finished:\n%s", screen(h))
	}
}

// esc, then enter again: the first (cancelled) read's late preview is dropped; the second owns the
// spinner until its own preview arrives.
func TestSetupImportStaleRead(t *testing.T) {
	h := newHarness(t, TabSetup)
	h.writeHome(".config/1mcp/mcp.json", `{"mcpServers": {"demo": {"command": "demo-mcp"}}}`)
	h.load()
	h.selectSetup(func(r suRow) bool { return r.kind == suImport && r.imp == importOneMCP })
	_, first := h.m.Update(keyMsg("enter"))
	h.key("esc")
	_, second := h.m.Update(keyMsg("enter"))
	h.run(first)
	if len(h.m.modals) != 0 || !h.m.busy(importWork(importOneMCP)) {
		t.Fatalf("stale preview applied (modals %d, busy %v)", len(h.m.modals), h.m.busy(importWork(importOneMCP)))
	}
	h.run(second)
	if len(h.m.modals) != 1 || h.m.busy(importWork(importOneMCP)) {
		t.Fatalf("second preview: modals %d busy %v", len(h.m.modals), h.m.busy(importWork(importOneMCP)))
	}
}

func TestCommandName(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "skilltri")
	os.WriteFile(bin, nil, 0o755)
	link := filepath.Join(t.TempDir(), "skilltri")
	os.Symlink(bin, link)
	found := func(p string) func(string) (string, error) {
		return func(string) (string, error) { return p, nil }
	}
	missing := func(string) (string, error) { return "", errors.New("not found") }
	for _, c := range []struct {
		bin  string
		look func(string) (string, error)
		want string
	}{
		{bin, found(bin), "skilltri"},
		{bin, found(link), "skilltri"}, // PATH has a symlink to it
		{bin, found("/usr/local/bin/skilltri"), bin},
		{bin, missing, bin},
		{"/opt/my tools/skilltri", missing, "'/opt/my tools/skilltri'"},
	} {
		if got := commandName(c.bin, c.look); got != c.want {
			t.Errorf("commandName(%q) = %q, want %q", c.bin, got, c.want)
		}
	}
	if got := hookLine("bash", "skilltri"); got != `eval "$(skilltri shell-init bash)"` {
		t.Errorf("hookLine = %q", got)
	}
}

// A wide rune in a one-cell column must not hang the wrapping.
func TestWrapWideRuneNarrow(t *testing.T) {
	if got := wrapCode("日本", 1); len(got) != 2 {
		t.Errorf("wrapCode = %q", got)
	}
	if got := wrap("日本", 1); len(got) != 2 {
		t.Errorf("wrap = %q", got)
	}
}

// TestSetupHarnessDuplicates: entries in a harness config that skilltri serves too are flagged on
// the harness row and listed in its details; project-dir adapters carry the parent-folder caveat.
func TestSetupHarnessDuplicates(t *testing.T) {
	h := newHarness(t, TabSetup)
	h.writeHome(".claude.json", `{"mcpServers": {"skilltri": {"command": "/opt/demo/bin/skilltri", "args": ["mcp"]},
	  "demo-1": {"command": "other"}}, "projects": {"/work/demo": {"mcpServers": {"dd": {"command": "demo-server"}}}}}`)
	h.writeHome(".cursor/mcp.json", `{"mcpServers": {"weather": {"command": "weather-mcp"}}}`)
	h.writeConfig(demoConfig(2))
	h.size(160, 60)
	h.load()
	if l := lineWith(h, "✓ Claude Code"); !strings.Contains(l, "! 2 duplicates") {
		t.Errorf("claude row: %q", l)
	}
	if l := lineWith(h, "Cursor "); strings.Contains(l, "duplicate") {
		t.Errorf("cursor row: %q", l)
	}
	h.selectSetup(func(r suRow) bool { return r.kind == suHarness && r.snip.Harness == "claude" })
	v := screen(h)
	for _, want := range []string{"duplicates", "demo-1 (same name as demo-1)", "dd in /work/demo (same command as demo-1)", "skilltri serves"} {
		if !strings.Contains(v, want) {
			t.Errorf("claude details lack %q:\n%s", want, v)
		}
	}
	h.selectSetup(func(r suRow) bool { return r.kind == suAdapter && r.adapter.ID == "codex" })
	if v := screen(h); !strings.Contains(v, "parent folders") {
		t.Errorf("project-dir adapter details lack the parent-folder caveat:\n%s", v)
	}
	h.selectSetup(func(r suRow) bool { return r.kind == suAdapter && r.adapter.ID == "claude-code" })
	if v := screen(h); strings.Contains(v, "parent folders") {
		t.Errorf("claude adapter has the project-dir caveat:\n%s", v)
	}
}

func TestSetupAgentAccess(t *testing.T) {
	h := newHarness(t, TabSetup)
	h.writeConfig(demoConfig(1))
	h.size(160, 60)
	h.load()
	h.selectSetup(rowKind(suAgent))
	if l := lineWith(h, "agent tools"); !strings.Contains(l, "write") {
		t.Errorf("default row: %q", l)
	}
	for _, want := range []string{"settings.agentAccess", "write: agents can also change skilltri", "audit` to see them"} {
		if !strings.Contains(screen(h), want) {
			t.Errorf("details lack %q:\n%s", want, screen(h))
		}
	}
	if !strings.Contains(ansi.Strip(h.m.footer(160)), "off/read/write") {
		t.Errorf("hints: %q", ansi.Strip(h.m.footer(160)))
	}
	for _, want := range []string{config.AgentAccessOff, config.AgentAccessRead, config.AgentAccessWrite, config.AgentAccessOff} {
		h.key(" ")
		if got := h.config().Settings.AgentAccess; got != want {
			t.Fatalf("agentAccess = %q, want %q", got, want)
		}
		if l := lineWith(h, "agent tools"); !strings.Contains(l, want) || !strings.Contains(screen(h), "agent access is now "+want) {
			t.Errorf("row after %s: %q\n%s", want, l, screen(h))
		}
	}
}
