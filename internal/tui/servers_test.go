package tui

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/zalando/go-keyring"

	"github.com/baeroe/skilltri/internal/actions"
	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/daemon"
	"github.com/baeroe/skilltri/internal/downstream"
	"github.com/baeroe/skilltri/internal/rules"
	"github.com/baeroe/skilltri/internal/secrets"
)

func screen(h *harness) string { return ansi.Strip(h.m.View()) }

func (h *harness) config() *config.Config {
	h.t.Helper()
	c, err := config.Load(h.m.opts.Env.ConfigPath)
	if err != nil {
		h.t.Fatal(err)
	}
	return c
}

func (h *harness) rawConfig() []byte {
	b, _ := os.ReadFile(h.m.opts.Env.ConfigPath)
	return b
}

// form is the open server form (fails the test when there is none).
func (h *harness) form() *serverForm {
	h.t.Helper()
	if n := len(h.m.modals); n > 0 {
		if f, ok := h.m.modals[n-1].(*serverForm); ok {
			return f
		}
	}
	h.t.Fatalf("no server form open:\n%s", screen(h))
	return nil
}

// topForm is the form of the top modal (fails the test when it has none).
func (h *harness) topForm() *form {
	h.t.Helper()
	if n := len(h.m.modals); n > 0 {
		if f, ok := h.m.modals[n-1].(interface{ formOf() *form }); ok {
			return f.formOf()
		}
	}
	h.t.Fatalf("no form open:\n%s", screen(h))
	return nil
}

// set focuses the form field key, clears it and types value; lines of a multiline field are
// separated by "\n".
func (h *harness) set(key, value string) {
	h.t.Helper()
	f := h.topForm()
	f.focusKey(key)
	fl := f.current()
	if fl.kind == linesField {
		fl.lines = fl.lines[:1]
		fl.line = 0
	}
	h.key("ctrl+u")
	for i, l := range strings.Split(value, "\n") {
		if i > 0 {
			h.key("enter")
		}
		if l != "" {
			h.key(l)
		}
	}
}

// demoServers is a config with a ready, a failing and a needs-review plugin server, plus a status
// and a which reply for them.
func demoServers(h *harness) (*config.Config, *daemon.StatusReply, *daemon.WhichReply) {
	home := h.m.opts.Home
	cfg := &config.Config{Version: 1, Servers: map[string]*config.Server{
		"demo-ready": {Type: "stdio", Command: "demo-server", Args: []string{"--port", "8080", "two words"},
			Env:   map[string]string{"API_KEY": secrets.Ref("demo-ready-api_key"), "PLAIN": "literal-value"},
			Rules: []rules.Rule{{Path: filepath.Join(home, "x"), Mode: rules.Only}},
			Tools: map[string]*config.Tool{"write": {Disabled: true}}},
		"demo-broken": {Type: "http", URL: "https://demo.example/mcp", Headers: map[string]string{"Authorization": "Bearer literal-token"},
			Rules: []rules.Rule{{Path: "/work/a", Mode: rules.Tree}, {Path: "/work/b", Mode: rules.Tree}, {Path: "/work/c", Mode: rules.Not}}},
		"demo-plugin": {Type: "stdio", Command: "old-cmd", Origin: &config.Origin{Source: "demo-src", Plugin: "p", Server: "s", DefHash: "old"}},
	}, Sources: map[string]*config.Source{"demo-src": {Type: "marketplace", URL: "demo/market"}}}
	st := &daemon.StatusReply{PID: 4242, Version: "0.1.0", Servers: []downstream.ServerStatus{
		{Name: "demo-broken", State: "error", Error: "connect failed: demo refused", StderrTail: []string{"panic: demo failure", "exit status 2"}},
		{Name: "demo-plugin", State: "stopped"},
		{Name: "demo-ready", State: "ready", Tools: 2, Sessions: 1},
	}}
	wh := &daemon.WhichReply{Dir: "/work/demo", Servers: []daemon.WhichServer{
		{Name: "demo-ready", State: "ready", Decision: rules.Decision{Active: true}, Tools: []daemon.WhichTool{
			{Name: "search", Exposed: "demo-ready_search", Decision: rules.Decision{Active: true, Reason: "server: no rules"}},
			{Name: "write", Exposed: "demo-ready_write", Decision: rules.Decision{Active: false, Reason: "tool: disabled"}},
		}},
	}}
	return cfg, st, wh
}

func TestServersListAndDetails(t *testing.T) {
	h := newHarness(t, TabServers)
	cfg, st, wh := demoServers(h)
	h.writeConfig(cfg)
	h.writeCatalog(&catalog.Catalog{Servers: []catalog.PluginServer{{Source: "demo-src", Plugin: "p", Name: "s", DefHash: "new", Def: config.Server{Type: "stdio", Command: "new-cmd"}}}})
	h.status, h.which = st, wh
	h.size(160, 40)
	h.load()
	v := screen(h)
	for _, want := range []string{"! demo-broken", "? demo-plugin", "● demo-ready", "2 tools", "1 sess", "only ~/x", "+2 rules", "everywhere", "(plugin demo-src/p)"} {
		if !strings.Contains(v, want) {
			t.Errorf("list misses %q:\n%s", want, v)
		}
	}
	// demo-broken is selected first (sorted by name)
	for _, want := range []string{"https://demo.example/mcp", "Authorization", "•••", "connect failed: demo refused", "panic: demo failure", "exit status 2", "/work/a", "/work/c"} {
		if !strings.Contains(v, want) {
			t.Errorf("details of demo-broken miss %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "literal-token") {
		t.Errorf("header value shown:\n%s", v)
	}
	h.key("j")
	if v = screen(h); !strings.Contains(v, "needs review") || !strings.Contains(v, "demo-src/p/s") {
		t.Errorf("details of demo-plugin:\n%s", v)
	}
	h.key("j")
	v = screen(h)
	for _, want := range []string{"demo-server --port 8080 'two words'", "API_KEY", "${secret:demo-ready-api_key}", "PLAIN", "✓ search", "✗ write", "only ~/x"} {
		if !strings.Contains(v, want) {
			t.Errorf("details of demo-ready miss %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "literal-value") {
		t.Errorf("env value shown:\n%s", v)
	}
	if !slices.ContainsFunc(h.hellos, func(hl daemon.Hello) bool {
		return hl.Cmd == "which" && len(hl.Args) == 1 && hl.Args[0] == "/work/demo"
	}) {
		t.Errorf("no which for the preview folder: %+v", h.hellos)
	}

	// offline: no tool lists, the configured overrides stay visible
	h.status = nil
	h.run(h.m.reload())
	if v = screen(h); !strings.Contains(v, "tools unavailable (daemon not running)") || !strings.Contains(v, "✗ write") {
		t.Errorf("offline details:\n%s", v)
	}
}

// TestServersNoOverflow: long names and paths, the form and the confirmation at every size.
func TestServersNoOverflow(t *testing.T) {
	h := newHarness(t, TabServers)
	cfg, st, wh := demoServers(h)
	long := strings.Repeat("n", 32)
	path := "/" + strings.Repeat("deep/", 39) + "end"
	cfg.Servers[long] = &config.Server{Type: "stdio", Command: path, Cwd: path, Args: []string{path, path},
		Env: map[string]string{strings.Repeat("K", 60): path}, Rules: []rules.Rule{{Path: path, Mode: rules.Tree}},
		Origin: &config.Origin{Source: strings.Repeat("s", 60), Plugin: strings.Repeat("p", 60), Server: "x"}}
	st.Servers = append(st.Servers, downstream.ServerStatus{Name: long, State: "error", Error: "failed " + path, StderrTail: []string{path}})
	h.writeConfig(cfg)
	h.status, h.which = st, wh
	h.load()
	for _, sz := range [][2]int{{40, 12}, {89, 24}, {90, 24}, {300, 50}} {
		h.size(sz[0], sz[1])
		for _, sel := range []string{long, "demo-ready"} {
			h.m.tabs[TabServers].(*serversTab).sel = sel
			checkScreen(t, h.m.View(), sz[0], sz[1], "servers "+sel)
			h.key("t")
			checkScreen(t, h.m.View(), sz[0], sz[1], "tools "+sel)
			h.key("esc")
			h.key("e")
			checkScreen(t, h.m.View(), sz[0], sz[1], "form "+sel)
			h.set("args", path+` "unterminated`)
			h.key("ctrl+s")
			checkScreen(t, h.m.View(), sz[0], sz[1], "form error "+sel)
			h.key("esc")
			h.key("d")
			checkScreen(t, h.m.View(), sz[0], sz[1], "confirm "+sel)
			h.key("n")
		}
	}
}

func TestServerAddForm(t *testing.T) {
	h := newHarness(t, TabServers)
	h.writeConfig(demoConfig(1))
	h.load()
	before := h.rawConfig()
	h.key("a")
	h.form()
	for _, c := range []struct{ field, value, wantErr string }{
		{"name", "bad name!", "letters, digits and '-'"},
		{"name", "skilltri", "reserved"},
		{"name", "demo-1", "already exists"},
		{"command", "", "required"},
		{"args", `--x "unterminated`, "unterminated quote"},
		{"cwd", "relative/dir", "absolute path"},
		{"env", "GOOD=1\nnot a pair", "line 2"},
		{"env", "TOKEN=secret:bad name", "secret name"},
		{"timeout", "soon", "milliseconds"},
	} {
		h.set("name", "demo-new")
		h.set("command", "demo-server")
		h.set("args", "")
		h.set("cwd", "")
		h.set("env", "")
		h.set("timeout", "")
		h.set(c.field, c.value)
		h.form().form.focusKey("name")
		h.key("ctrl+s")
		f := h.form()
		if fl := f.form.get(c.field); !strings.Contains(fl.err+f.form.err, c.wantErr) {
			t.Errorf("%s=%q: error %q / %q, want %q", c.field, c.value, fl.err, f.form.err, c.wantErr)
		}
		if !strings.Contains(screen(h), c.wantErr) {
			t.Errorf("%s=%q: error not shown:\n%s", c.field, c.value, screen(h))
		}
		if !bytes.Equal(before, h.rawConfig()) {
			t.Fatalf("%s=%q: config written", c.field, c.value)
		}
	}
	// http without a url, then a valid stdio server
	h.form().form.focusKey("type")
	h.key("right")
	h.form().form.focusKey("name")
	h.key("ctrl+s")
	if f := h.form(); !strings.Contains(f.form.get("url").err, "required") {
		t.Errorf("http without url: %q", f.form.get("url").err)
	}
	h.form().form.focusKey("type")
	h.key("left")
	h.set("name", "demo-new")
	h.set("command", "demo-server")
	h.set("args", `--flag "two words" 'x y' a\ b`)
	h.set("cwd", "~/work")
	h.set("env", "TOKEN=secret:demo-token\n\nMODE=fast")
	h.set("timeout", "5000")
	h.form().form.focusKey("perSession")
	h.key(" ")
	h.form().form.focusKey("name")
	h.key("ctrl+s")
	if len(h.m.modals) != 0 {
		t.Fatalf("form still open:\n%s", screen(h))
	}
	s := h.config().Servers["demo-new"]
	if s == nil {
		t.Fatal("server not added")
	}
	want := config.Server{Type: "stdio", Command: "demo-server", Args: []string{"--flag", "two words", "x y", "a b"},
		Cwd: filepath.Join(h.m.opts.Home, "work"), Env: map[string]string{"TOKEN": "${secret:demo-token}", "MODE": "fast"},
		RequestTimeoutMs: 5000, PerSession: true}
	if fmt.Sprint(*s) != fmt.Sprint(want) {
		t.Errorf("server = %+v\nwant %+v", *s, want)
	}
	if v := screen(h); !strings.Contains(v, "saved demo-new") || h.m.tabs[TabServers].(*serversTab).sel != "demo-new" {
		t.Errorf("after save: sel %q\n%s", h.m.tabs[TabServers].(*serversTab).sel, v)
	}
}

func TestSplitJoinArgs(t *testing.T) {
	for in, want := range map[string][]string{
		`a b`:               {"a", "b"},
		`  "x y"  'z w' `:   {"x y", "z w"},
		`a\ b "c\"d" 'e\f'`: {"a b", `c"d`, `e\f`},
		`""`:                {""},
		`pre"mid dle"post`:  {"premid dlepost"},
		``:                  nil,
		`--root=/x "it's"`:  {"--root=/x", "it's"},
		`'a'"b"`:            {"ab"},
		"tab\tsep\nnewline": {"tab", "sep", "newline"},
	} {
		got, err := splitArgs(in)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("splitArgs(%q) = %q, %v; want %q", in, got, err, want)
		}
		if back, err := splitArgs(joinArgs(want)); err != nil || !slices.Equal(back, want) {
			t.Errorf("round trip of %q via %q: %q, %v", want, joinArgs(want), back, err)
		}
	}
	for _, bad := range []string{`"open`, `'open`, `trailing\`} {
		if _, err := splitArgs(bad); err == nil {
			t.Errorf("splitArgs(%q) accepted", bad)
		}
	}
	if got := joinArgs([]string{"--port", "8080", "two words", "it's", ""}); got != `--port 8080 'two words' "it's" ''` {
		t.Errorf("joinArgs = %s", got)
	}
}

func TestServerEditKeepsRules(t *testing.T) {
	h := newHarness(t, TabServers)
	cfg := demoConfig(0)
	cfg.Servers["demo-a"] = &config.Server{Type: "stdio", Command: "old", Disabled: true,
		Env:    map[string]string{"B": "2", "A": "1"},
		Rules:  []rules.Rule{{Path: "/work/demo", Mode: rules.Tree}},
		Tools:  map[string]*config.Tool{"t": {Disabled: true}},
		Origin: &config.Origin{Source: "demo-src", Plugin: "p", Server: "s", DefHash: "h"}}
	h.writeConfig(cfg)
	h.load()
	h.key("e")
	if got := h.form().form.get("env").text(); got != "A=1\nB=2" {
		t.Errorf("env field = %q", got)
	}
	h.set("command", "new")
	h.set("name", "demo-b")
	h.key("ctrl+s")
	if len(h.m.modals) != 0 {
		t.Fatalf("form still open:\n%s", screen(h))
	}
	c := h.config()
	s := c.Servers["demo-b"]
	if c.Servers["demo-a"] != nil || s == nil {
		t.Fatalf("rename failed: %v", c.Servers)
	}
	if s.Command != "new" || !s.Disabled || len(s.Rules) != 1 || s.Tools["t"] == nil || !s.Tools["t"].Disabled || s.Origin == nil || s.Env["A"] != "1" {
		t.Errorf("after edit: %+v", *s)
	}
	if h.m.tabs[TabServers].(*serversTab).sel != "demo-b" {
		t.Error("renamed server not selected")
	}
	// a rename onto an existing name is an inline error
	cfg = h.config()
	cfg.Servers["demo-c"] = &config.Server{Type: "stdio", Command: "c"}
	h.writeConfig(cfg)
	h.run(h.m.reload())
	before := h.rawConfig()
	h.key("e")
	h.set("name", "demo-c")
	h.key("ctrl+s")
	if f := h.form(); !strings.Contains(f.form.get("name").err, "already exists") {
		t.Errorf("rename onto demo-c: %q / %q", f.form.get("name").err, f.form.err)
	}
	if !bytes.Equal(before, h.rawConfig()) {
		t.Error("config changed by a failed rename")
	}
}

func TestServerDeleteConfirm(t *testing.T) {
	h := newHarness(t, TabServers)
	h.writeConfig(demoConfig(2))
	h.load()
	h.key("d")
	if v := screen(h); !strings.Contains(v, "Remove server") || !strings.Contains(v, "demo-1") {
		t.Fatalf("no confirmation:\n%s", v)
	}
	h.key("n")
	if len(h.m.modals) != 0 || h.config().Servers["demo-1"] == nil {
		t.Fatal("cancel removed the server or kept the modal")
	}
	h.key("d", "y")
	if len(h.m.modals) != 0 || h.config().Servers["demo-1"] != nil || h.config().Servers["demo-2"] == nil {
		t.Fatalf("confirm: %v", h.config().Servers)
	}
	if v := screen(h); !strings.Contains(v, "removed demo-1") {
		t.Errorf("no status:\n%s", v)
	}
}

func TestToggleTool(t *testing.T) {
	h := newHarness(t, TabServers)
	cfg, st, wh := demoServers(h)
	h.writeConfig(cfg)
	h.status, h.which = st, wh
	h.load()
	h.m.tabs[TabServers].(*serversTab).sel = "demo-ready"
	h.key("t", " ") // first tool: search
	tl := h.config().Servers["demo-ready"].Tools
	if tl["search"] == nil || !tl["search"].Disabled {
		t.Fatalf("search not disabled: %+v", tl)
	}
	h.key("j", " ") // write: disabled → enabled
	if tl = h.config().Servers["demo-ready"].Tools; tl["write"].Disabled {
		t.Fatalf("write not enabled: %+v", tl["write"])
	}
	h.key("esc", " ") // back in the list: space toggles the server
	if !h.config().Servers["demo-ready"].Disabled {
		t.Fatal("server not disabled")
	}
	if v := screen(h); !strings.Contains(v, "disabled demo-ready") {
		t.Errorf("status:\n%s", v)
	}
	h.key(" ")
	if h.config().Servers["demo-ready"].Disabled {
		t.Fatal("server not enabled again")
	}
}

func TestSecretLineToKeyring(t *testing.T) {
	keyring.MockInit()
	h := newHarness(t, TabServers)
	h.load()
	h.key("a")
	h.set("env", "API_KEY=s3cr3t-demo")
	h.key("ctrl+k") // before a name is set: no secret name
	if f := h.form(); !strings.Contains(f.form.get("name").err, "name") {
		t.Errorf("no name error: %q / %q", f.form.get("name").err, f.form.get("env").err)
	}
	h.set("name", "demo-sec")
	h.set("command", "demo-server")
	h.set("env", "MODE=fast\nAPI_KEY=s3cr3t-demo")
	h.key("ctrl+k") // on the API_KEY line
	if v, err := secrets.Get("demo-sec-api_key"); err != nil || v != "s3cr3t-demo" {
		t.Fatalf("keyring: %q, %v", v, err)
	}
	if got := h.form().form.get("env").text(); got != "MODE=fast\nAPI_KEY=${secret:demo-sec-api_key}" {
		t.Errorf("env field = %q", got)
	}
	if v := screen(h); strings.Contains(v, "s3cr3t") {
		t.Errorf("literal still shown:\n%s", v)
	}
	h.form().form.focusKey("type")
	h.key("right")
	h.set("url", "https://demo.example/mcp")
	h.set("headers", "X-Api+Token: tok-demo")
	h.key("ctrl+k")
	if v, err := secrets.Get("demo-sec-x-api-token"); err != nil || v != "tok-demo" {
		t.Fatalf("header secret: %q, %v", v, err)
	}
	h.form().form.focusKey("type")
	h.key("left")
	h.form().form.focusKey("env") // ctrl+s saves from an env line too
	h.key("ctrl+s")
	if len(h.m.modals) != 0 {
		t.Fatalf("form still open:\n%s", screen(h))
	}
	if s := h.config().Servers["demo-sec"]; s == nil || s.Env["API_KEY"] != "${secret:demo-sec-api_key}" {
		t.Fatalf("config: %+v", s)
	}
	if bytes.Contains(h.rawConfig(), []byte("s3cr3t")) {
		t.Error("literal in the config file")
	}
	h.m.tabs[TabServers].(*serversTab).sel = "demo-sec"
	if v := screen(h); !strings.Contains(v, "${secret:demo-sec-api_key}") || strings.Contains(v, "s3cr3t") {
		t.Errorf("details:\n%s", v)
	}
}

func TestFormSurvivesReload(t *testing.T) {
	h := newHarness(t, TabServers)
	h.writeConfig(demoConfig(1))
	h.load()
	h.key("e")
	h.set("command", "typed-command")
	// another writer adds a server; the snapshot reloads under the open form
	cfg := h.config()
	cfg.Servers["demo-other"] = &config.Server{Type: "stdio", Command: "other"}
	h.writeConfig(cfg)
	h.run(h.m.reload())
	if len(h.m.snap.Config.Servers) != 2 {
		t.Fatal("snapshot not reloaded")
	}
	if got := h.form().form.get("command").value(); got != "typed-command" {
		t.Fatalf("typed input lost: %q", got)
	}
	h.key("ctrl+s")
	c := h.config()
	if c.Servers["demo-other"] == nil || c.Servers["demo-1"].Command != "typed-command" {
		t.Fatalf("save did not merge: %+v", c.Servers)
	}
	// the edited server disappears while the form is open
	h.key("e")
	h.set("command", "again")
	cfg = h.config()
	delete(cfg.Servers, "demo-1")
	h.writeConfig(cfg)
	h.run(h.m.reload())
	before := h.rawConfig()
	h.key("ctrl+s")
	f := h.form()
	if !strings.Contains(f.form.err, "server demo-1 no longer exists") || !strings.Contains(screen(h), "no longer exists") {
		t.Errorf("form error %q:\n%s", f.form.err, screen(h))
	}
	if f.form.get("command").value() != "again" || !bytes.Equal(before, h.rawConfig()) {
		t.Error("input lost or config changed")
	}
}

func TestRestartOffline(t *testing.T) {
	h := newHarness(t, TabServers)
	h.writeConfig(demoConfig(1))
	h.load()
	h.key("x")
	if v := screen(h); !strings.Contains(v, "daemon is not running") {
		t.Errorf("offline restart:\n%s", v)
	}
	h.status = readyStatus(1, 1)
	h.run(h.m.reload())
	h.key("x")
	if !slices.ContainsFunc(h.hellos, func(hl daemon.Hello) bool { return hl.Cmd == "restart" && slices.Equal(hl.Args, []string{"demo-1"}) }) {
		t.Errorf("no restart request: %+v", h.hellos)
	}
	if v := screen(h); !strings.Contains(v, "restarted demo-1") {
		t.Errorf("status:\n%s", v)
	}
	h.cmdErr = map[string]error{"restart": fmt.Errorf("no such server %q", "demo-1")}
	h.key("x")
	if v := screen(h); !strings.Contains(v, "restart demo-1: no such server") {
		t.Errorf("error status:\n%s", v)
	}
}

func TestAcceptPluginServer(t *testing.T) {
	h := newHarness(t, TabServers)
	market := filepath.Join(t.TempDir(), "demo-market")
	if err := os.CopyFS(market, os.DirFS("../marketplace/testdata/demo-market")); err != nil {
		t.Fatal(err)
	}
	e := h.m.opts.Env
	if err := actions.SourceAdd(e, "m", config.Source{Type: "marketplace", Path: market}); err != nil {
		t.Fatal(err)
	}
	if _, err := actions.PluginServerEnable(e, "m/writing/notes", ""); err != nil {
		t.Fatal(err)
	}
	h.load()
	h.key("A")
	if v := screen(h); !strings.Contains(v, "not waiting for review") {
		t.Errorf("accept of an unchanged server:\n%s", v)
	}
	mcp := filepath.Join(market, "plugins", "writing", ".mcp.json")
	b, _ := os.ReadFile(mcp)
	os.WriteFile(mcp, bytes.Replace(b, []byte(`"MODE": "demo"`), []byte(`"MODE": "changed"`), 1), 0o644)
	if err := e.RefreshCatalog(); err != nil {
		t.Fatal(err)
	}
	h.run(h.m.reload())
	h.key("A")
	v := screen(h)
	if !strings.Contains(v, "MODE") || !strings.Contains(v, "changed") || !strings.Contains(v, "Accept") {
		t.Fatalf("accept modal:\n%s", v)
	}
	h.key("n")
	if h.config().Servers["writing-notes"].Env["MODE"] != "demo" {
		t.Fatal("cancel accepted")
	}
	h.key("A", "y")
	if got := h.config().Servers["writing-notes"].Env["MODE"]; got != "changed" {
		t.Fatalf("after accept MODE = %q", got)
	}
	if v := screen(h); !strings.Contains(v, "accepted") {
		t.Errorf("status:\n%s", v)
	}
}

// TestBusyFeedback: work in flight shows a spinner in the status line, a repeated key does not
// start it twice, and the result ends it.
func TestBusyFeedback(t *testing.T) {
	h := newHarness(t, TabServers)
	h.writeConfig(demoConfig(1))
	h.status = readyStatus(1, 1)
	h.load()
	_, cmd := h.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if cmd == nil {
		t.Fatal("no restart command")
	}
	if v := screen(h); !strings.Contains(v, "| restarting demo-1 …") {
		t.Errorf("no busy status:\n%s", v)
	}
	h.m.Update(spinMsg{})
	if v := screen(h); !strings.Contains(v, "/ restarting demo-1 …") {
		t.Errorf("spinner did not advance:\n%s", v)
	}
	if _, again := h.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")}); again != nil {
		t.Error("a second restart started while the first is pending")
	}
	h.run(cmd)
	if n := slices.IndexFunc(h.hellos, func(hl daemon.Hello) bool { return hl.Cmd == "restart" }); n < 0 ||
		len(slices.DeleteFunc(slices.Clone(h.hellos), func(hl daemon.Hello) bool { return hl.Cmd != "restart" })) != 1 {
		t.Errorf("restart requests: %+v", h.hellos)
	}
	if v := screen(h); strings.Contains(v, "restarting") || !strings.Contains(v, "restarted demo-1") {
		t.Errorf("after the result:\n%s", v)
	}
	if h.m.spin() != nil {
		t.Error("spinner scheduled while idle")
	}
	// a toggle shows its own busy text
	_, cmd = h.m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	if v := screen(h); !strings.Contains(v, "disabling demo-1 …") {
		t.Errorf("no busy toggle:\n%s", v)
	}
	h.run(cmd)
	if !h.config().Servers["demo-1"].Disabled {
		t.Error("toggle not applied")
	}
}

func TestDangerConfirmNeedsY(t *testing.T) {
	h := newHarness(t, TabServers)
	h.writeConfig(demoConfig(1))
	h.load()
	h.key("d", "enter")
	if len(h.m.modals) != 0 || h.config().Servers["demo-1"] == nil {
		t.Fatal("enter removed the server or kept the modal")
	}
}

func TestToggleToolRulesNote(t *testing.T) {
	h := newHarness(t, TabServers)
	cfg, st, wh := demoServers(h)
	cfg.Servers["demo-ready"].Rules = nil // only the tool's own rules decide
	cfg.Servers["demo-ready"].Tools["search"] = &config.Tool{Rules: []rules.Rule{{Path: "/elsewhere", Mode: rules.Only}}}
	wh.Servers[0].Tools[0].Decision = rules.Decision{Reason: "tool: no include matches"}
	h.writeConfig(cfg)
	h.status, h.which = st, wh
	h.load()
	h.m.tabs[TabServers].(*serversTab).sel = "demo-ready"
	h.key("t", " ")
	if v := screen(h); !strings.Contains(v, "disabled tool search of demo-ready (rules also exclude it in /work/demo)") {
		t.Errorf("disable note:\n%s", v)
	}
	h.key(" ")
	if v := screen(h); !strings.Contains(v, "enabled tool search of demo-ready, but rules exclude it in /work/demo") {
		t.Errorf("enable note:\n%s", v)
	}
	h.key("j", " ") // write: excluded only by its disabled flag
	if v := screen(h); !strings.Contains(v, "enabled tool write of demo-ready") || strings.Contains(v, "but rules") {
		t.Errorf("plain enable:\n%s", v)
	}
}

func TestWhichRetriedOnTick(t *testing.T) {
	h := newHarness(t, TabServers)
	cfg, st, wh := demoServers(h)
	h.writeConfig(cfg)
	h.status, h.which = st, wh
	h.cmdErr = map[string]error{"which": fmt.Errorf("read: %w", os.ErrDeadlineExceeded)}
	h.load()
	h.m.tabs[TabServers].(*serversTab).sel = "demo-ready"
	if v := screen(h); !strings.Contains(v, "tools unavailable (read: i/o timeout)") {
		t.Errorf("which error:\n%s", v)
	}
	h.cmdErr = nil
	h.tick(2 * time.Second)
	if v := screen(h); !strings.Contains(v, "✓ search") {
		t.Errorf("which not retried on the tick:\n%s", v)
	}
}
