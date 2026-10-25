package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/downstream"
	"github.com/baeroe/skilltri/internal/rules"
	"github.com/baeroe/skilltri/internal/testutil/fakemcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var fakeBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakemcp")
	if err != nil {
		panic(err)
	}
	fakeBin = filepath.Join(dir, "fakemcp")
	out, err := exec.Command("go", "build", "-o", fakeBin, "github.com/baeroe/skilltri/internal/testutil/fakemcp/cmd/fakemcp").CombinedOutput()
	if err != nil {
		panic(fmt.Sprintf("build fakemcp: %v\n%s", err, out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestServersAndToolDecision(t *testing.T) {
	p := rules.Normalize(t.TempDir())
	cfg := &config.Config{Version: 1, Servers: map[string]*config.Server{
		"a": {Type: "stdio", Command: "x", Tools: map[string]*config.Tool{
			"x": {Rules: []rules.Rule{{Path: p, Mode: rules.NotTree}}},
		}},
		"b": {Type: "stdio", Command: "x", Rules: []rules.Rule{{Path: p, Mode: rules.Only}}},
		"c": {Type: "stdio", Command: "x", Disabled: true},
	}}
	got := Servers(cfg, p)
	want := map[string]bool{"a": true, "b": true, "c": false}
	if len(got) != 3 || got[0].Name != "a" || got[1].Name != "b" || got[2].Name != "c" {
		t.Fatalf("Servers = %+v", got)
	}
	for _, d := range got {
		if d.Decision.Active != want[d.Name] {
			t.Errorf("%s active = %v (%s), want %v", d.Name, d.Decision.Active, d.Decision.Reason, want[d.Name])
		}
	}
	if d := ToolDecision(cfg, "a", "x", p); d.Active {
		t.Errorf("a.x active: %s", d.Reason)
	}
	if d := ToolDecision(cfg, "a", "y", p); !d.Active {
		t.Errorf("a.y inactive: %s", d.Reason)
	}
}

func TestExposedName(t *testing.T) {
	if got := ExposedName("docs", "search"); got != "docs_search" {
		t.Fatalf("ExposedName = %q", got)
	}
	long := strings.Repeat("t", 70)
	got := ExposedName("docs", long)
	if len(got) != 64 || got != ExposedName("docs", long) {
		t.Fatalf("long name = %q (%d)", got, len(got))
	}
	other := strings.Repeat("t", 60) + strings.Repeat("u", 10)
	if ExposedName("docs", other) == got {
		t.Fatalf("names sharing a 60-char prefix collide: %q", got)
	}
}

// harness is a session connected to an in-memory client.
type harness struct {
	cfg     atomic.Pointer[config.Config]
	mgr     *downstream.Manager
	sess    *Session
	cs      *mcp.ClientSession
	changed chan string // "tools" | "prompts" | "resources"
}

func stdio(env map[string]string) *config.Server {
	return &config.Server{Type: "stdio", Command: fakeBin, Env: env}
}

func newHarness(t *testing.T, cfg *config.Config, dir string) *harness {
	t.Helper()
	return connectHarness(t, cfg, SessionInfo{ID: "s1", Dir: dir, ClientName: "test"}, nil)
}

// connectHarness creates a session for info with skills, refreshes it and connects a client.
func connectHarness(t *testing.T, cfg *config.Config, info SessionInfo, skills func(string) []catalog.Entry) *harness {
	t.Helper()
	h := &harness{changed: make(chan string, 16)}
	h.cfg.Store(cfg)
	h.mgr = downstream.NewManager(downstream.Options{Lookup: func(name string) (*config.Server, bool) {
		s, ok := h.cfg.Load().Servers[name]
		return s, ok
	}})
	t.Cleanup(h.mgr.Close)
	h.sess = NewSession(info, h.cfg.Load, h.mgr, skills)
	t.Cleanup(h.sess.Close)
	connectSession(t, h)
	return h
}

// connectSession refreshes h.sess and connects a client to it, recording list_changed in h.changed.
func connectSession(t *testing.T, h *harness) {
	t.Helper()
	ctx := context.Background()
	if err := h.sess.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	st, ct := mcp.NewInMemoryTransports()
	ss, err := h.sess.MCPServer().Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	notify := func(kind string) { h.changed <- kind }
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, &mcp.ClientOptions{
		ToolListChangedHandler:     func(context.Context, *mcp.ToolListChangedRequest) { notify("tools") },
		PromptListChangedHandler:   func(context.Context, *mcp.PromptListChangedRequest) { notify("prompts") },
		ResourceListChangedHandler: func(context.Context, *mcp.ResourceListChangedRequest) { notify("resources") },
	})
	h.cs, err = client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.cs.Close() })
}

func (h *harness) tools(t *testing.T) []string {
	t.Helper()
	res, err := h.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	return names
}

func (h *harness) waitChanged(t *testing.T, kind string) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case k := <-h.changed:
			if k == kind {
				return
			}
		case <-timeout:
			t.Fatalf("no %s list_changed notification", kind)
		}
	}
}

func (h *harness) call(t *testing.T, name string) *mcp.CallToolResult {
	t.Helper()
	res, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"v": 1}})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return res
}

func text(res *mcp.CallToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}
	if tc, ok := res.Content[0].(*mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}

// twoServers is downstream a (tools x,y; extra env) everywhere and b (tool z) only in p.
func twoServers(p string, aEnv map[string]string) *config.Config {
	env := map[string]string{"FAKEMCP_NAME": "a", "FAKEMCP_TOOLS": "x,y"}
	for k, v := range aEnv {
		env[k] = v
	}
	b := stdio(map[string]string{"FAKEMCP_NAME": "b", "FAKEMCP_TOOLS": "z"})
	b.Rules = []rules.Rule{{Path: p, Mode: rules.Only}}
	return &config.Config{Version: 1, Servers: map[string]*config.Server{"a": stdio(env), "b": b}}
}

func projectDirs(t *testing.T) (p, sub string) {
	t.Helper()
	p = t.TempDir()
	sub = filepath.Join(p, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	return p, sub
}

func TestSessionLists(t *testing.T) {
	p, sub := projectDirs(t)
	h := newHarness(t, twoServers(p, nil), sub)
	if got := h.tools(t); !slices.Equal(got, []string{"a_x", "a_y"}) {
		t.Fatalf("tools = %v", got)
	}
	var e fakemcp.Echo
	if err := json.Unmarshal([]byte(text(h.call(t, "a_x"))), &e); err != nil {
		t.Fatal(err)
	}
	if e.Server != "a" || string(e.Args) != `{"v":1}` {
		t.Fatalf("echo = %+v", e)
	}
	if !h.sess.UsesServer("a") || h.sess.UsesServer("b") {
		t.Fatalf("UsesServer a=%v b=%v", h.sess.UsesServer("a"), h.sess.UsesServer("b"))
	}
}

func TestSessionSetDir(t *testing.T) {
	p, sub := projectDirs(t)
	h := newHarness(t, twoServers(p, nil), sub)
	h.sess.SetDir(context.Background(), p)
	h.waitChanged(t, "tools")
	if got := h.tools(t); !slices.Equal(got, []string{"a_x", "a_y", "b_z"}) {
		t.Fatalf("tools = %v", got)
	}
	if got := h.sess.Info().Dir; got != rules.Normalize(p) {
		t.Fatalf("Info().Dir = %q", got)
	}
}

func TestSessionRefreshOnConfigChange(t *testing.T) {
	p, _ := projectDirs(t)
	cfg := twoServers(p, nil)
	h := newHarness(t, cfg, p)
	if got := h.tools(t); !slices.Equal(got, []string{"a_x", "a_y", "b_z"}) {
		t.Fatalf("tools = %v", got)
	}
	next := &config.Config{Version: 1, Servers: map[string]*config.Server{"b": cfg.Servers["b"]}}
	a := *cfg.Servers["a"]
	a.Disabled = true
	next.Servers["a"] = &a
	h.cfg.Store(next)
	if err := h.sess.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.waitChanged(t, "tools")
	if got := h.tools(t); !slices.Equal(got, []string{"b_z"}) {
		t.Fatalf("tools = %v", got)
	}
	if h.sess.UsesServer("a") {
		t.Fatal("session still uses a")
	}
}

func TestSessionDeadServer(t *testing.T) {
	p, _ := projectDirs(t)
	h := newHarness(t, twoServers(p, map[string]string{"FAKEMCP_EXIT_AFTER": "1"}), p)
	if res := h.call(t, "a_x"); res.IsError {
		t.Fatalf("first call failed: %s", text(res))
	}
	res := h.call(t, "a_x")
	if !res.IsError || !strings.Contains(text(res), `"a"`) {
		t.Fatalf("second call = IsError %v, %q", res.IsError, text(res))
	}
	if got := h.tools(t); !slices.Contains(got, "b_z") {
		t.Fatalf("tools = %v", got)
	}
	// The next call restarts a (once the manager noticed the crash).
	deadline := time.Now().Add(2 * time.Second)
	for {
		res := h.call(t, "a_x")
		if !res.IsError {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a not restarted: %s", text(res))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSessionPromptsResources(t *testing.T) {
	const uri = "demo://a/r"
	fs := fakemcp.New(fakemcp.Spec{Name: "a", Tools: []string{"x"}, Prompts: []string{"p"}})
	fs.AddResource(&mcp.Resource{Name: "r", URI: uri, MIMEType: "text/plain"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: "text/plain", Text: "a:r"}}}, nil
	})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return fs }, nil))
	t.Cleanup(ts.Close) // registered first, so it runs after the manager and session are closed

	h := newHarness(t, &config.Config{Version: 1, Servers: map[string]*config.Server{"a": {Type: "http", URL: ts.URL}}}, t.TempDir())
	ctx := context.Background()
	pl, err := h.cs.ListPrompts(ctx, nil)
	if err != nil || len(pl.Prompts) != 1 || pl.Prompts[0].Name != "a_p" {
		t.Fatalf("ListPrompts = %+v, %v", pl, err)
	}
	gp, err := h.cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "a_p", Arguments: map[string]string{"k": "v"}})
	if err != nil {
		t.Fatalf("GetPrompt: %v", err)
	}
	var e fakemcp.Echo
	if err := json.Unmarshal([]byte(gp.Messages[0].Content.(*mcp.TextContent).Text), &e); err != nil {
		t.Fatal(err)
	}
	if e.Server != "a" || string(e.Args) != `{"k":"v"}` {
		t.Fatalf("prompt echo = %+v", e)
	}
	rl, err := h.cs.ListResources(ctx, nil)
	if err != nil || len(rl.Resources) != 1 || rl.Resources[0].URI != uri {
		t.Fatalf("ListResources = %+v, %v", rl, err)
	}
	rr, err := h.cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if err != nil || rr.Contents[0].Text != "a:r" {
		t.Fatalf("ReadResource = %+v, %v", rr, err)
	}
}

func TestSessionRefreshDeadline(t *testing.T) {
	p := t.TempDir()
	slow := &config.Server{Type: "stdio", Command: "sh", Args: []string{"-c", "sleep 2; exec " + fakeBin},
		Env: map[string]string{"FAKEMCP_NAME": "slow", "FAKEMCP_TOOLS": "s"}}
	cfg := &config.Config{Version: 1, Servers: map[string]*config.Server{
		"fast": stdio(map[string]string{"FAKEMCP_NAME": "fast", "FAKEMCP_TOOLS": "f"}),
		"slow": slow,
	}}
	h := newHarness(t, &config.Config{Version: 1, Servers: map[string]*config.Server{}}, p)
	h.cfg.Store(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := h.sess.Refresh(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded Refresh = %v, want DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("bounded Refresh took %v", d)
	}
	if got := h.tools(t); !slices.Equal(got, []string{"fast_f"}) {
		t.Fatalf("tools after bounded Refresh = %v", got)
	}
	if err := h.sess.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := h.tools(t); !slices.Equal(got, []string{"fast_f", "slow_s"}) {
		t.Fatalf("tools after full Refresh = %v", got)
	}

	// A Refresh cut short keeps the items of servers it could not finish.
	done, cancelDone := context.WithCancel(context.Background())
	cancelDone()
	_ = h.sess.Refresh(done)
	if got := h.tools(t); !slices.Equal(got, []string{"fast_f", "slow_s"}) {
		t.Fatalf("tools after cancelled Refresh = %v", got)
	}
}

func serverState(m *downstream.Manager, name string) downstream.ServerStatus {
	for _, s := range m.Status() {
		if s.Name == name {
			return s
		}
	}
	return downstream.ServerStatus{Name: name, State: "stopped"}
}

// A perSession instance ends with its session, not after the idle timeout.
func TestSessionCloseStopsPerSession(t *testing.T) {
	per := stdio(map[string]string{"FAKEMCP_NAME": "per", "FAKEMCP_TOOLS": "x"})
	per.PerSession = true
	cfg := &config.Config{Version: 1, Servers: map[string]*config.Server{"per": per}}
	mgr := downstream.NewManager(downstream.Options{
		Lookup:      func(name string) (*config.Server, bool) { s, ok := cfg.Servers[name]; return s, ok },
		IdleTimeout: time.Hour,
	})
	t.Cleanup(mgr.Close)
	sess := NewSession(SessionInfo{ID: "s1", Dir: t.TempDir()}, func() *config.Config { return cfg }, mgr, nil)
	if err := sess.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := serverState(mgr, "per"); s.State != "ready" {
		t.Fatalf("status before Close = %+v", s)
	}
	sess.Close()
	if s := serverState(mgr, "per"); s.State != "stopped" || s.Sessions != 0 {
		t.Fatalf("status after Close = %+v", s)
	}
}

// After a crash, every explicit tool call tries a start, Refreshes respect the backoff.
func TestToolCallBypassesBackoff(t *testing.T) {
	dir := t.TempDir()
	starts, ok := filepath.Join(dir, "starts"), filepath.Join(dir, "ok")
	// Serves once (then crashes on its second tool call), every later start fails.
	srv := &config.Server{Type: "stdio", Command: "sh",
		Args: []string{"-c", `echo start >> "$STARTS"; [ -e "$OK" ] && exit 1; touch "$OK"; exec "$FAKE"`},
		Env:  map[string]string{"STARTS": starts, "OK": ok, "FAKE": fakeBin, "FAKEMCP_NAME": "a", "FAKEMCP_TOOLS": "x", "FAKEMCP_EXIT_AFTER": "1"}}
	h := newHarness(t, &config.Config{Version: 1, Servers: map[string]*config.Server{"a": srv}}, t.TempDir())
	count := func() int { b, _ := os.ReadFile(starts); return strings.Count(string(b), "start") }
	h.call(t, "a_x")
	h.call(t, "a_x") // crashes the server
	deadline := time.Now().Add(2 * time.Second)
	for serverState(h.mgr, "a").State != "error" {
		if time.Now().After(deadline) {
			t.Fatalf("crash not noticed: %+v", serverState(h.mgr, "a"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if res := h.call(t, "a_x"); !res.IsError { // start 2 fails and opens the backoff window
		t.Fatalf("call succeeded: %s", text(res))
	}
	if res := h.call(t, "a_x"); !res.IsError || strings.Contains(text(res), "backoff") {
		t.Fatalf("call in backoff = %q, want a fresh start failure", text(res))
	}
	if n := count(); n != 3 {
		t.Fatalf("starts after explicit calls = %d, want 3", n)
	}
	ctx := context.Background()
	_ = h.sess.Refresh(ctx) // inside the window: no start, the server's tools are dropped
	_ = h.sess.Refresh(ctx)
	if n := count(); n != 3 {
		t.Fatalf("starts after refreshes = %d, want 3", n)
	}
}
