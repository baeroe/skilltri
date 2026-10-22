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

// failing is a stdio server that records every start in a file and exits at once.
func failing(t *testing.T) (*config.Server, func() int) {
	t.Helper()
	f := filepath.Join(t.TempDir(), "starts")
	srv := &config.Server{Type: "stdio", Command: "sh", Args: []string{"-c", `echo start >> "$STARTS"; echo "demo failure" >&2; exit 1`},
		Env: map[string]string{"STARTS": f}}
	return srv, func() int {
		b, _ := os.ReadFile(f)
		return strings.Count(string(b), "start")
	}
}

func TestBackoffDelay(t *testing.T) {
	for _, tc := range []struct {
		fails int
		want  time.Duration
	}{
		{0, 0}, {1, 30 * time.Second}, {2, time.Minute}, {3, 2 * time.Minute}, {4, 4 * time.Minute},
		{5, 8 * time.Minute}, {6, 10 * time.Minute}, {40, 10 * time.Minute},
	} {
		if got := backoffDelay(30*time.Second, 10*time.Minute, tc.fails); got != tc.want {
			t.Errorf("backoffDelay(%d) = %v, want %v", tc.fails, got, tc.want)
		}
	}
}

func TestStartBackoff(t *testing.T) {
	srv, starts := failing(t)
	m := newManager(t, map[string]*config.Server{"a": srv}, func(o *Options) { o.Backoff = time.Hour })
	ctx := context.Background()
	if _, err := m.Acquire(ctx, "a", "s1"); err == nil {
		t.Fatal("Acquire succeeded")
	}
	if n := starts(); n != 1 {
		t.Fatalf("starts = %d, want 1", n)
	}
	// Refresh and retry Acquires within the backoff window get the recorded error, no new process.
	for range 3 {
		start := time.Now()
		_, err := m.Acquire(ctx, "a", "s1")
		if err == nil || !strings.Contains(err.Error(), "start a") || !strings.Contains(err.Error(), "backoff") {
			t.Fatalf("Acquire in backoff = %v", err)
		}
		if d := time.Since(start); d > 100*time.Millisecond {
			t.Fatalf("Acquire in backoff took %v", d)
		}
	}
	if n := starts(); n != 1 {
		t.Fatalf("starts in backoff = %d, want 1", n)
	}
	if s := status(m, "a"); s.State != "error" || s.Sessions != 0 || !slices.Contains(s.StderrTail, "demo failure") {
		t.Fatalf("status = %+v", s)
	}
	// An explicit call tries once more.
	if _, err := m.Acquire(ctx, "a", "s1", BypassBackoff()); err == nil || strings.Contains(err.Error(), "backoff") {
		t.Fatalf("bypassing Acquire = %v, want a fresh start failure", err)
	}
	if n := starts(); n != 2 {
		t.Fatalf("starts after bypass = %d, want 2", n)
	}
	if _, err := m.Acquire(ctx, "a", "s1"); err == nil || !strings.Contains(err.Error(), "backoff") {
		t.Fatalf("Acquire after bypass = %v", err)
	}
	if n := starts(); n != 2 {
		t.Fatalf("starts = %d, want 2", n)
	}
}

func TestBackoffGrowsAndExpires(t *testing.T) {
	srv, starts := failing(t)
	m := newManager(t, map[string]*config.Server{"a": srv}, func(o *Options) { o.Backoff = 200 * time.Millisecond })
	ctx := context.Background()
	acquire := func() { _, _ = m.Acquire(ctx, "a", "s1") }
	acquire()
	acquire()
	if n := starts(); n != 1 {
		t.Fatalf("starts = %d, want 1", n)
	}
	time.Sleep(300 * time.Millisecond) // past the first window (200ms)
	acquire()
	if n := starts(); n != 2 {
		t.Fatalf("starts after the first window = %d, want 2", n)
	}
	time.Sleep(300 * time.Millisecond) // inside the second window (400ms)
	acquire()
	if n := starts(); n != 2 {
		t.Fatalf("starts inside the second window = %d, want 2", n)
	}
	time.Sleep(200 * time.Millisecond)
	acquire()
	if n := starts(); n != 3 {
		t.Fatalf("starts after the second window = %d, want 3", n)
	}
}

func TestBackoffEndsOnConfigChangeAndRestart(t *testing.T) {
	srv, _ := failing(t)
	cfg := &servers{m: map[string]*config.Server{"a": srv, "b": srv}}
	m := NewManager(Options{Lookup: cfg.lookup, Backoff: time.Hour})
	defer m.Close()
	ctx := context.Background()
	for _, name := range []string{"a", "b"} {
		if _, err := m.Acquire(ctx, name, "s1"); err == nil {
			t.Fatalf("Acquire %s succeeded", name)
		}
	}
	cfg.mu.Lock()
	cfg.m["a"] = stdio(map[string]string{"FAKEMCP_TOOLS": "x"})
	cfg.mu.Unlock()
	if _, err := m.Acquire(ctx, "a", "s1"); err != nil {
		t.Fatalf("Acquire after a config fix: %v", err)
	}
	m.Restart("b")
	if _, err := m.Acquire(ctx, "b", "s1"); err == nil || strings.Contains(err.Error(), "backoff") {
		t.Fatalf("Acquire after Restart = %v, want a fresh start failure", err)
	}
}

func TestBackoffResetBySuccess(t *testing.T) {
	dir := t.TempDir()
	flag := filepath.Join(dir, "ok")
	// Fails until the flag file exists.
	srv := &config.Server{Type: "stdio", Command: "sh", Args: []string{"-c", `[ -e "$FLAG" ] || exit 1; exec "$FAKE"`},
		Env: map[string]string{"FLAG": flag, "FAKE": fakeBin, "FAKEMCP_TOOLS": "x"}}
	m := newManager(t, map[string]*config.Server{"a": srv}, func(o *Options) { o.Backoff = time.Hour })
	ctx := context.Background()
	if _, err := m.Acquire(ctx, "a", "s1"); err == nil {
		t.Fatal("Acquire succeeded")
	}
	if err := os.WriteFile(flag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := m.Acquire(ctx, "a", "s1", BypassBackoff())
	if err != nil {
		t.Fatalf("bypassing Acquire: %v", err)
	}
	m.Restart("a") // the next start must not be held back by the old failure
	if _, err := c.CallTool(ctx, "x", nil); err == nil {
		t.Fatal("call on restarted conn succeeded")
	}
	if _, err := m.Acquire(ctx, "a", "s1"); err != nil {
		t.Fatalf("Acquire after a successful start: %v", err)
	}
}

func TestReleaseSessionStopsPerSessionAtOnce(t *testing.T) {
	per := stdio(map[string]string{"FAKEMCP_TOOLS": "x"})
	per.PerSession = true
	m := newManager(t, map[string]*config.Server{"per": per, "shared": stdio(map[string]string{"FAKEMCP_TOOLS": "x"})},
		func(o *Options) { o.IdleTimeout = time.Hour })
	ctx := context.Background()
	pc, err := m.Acquire(ctx, "per", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Acquire(ctx, "per", "s2"); err != nil {
		t.Fatal(err)
	}
	sc, err := m.Acquire(ctx, "shared", "s1")
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseSession("s1")
	if _, err := pc.CallTool(ctx, "x", nil); err == nil {
		t.Fatal("per-session conn of the ended session still works")
	}
	if s := status(m, "per"); s.State != "ready" || s.Sessions != 1 {
		t.Fatalf("per status = %+v (s2's instance must stay)", s)
	}
	if s := status(m, "shared"); s.State != "ready" || s.Sessions != 0 {
		t.Fatalf("shared status = %+v (the idle timer keeps it)", s)
	}
	if _, err := sc.CallTool(ctx, "x", nil); err != nil {
		t.Fatalf("shared conn stopped: %v", err)
	}
}

func TestRequestTimeoutChangeAppliesWithoutRestart(t *testing.T) {
	srv := stdio(map[string]string{"FAKEMCP_SLOW": "slow:2000"})
	srv.RequestTimeoutMs = 10000
	cfg := &servers{m: map[string]*config.Server{"a": srv}}
	m := NewManager(Options{Lookup: cfg.lookup})
	defer m.Close()
	c, err := m.Acquire(context.Background(), "a", "s1")
	if err != nil {
		t.Fatal(err)
	}
	next := *srv
	next.RequestTimeoutMs = 200
	cfg.mu.Lock()
	cfg.m["a"] = &next
	cfg.mu.Unlock()
	start := time.Now()
	_, err = c.CallTool(context.Background(), "slow", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "timeout after 200ms") {
		t.Fatalf("err = %v, want timeout after 200ms", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("timeout took %v", d)
	}
}

func TestStatusDropsRemovedServers(t *testing.T) {
	cfg := &servers{m: map[string]*config.Server{"a": stdio(map[string]string{"FAKEMCP_TOOLS": "x"}), "b": {Type: "stdio", Command: "/nonexistent"}}}
	m := NewManager(Options{Lookup: cfg.lookup})
	defer m.Close()
	_, _ = m.Acquire(context.Background(), "a", "s1")
	_, _ = m.Acquire(context.Background(), "b", "s1")
	cfg.mu.Lock()
	delete(cfg.m, "b")
	cfg.mu.Unlock()
	var names []string
	for _, s := range m.Status() {
		names = append(names, s.Name)
	}
	if !slices.Equal(names, []string{"a"}) {
		t.Fatalf("status names = %v, want [a]", names)
	}
}

// Servers without the prompts or resources capability are not asked for those lists.
func TestListsFollowCapabilities(t *testing.T) {
	fs := fakemcp.New(fakemcp.Spec{Name: "a", Tools: []string{"x"}})
	var mu sync.Mutex
	var methods []string
	fs.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			mu.Lock()
			methods = append(methods, method)
			mu.Unlock()
			return next(ctx, method, req)
		}
	})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return fs }, nil))
	t.Cleanup(ts.Close) // registered before the manager, so it runs after Manager.Close

	m := newManager(t, map[string]*config.Server{"a": {Type: "http", URL: ts.URL}}, nil)
	c, err := m.Acquire(context.Background(), "a", "s1")
	if err != nil {
		t.Fatal(err)
	}
	ps, err := c.Prompts(context.Background())
	if err != nil || len(ps) != 0 {
		t.Fatalf("prompts = %v, %v", ps, err)
	}
	rs, err := c.Resources(context.Background())
	if err != nil || len(rs) != 0 {
		t.Fatalf("resources = %v, %v", rs, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if slices.Contains(methods, "prompts/list") || slices.Contains(methods, "resources/list") {
		t.Fatalf("methods sent = %v", methods)
	}
}
