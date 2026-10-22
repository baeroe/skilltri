package downstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/secrets"
	"github.com/baeroe/skilltri/internal/testutil/fakemcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zalando/go-keyring"
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

// servers is a mutable config for Options.Lookup.
type servers struct {
	mu sync.Mutex
	m  map[string]*config.Server
}

func (s *servers) lookup(name string) (*config.Server, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	srv, ok := s.m[name]
	return srv, ok
}

func stdio(env map[string]string) *config.Server {
	return &config.Server{Type: "stdio", Command: fakeBin, Env: env}
}

func newManager(t *testing.T, m map[string]*config.Server, mod func(*Options)) *Manager {
	t.Helper()
	s := &servers{m: m}
	o := Options{Lookup: s.lookup, Expand: func(v string) (string, error) { return v, nil }}
	if mod != nil {
		mod(&o)
	}
	mgr := NewManager(o)
	t.Cleanup(mgr.Close)
	return mgr
}

func toolNames(t *testing.T, c *Conn) []string {
	t.Helper()
	tools, err := c.Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	var names []string
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	return names
}

func callEcho(t *testing.T, c *Conn, tool, args string) fakemcp.Echo {
	t.Helper()
	res, err := c.CallTool(context.Background(), tool, json.RawMessage(args))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	var e fakemcp.Echo
	if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &e); err != nil {
		t.Fatalf("echo: %v", err)
	}
	return e
}

func status(m *Manager, name string) ServerStatus {
	for _, s := range m.Status() {
		if s.Name == name {
			return s
		}
	}
	return ServerStatus{Name: name, State: "stopped"}
}

// waitState polls Status until server reaches state or the deadline passes.
func waitState(t *testing.T, m *Manager, name, state string) ServerStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		s := status(m, name)
		if s.State == state || time.Now().After(deadline) {
			if s.State != state {
				t.Fatalf("state of %s = %q, want %q (%+v)", name, s.State, state, s)
			}
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStdioToolsAndCall(t *testing.T) {
	m := newManager(t, map[string]*config.Server{"a": stdio(map[string]string{"FAKEMCP_NAME": "a", "FAKEMCP_TOOLS": "x,y"})}, nil)
	c, err := m.Acquire(context.Background(), "a", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got := toolNames(t, c); !slices.Equal(got, []string{"x", "y"}) {
		t.Fatalf("tools = %v", got)
	}
	e := callEcho(t, c, "x", `{"v":1}`)
	if e.Server != "a" || string(e.Args) != `{"v":1}` {
		t.Fatalf("echo = %+v", e)
	}
	s := status(m, "a")
	if s.State != "ready" || s.Tools != 2 || s.Sessions != 1 || s.LastCall.IsZero() {
		t.Fatalf("status = %+v", s)
	}
	if len(s.StderrTail) == 0 || !strings.Contains(s.StderrTail[0], "fakemcp a started") {
		t.Fatalf("stderr tail = %q", s.StderrTail)
	}
}

func TestStdioEnvCwdAndPromptsResources(t *testing.T) {
	dir := t.TempDir()
	cwdFile := filepath.Join(dir, "cwd")
	srv := stdio(map[string]string{"FAKEMCP_NAME": "${secret:n}", "FAKEMCP_PROMPTS": "p", "FAKEMCP_RESOURCES": "r", "FAKEMCP_CWD_FILE": cwdFile})
	srv.Cwd = dir
	m := newManager(t, map[string]*config.Server{"a": srv}, func(o *Options) {
		o.Expand = func(v string) (string, error) { return strings.ReplaceAll(v, "${secret:n}", "named"), nil }
	})
	c, err := m.Acquire(context.Background(), "a", "s1")
	if err != nil {
		t.Fatal(err)
	}
	wd, _ := os.ReadFile(cwdFile)
	got, _ := filepath.EvalSymlinks(string(wd))
	if want, _ := filepath.EvalSymlinks(dir); got != want {
		t.Fatalf("cwd = %q, want %q", wd, want)
	}
	ps, err := c.Prompts(context.Background())
	if err != nil || len(ps) != 1 || ps[0].Name != "p" {
		t.Fatalf("prompts = %v, %v", ps, err)
	}
	pr, err := c.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "p"})
	if err != nil || !strings.Contains(pr.Messages[0].Content.(*mcp.TextContent).Text, `"server":"named"`) {
		t.Fatalf("prompt = %+v, %v", pr, err)
	}
	rs, err := c.Resources(context.Background())
	if err != nil || len(rs) != 1 || rs[0].URI != fakemcp.ResourceURI("r") {
		t.Fatalf("resources = %v, %v", rs, err)
	}
	rr, err := c.ReadResource(context.Background(), fakemcp.ResourceURI("r"))
	if err != nil || rr.Contents[0].Text != "named:r" {
		t.Fatalf("read = %+v, %v", rr, err)
	}
}

func TestHTTPWithHeaders(t *testing.T) {
	keyring.MockInit()
	if err := secrets.Set("t", "Bearer abc"); err != nil {
		t.Fatal(err)
	}
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return fakemcp.New(fakemcp.Spec{Name: "h", Tools: []string{"x"}})
	}, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer abc" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close) // registered before the manager, so it runs after Manager.Close

	srv := &config.Server{Type: "http", URL: ts.URL, Headers: map[string]string{"Authorization": "${secret:t}"}}
	m := newManager(t, map[string]*config.Server{"h": srv}, func(o *Options) { o.Expand = secrets.Expand })
	c, err := m.Acquire(context.Background(), "h", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got := toolNames(t, c); !slices.Equal(got, []string{"x"}) {
		t.Fatalf("tools = %v", got)
	}
	m.Close()

	srv.Headers["Authorization"] = "Bearer wrong"
	m2 := newManager(t, map[string]*config.Server{"h": srv}, nil)
	if _, err := m2.Acquire(context.Background(), "h", "s1"); err == nil {
		t.Fatal("Acquire with wrong header succeeded")
	}
}

func TestSharedVsPerSession(t *testing.T) {
	env := map[string]string{"FAKEMCP_TOOLS": "x"}
	shared := stdio(env)
	per := stdio(env)
	per.PerSession = true
	m := newManager(t, map[string]*config.Server{"shared": shared, "per": per}, nil)
	ctx := context.Background()

	c1, err1 := m.Acquire(ctx, "shared", "s1")
	c2, err2 := m.Acquire(ctx, "shared", "s2")
	if err1 != nil || err2 != nil || c1 != c2 {
		t.Fatalf("shared: %p %p %v %v", c1, c2, err1, err2)
	}
	if s := status(m, "shared"); s.Sessions != 2 {
		t.Fatalf("sessions = %d", s.Sessions)
	}

	p1, err1 := m.Acquire(ctx, "per", "s1")
	p2, err2 := m.Acquire(ctx, "per", "s2")
	if err1 != nil || err2 != nil || p1 == p2 {
		t.Fatalf("per session: %p %p %v %v", p1, p2, err1, err2)
	}
	if a, b := callEcho(t, p1, "x", `{}`).PID, callEcho(t, p2, "x", `{}`).PID; a == b {
		t.Fatalf("per-session conns share pid %d", a)
	}
}

func TestConcurrentAcquireStartsOnce(t *testing.T) {
	m := newManager(t, map[string]*config.Server{"a": stdio(map[string]string{"FAKEMCP_TOOLS": "x"})}, nil)
	var wg sync.WaitGroup
	conns := make([]*Conn, 10)
	for i := range conns {
		wg.Go(func() {
			c, err := m.Acquire(context.Background(), "a", fmt.Sprintf("s%d", i))
			if err != nil {
				t.Error(err)
			}
			conns[i] = c
		})
	}
	wg.Wait()
	for _, c := range conns {
		if c != conns[0] {
			t.Fatal("concurrent Acquire returned different conns")
		}
	}
}

func TestIdleStop(t *testing.T) {
	m := newManager(t, map[string]*config.Server{"a": stdio(map[string]string{"FAKEMCP_TOOLS": "x"})},
		func(o *Options) { o.IdleTimeout = 100 * time.Millisecond })
	ctx := context.Background()
	c1, err := m.Acquire(ctx, "a", "s1")
	if err != nil {
		t.Fatal(err)
	}
	pid := callEcho(t, c1, "x", `{}`).PID
	m.Release("a", "s1")
	if s := waitState(t, m, "a", "stopped"); s.Sessions != 0 {
		t.Fatalf("status after idle = %+v", s)
	}
	if _, err := c1.CallTool(ctx, "x", nil); err == nil {
		t.Fatal("call on stopped conn succeeded")
	}
	c2, err := m.Acquire(ctx, "a", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got := callEcho(t, c2, "x", `{}`).PID; got == pid {
		t.Fatalf("restarted server has old pid %d", pid)
	}
}

func TestRequestTimeout(t *testing.T) {
	srv := stdio(map[string]string{"FAKEMCP_SLOW": "slow:2000"})
	srv.RequestTimeoutMs = 200
	m := newManager(t, map[string]*config.Server{"a": srv}, nil)
	c, err := m.Acquire(context.Background(), "a", "s1")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = c.CallTool(context.Background(), "slow", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("err = %v, want timeout", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("timeout took %v", d)
	}
}

func TestStartFailure(t *testing.T) {
	m := newManager(t, map[string]*config.Server{"a": {Type: "stdio", Command: "/nonexistent"}}, nil)
	if _, err := m.Acquire(context.Background(), "a", "s1"); err == nil {
		t.Fatal("Acquire succeeded")
	}
	if s := status(m, "a"); s.State != "error" || !strings.Contains(s.Error, "/nonexistent") {
		t.Fatalf("status = %+v", s)
	}
}

func TestUnknownAndDisabled(t *testing.T) {
	m := newManager(t, map[string]*config.Server{"off": {Type: "stdio", Command: fakeBin, Disabled: true}}, nil)
	for _, name := range []string{"missing", "off"} {
		if _, err := m.Acquire(context.Background(), name, "s1"); err == nil {
			t.Fatalf("Acquire(%s) succeeded", name)
		}
	}
}

func TestCrashRestart(t *testing.T) {
	m := newManager(t, map[string]*config.Server{"a": stdio(map[string]string{"FAKEMCP_TOOLS": "x", "FAKEMCP_EXIT_AFTER": "1"})}, nil)
	ctx := context.Background()
	c, err := m.Acquire(ctx, "a", "s1")
	if err != nil {
		t.Fatal(err)
	}
	pid := callEcho(t, c, "x", `{"n":1}`).PID
	if _, err := c.CallTool(ctx, "x", json.RawMessage(`{"n":2}`)); err == nil {
		t.Fatal("second call succeeded")
	}
	s := waitState(t, m, "a", "error")
	if s.Error == "" || !slices.ContainsFunc(s.StderrTail, func(l string) bool { return strings.Contains(l, "crashing after 1 tool calls") }) {
		t.Fatalf("status = %+v", s)
	}
	if _, err := c.CallTool(ctx, "x", nil); err == nil {
		t.Fatal("call on dead conn succeeded")
	}
	c2, err := m.Acquire(ctx, "a", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if c2 == c {
		t.Fatal("Acquire returned the dead conn")
	}
	if got := callEcho(t, c2, "x", `{"n":3}`).PID; got == pid {
		t.Fatalf("fresh process has old pid %d", pid)
	}
	if s := status(m, "a"); s.State != "ready" || s.Error != "" {
		t.Fatalf("status after restart = %+v", s)
	}
}

func TestRestartAndConnKeyChange(t *testing.T) {
	cfg := &servers{m: map[string]*config.Server{"a": stdio(map[string]string{"FAKEMCP_TOOLS": "x"})}}
	m := NewManager(Options{Lookup: cfg.lookup, Expand: func(v string) (string, error) { return v, nil }})
	defer m.Close()
	ctx := context.Background()
	c1, err := m.Acquire(ctx, "a", "s1")
	if err != nil {
		t.Fatal(err)
	}
	m.Restart("a")
	if _, err := c1.CallTool(ctx, "x", nil); err == nil {
		t.Fatal("call on restarted conn succeeded")
	}
	c2, err := m.Acquire(ctx, "a", "s1")
	if err != nil || c2 == c1 {
		t.Fatalf("after Restart: %p %p %v", c1, c2, err)
	}

	cfg.mu.Lock()
	cfg.m["a"] = stdio(map[string]string{"FAKEMCP_TOOLS": "x,y"})
	cfg.mu.Unlock()
	c3, err := m.Acquire(ctx, "a", "s1")
	if err != nil || c3 == c2 {
		t.Fatalf("after ConnKey change: %p %p %v", c2, c3, err)
	}
	if got := toolNames(t, c3); !slices.Equal(got, []string{"x", "y"}) {
		t.Fatalf("tools = %v", got)
	}
}

func TestListChanged(t *testing.T) {
	fs := fakemcp.New(fakemcp.Spec{Name: "a", Tools: []string{"x"}})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return fs }, nil)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close) // registered before the manager, so it runs after Manager.Close

	changed := make(chan string, 10)
	m := newManager(t, map[string]*config.Server{"a": {Type: "http", URL: ts.URL}},
		func(o *Options) { o.OnListChanged = func(s string) { changed <- s } })
	c, err := m.Acquire(context.Background(), "a", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got := toolNames(t, c); !slices.Equal(got, []string{"x"}) {
		t.Fatalf("tools = %v", got)
	}
	fakemcp.AddEchoTool(fs, "a", "z", 0)
	select {
	case s := <-changed:
		if s != "a" {
			t.Fatalf("OnListChanged(%q)", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnListChanged not called")
	}
	if got := toolNames(t, c); !slices.Equal(got, []string{"x", "z"}) {
		t.Fatalf("tools after change = %v", got)
	}
}

func TestClosedManager(t *testing.T) {
	m := newManager(t, map[string]*config.Server{"a": stdio(nil)}, nil)
	m.Close()
	if _, err := m.Acquire(context.Background(), "a", "s1"); err == nil {
		t.Fatal("Acquire after Close succeeded")
	}
}

func TestCloseLeavesNoGoroutines(t *testing.T) {
	before := runtime.NumGoroutine()
	cfg := &servers{m: map[string]*config.Server{
		"a": stdio(map[string]string{"FAKEMCP_TOOLS": "x"}),
		"b": stdio(map[string]string{"FAKEMCP_TOOLS": "x", "FAKEMCP_EXIT_AFTER": "0"}),
	}}
	m := NewManager(Options{Lookup: cfg.lookup, IdleTimeout: time.Hour})
	for _, name := range []string{"a", "b"} {
		c, err := m.Acquire(context.Background(), name, "s1")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = c.CallTool(context.Background(), "x", nil)
	}
	m.Release("a", "s1")
	m.Close()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			t.Fatalf("goroutines: %d before, %d after Close\n%s", before, runtime.NumGoroutine(), buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRestartAndCloseAbortInFlightCalls(t *testing.T) {
	for _, stop := range []string{"restart", "close"} {
		t.Run(stop, func(t *testing.T) {
			m := newManager(t, map[string]*config.Server{"a": stdio(map[string]string{"FAKEMCP_SLOW": "slow:5000"})}, nil)
			c, err := m.Acquire(context.Background(), "a", "s1")
			if err != nil {
				t.Fatal(err)
			}
			callErr := make(chan error, 1)
			go func() {
				_, err := c.CallTool(context.Background(), "slow", json.RawMessage(`{}`))
				callErr <- err
			}()
			time.Sleep(100 * time.Millisecond) // let the call reach the server
			start := time.Now()
			if stop == "restart" {
				m.Restart("a")
			} else {
				m.Close()
			}
			if d := time.Since(start); d > time.Second {
				t.Fatalf("%s took %v", stop, d)
			}
			select {
			case err := <-callErr:
				if err == nil {
					t.Fatal("in-flight call succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("in-flight call still running")
			}
		})
	}
}

func TestFailedAcquireLeavesNoClaim(t *testing.T) {
	m := newManager(t, map[string]*config.Server{"a": {Type: "stdio", Command: "/nonexistent"}}, nil)
	if _, err := m.Acquire(context.Background(), "a", "s1"); err == nil {
		t.Fatal("Acquire succeeded")
	}
	if s := status(m, "a"); s.Sessions != 0 {
		t.Fatalf("sessions = %d after failed Acquire", s.Sessions)
	}
}

func TestPerSessionStopsOnReleaseWithoutIdleTimeout(t *testing.T) {
	per := stdio(map[string]string{"FAKEMCP_TOOLS": "x"})
	per.PerSession = true
	m := newManager(t, map[string]*config.Server{"per": per}, nil)
	c, err := m.Acquire(context.Background(), "per", "s1")
	if err != nil {
		t.Fatal(err)
	}
	m.Release("per", "s1")
	if s := status(m, "per"); s.State != "stopped" {
		t.Fatalf("status = %+v", s)
	}
	if _, err := c.CallTool(context.Background(), "x", nil); err == nil {
		t.Fatal("call on released per-session conn succeeded")
	}
}
