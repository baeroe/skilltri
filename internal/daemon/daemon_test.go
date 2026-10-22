package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/baeroe/skilltri/internal/config"
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

const poll = 50 * time.Millisecond

// env is a daemon's files in a short /tmp dir (macOS limits socket paths to 104 bytes).
type env struct {
	dir, cfg, sock, lock string
	connectRefresh       time.Duration
	tick                 time.Duration // Options.UpdateTick
	d                    **Daemon      // set by start
}

func newEnv(t *testing.T) env {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "st")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	isolateDirs(t, dir)
	return env{dir: dir, cfg: filepath.Join(dir, "config.json"), sock: filepath.Join(dir, "d.sock"), lock: filepath.Join(dir, "d.lock"), d: new(*Daemon)}
}

// isolateDirs points skilltri's config, state, data and cache dirs below dir, so the daemon's
// catalog, checkouts and views never reach the real ones.
func isolateDirs(t *testing.T, dir string) {
	t.Helper()
	for env, sub := range map[string]string{
		"SKILLTRI_CONFIG_DIR": "config",
		"SKILLTRI_STATE_DIR":  "state",
		"SKILLTRI_DATA_DIR":   "data",
		"SKILLTRI_CACHE_DIR":  "cache",
	} {
		t.Setenv(env, filepath.Join(dir, sub))
	}
}

func (e env) options() Options {
	return Options{ConfigPath: e.cfg, SocketPath: e.sock, LockPath: e.lock, Version: "test", PollInterval: poll, ConnectRefresh: e.connectRefresh, UpdateTick: e.tick}
}

// writeConfig writes c as the config file and makes sure its mtime moves forward.
func (e env) writeConfig(t *testing.T, c *config.Config) {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	e.writeRaw(t, string(b))
}

func (e env) writeRaw(t *testing.T, s string) {
	t.Helper()
	var old time.Time
	if fi, err := os.Stat(e.cfg); err == nil {
		old = fi.ModTime()
	}
	if err := os.WriteFile(e.cfg, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(e.cfg); err == nil && !fi.ModTime().After(old) {
		later := old.Add(time.Second)
		_ = os.Chtimes(e.cfg, later, later)
	}
}

// start runs a daemon until the test ends and waits until it answers on the socket.
func (e env) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	d := New(e.options())
	*e.d = d
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Run did not return after cancel")
		}
		if _, err := os.Stat(e.sock); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("socket left behind: %v", err)
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		var st StatusReply
		err := Control(e.sock, Hello{Kind: "control", Cmd: "status"}, &st)
		if err == nil {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("Run returned early: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon not reachable: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (e env) status(t *testing.T) StatusReply {
	t.Helper()
	var st StatusReply
	if err := Control(e.sock, Hello{V: 1, Kind: "control", Cmd: "status"}, &st); err != nil {
		t.Fatalf("status: %v", err)
	}
	return st
}

func fake(name, tools string) *config.Server {
	return &config.Server{Type: "stdio", Command: fakeBin, Env: map[string]string{"FAKEMCP_NAME": name, "FAKEMCP_TOOLS": tools}}
}

func cfgOf(servers map[string]*config.Server) *config.Config {
	return &config.Config{Version: 1, Servers: servers}
}

// client is a harness session over the daemon socket.
type client struct {
	cs      *mcp.ClientSession
	changed chan struct{}
}

func (e env) connect(t *testing.T, h Hello, roots ...string) *client {
	t.Helper()
	return e.connectAs(t, "test-harness", h, roots...)
}

// connectAs connects a client named clientName; without roots it does not advertise roots.
func (e env) connectAs(t *testing.T, clientName string, h Hello, roots ...string) *client {
	t.Helper()
	conn, err := net.Dial("unix", e.sock)
	if err != nil {
		t.Fatal(err)
	}
	h.V, h.Kind = 1, "session"
	b, _ := json.Marshal(h)
	if _, err := conn.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
	c := &client{changed: make(chan struct{}, 16)}
	opts := &mcp.ClientOptions{ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { c.changed <- struct{}{} }}
	if len(roots) == 0 {
		opts.Capabilities = &mcp.ClientCapabilities{}
	}
	mc := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: "0"}, opts)
	for _, r := range roots {
		mc.AddRoots(&mcp.Root{URI: "file://" + r})
	}
	// The client probes server/discover (2026-07-28) first and falls back to initialize.
	c.cs, err = mc.Connect(context.Background(), &mcp.IOTransport{Reader: conn, Writer: conn}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.cs.Close() })
	return c
}

// tools lists the downstream tools, without the skill fallback's skilltri_ tools.
func (c *client) tools(t *testing.T) []string {
	t.Helper()
	return slices.DeleteFunc(c.allTools(t), func(n string) bool { return strings.HasPrefix(n, "skilltri_") })
}

func (c *client) allTools(t *testing.T) []string {
	t.Helper()
	res, err := c.cs.ListTools(context.Background(), nil)
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

func (c *client) pid(t *testing.T, tool string) int {
	t.Helper()
	res, err := c.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("CallTool %s: %v", tool, err)
	}
	if res.IsError || len(res.Content) == 0 {
		t.Fatalf("CallTool %s: error result %+v", tool, res.Content)
	}
	var echo fakemcp.Echo
	if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &echo); err != nil {
		t.Fatal(err)
	}
	return echo.PID
}

// eventually polls cond until it holds or the timeout passes.
func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func projectDirs(t *testing.T) (p, sub string) {
	t.Helper()
	p = rules.Normalize(t.TempDir())
	sub = filepath.Join(p, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	return p, sub
}

func TestSingleInstance(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	o := e.options()
	o.SocketPath = filepath.Join(e.dir, "other.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := New(o).Run(ctx); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Run = %v, want ErrAlreadyRunning", err)
	}
	e.status(t) // the first daemon is unaffected
}

func TestStaleSocket(t *testing.T) {
	e := newEnv(t)
	if err := os.WriteFile(e.sock, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.start(t)
	fi, err := os.Stat(e.sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v, want socket 0600", fi.Mode())
	}
}

func TestSessionOverSocket(t *testing.T) {
	e := newEnv(t)
	p, sub := projectDirs(t)
	b := fake("b", "z")
	b.Rules = []rules.Rule{{Path: p, Mode: rules.Only}}
	e.writeConfig(t, cfgOf(map[string]*config.Server{"a": fake("a", "x,y"), "b": b}))
	e.start(t)

	if got := e.connect(t, Hello{Dir: p}).tools(t); !slices.Equal(got, []string{"a_x", "a_y", "b_z"}) {
		t.Fatalf("tools in p = %v", got)
	}
	if got := e.connect(t, Hello{Dir: sub}).tools(t); !slices.Equal(got, []string{"a_x", "a_y"}) {
		t.Fatalf("tools in sub = %v", got)
	}
}

func TestRootsOverrideDir(t *testing.T) {
	e := newEnv(t)
	p, sub := projectDirs(t)
	b := fake("b", "z")
	b.Rules = []rules.Rule{{Path: p, Mode: rules.Only}}
	e.writeConfig(t, cfgOf(map[string]*config.Server{"b": b}))
	e.start(t)

	sessionDir := func(client string) func() string {
		return func() string {
			for _, s := range e.status(t).Sessions {
				if s.ClientName == client {
					return s.Dir
				}
			}
			return ""
		}
	}

	c := e.connect(t, Hello{Dir: sub}, p)
	select {
	case <-c.changed:
	case <-time.After(5 * time.Second):
		t.Fatal("no tools list_changed after the roots answer")
	}
	if got := sessionDir("test-harness")(); got != p {
		t.Fatalf("session dir = %q, want root %q", got, p)
	}
	if got := c.tools(t); !slices.Equal(got, []string{"b_z"}) {
		t.Fatalf("tools after roots = %v", got)
	}
	c.cs.Close()
	eventually(t, 5*time.Second, "closed session to leave status", func() bool { return len(e.status(t).Sessions) == 0 })

	c = e.connect(t, Hello{Dir: sub, DirExplicit: true}, p)
	time.Sleep(300 * time.Millisecond)
	if got := sessionDir("test-harness")(); got != sub {
		t.Fatalf("explicit session dir = %q, want %q", got, sub)
	}
	if got := c.tools(t); len(got) != 0 {
		t.Fatalf("explicit tools = %v", got)
	}
}

func TestConfigReload(t *testing.T) {
	e := newEnv(t)
	p, _ := projectDirs(t)
	a := fake("a", "x")
	e.writeConfig(t, cfgOf(map[string]*config.Server{"a": a}))
	e.start(t)
	c := e.connect(t, Hello{Dir: p})
	if got := c.tools(t); !slices.Equal(got, []string{"a_x"}) {
		t.Fatalf("tools = %v", got)
	}
	pid := c.pid(t, "a_x")

	e.writeConfig(t, cfgOf(map[string]*config.Server{"a": a, "b": fake("b", "z")}))
	eventually(t, 3*poll+2*time.Second, "b_z after adding b", func() bool { return slices.Contains(c.tools(t), "b_z") })

	a.Rules = []rules.Rule{{Path: p, Mode: rules.Tree}}
	e.writeConfig(t, cfgOf(map[string]*config.Server{"a": a, "b": fake("b", "z")}))
	time.Sleep(5 * poll)
	if got := c.pid(t, "a_x"); got != pid {
		t.Fatalf("rules change restarted a: pid %d → %d", pid, got)
	}

	a.Args = []string{"--changed"}
	e.writeConfig(t, cfgOf(map[string]*config.Server{"a": a, "b": fake("b", "z")}))
	eventually(t, 5*time.Second, "a restarted after args change", func() bool {
		res, err := c.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "a_x", Arguments: map[string]any{}})
		if err != nil || res.IsError {
			return false
		}
		var echo fakemcp.Echo
		_ = json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &echo)
		return echo.PID != 0 && echo.PID != pid
	})
}

func TestBrokenConfig(t *testing.T) {
	t.Run("after start", func(t *testing.T) {
		e := newEnv(t)
		p, _ := projectDirs(t)
		e.writeConfig(t, cfgOf(map[string]*config.Server{"a": fake("a", "x")}))
		e.start(t)
		c := e.connect(t, Hello{Dir: p})
		e.writeRaw(t, "{")
		eventually(t, 3*poll+2*time.Second, "ConfigError", func() bool { return e.status(t).ConfigError != "" })
		if got := c.tools(t); !slices.Equal(got, []string{"a_x"}) {
			t.Fatalf("tools after broken config = %v", got)
		}
		if got := c.pid(t, "a_x"); got == 0 {
			t.Fatal("no pid")
		}
		e.writeConfig(t, cfgOf(map[string]*config.Server{"a": fake("a", "x")}))
		eventually(t, 3*poll+2*time.Second, "ConfigError cleared", func() bool { return e.status(t).ConfigError == "" })
	})
	t.Run("at start", func(t *testing.T) {
		e := newEnv(t)
		e.writeRaw(t, "{")
		e.start(t)
		st := e.status(t)
		if st.ConfigError == "" || len(st.Servers) != 0 {
			t.Fatalf("status = %+v, want ConfigError and no servers", st)
		}
		p, _ := projectDirs(t)
		if got := e.connect(t, Hello{Dir: p}).tools(t); len(got) != 0 {
			t.Fatalf("tools = %v", got)
		}
	})
}

func TestControlStatusWhich(t *testing.T) {
	e := newEnv(t)
	p, sub := projectDirs(t)
	b := fake("b", "z")
	b.Rules = []rules.Rule{{Path: sub, Mode: rules.Only}}
	e.writeConfig(t, cfgOf(map[string]*config.Server{"a": fake("a", "x,y"), "b": b}))
	e.start(t)
	c := e.connect(t, Hello{Dir: p})
	c.tools(t)

	st := e.status(t)
	if st.PID != os.Getpid() || st.Version != "test" || st.ConfigError != "" {
		t.Fatalf("status = %+v", st)
	}
	if len(st.Sessions) != 1 || st.Sessions[0].Dir != p || st.Sessions[0].ID == "" || st.Sessions[0].ClientName != "test-harness" {
		t.Fatalf("sessions = %+v", st.Sessions)
	}
	states := map[string]string{}
	for _, s := range st.Servers {
		states[s.Name] = s.State
	}
	if states["a"] != "ready" || states["b"] != "stopped" {
		t.Fatalf("server states = %v", states)
	}

	var w WhichReply
	if err := Control(e.sock, Hello{Kind: "control", Cmd: "which", Args: []string{p}}, &w); err != nil {
		t.Fatal(err)
	}
	if w.Dir != p || len(w.Servers) != 2 {
		t.Fatalf("which = %+v", w)
	}
	wa, wb := w.Servers[0], w.Servers[1]
	if wa.Name != "a" || !wa.Decision.Active || wa.Decision.Reason == "" || wa.State != "ready" {
		t.Fatalf("which a = %+v", wa)
	}
	if len(wa.Tools) != 2 || wa.Tools[0].Name != "x" || wa.Tools[0].Exposed != "a_x" || !wa.Tools[0].Decision.Active {
		t.Fatalf("which a tools = %+v", wa.Tools)
	}
	if wb.Name != "b" || wb.Decision.Active || !strings.Contains(wb.Decision.Reason, "no include matches") || wb.Tools != nil {
		t.Fatalf("which b = %+v", wb)
	}

	if err := Control(e.sock, Hello{Kind: "control", Cmd: "nope"}, &w); err == nil {
		t.Fatal("unknown control command succeeded")
	}
}

func TestSlowServerDoesNotBlockInitialize(t *testing.T) {
	e := newEnv(t)
	e.connectRefresh = time.Second
	p, _ := projectDirs(t)
	slow := &config.Server{Type: "stdio", Command: "sh", Args: []string{"-c", "sleep 3; exec " + fakeBin},
		Env: map[string]string{"FAKEMCP_NAME": "slow", "FAKEMCP_TOOLS": "s"}}
	e.writeConfig(t, cfgOf(map[string]*config.Server{"fast": fake("fast", "f"), "slow": slow}))
	e.start(t)

	start := time.Now()
	c := e.connect(t, Hello{Dir: p})
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("initialize took %v with a slow server", d)
	}
	if got := c.tools(t); !slices.Equal(got, []string{"fast_f"}) {
		t.Fatalf("tools at connect = %v", got)
	}
	select {
	case <-c.changed:
	case <-time.After(8 * time.Second):
		t.Fatal("slow server's tools never announced")
	}
	if got := c.tools(t); !slices.Equal(got, []string{"fast_f", "slow_s"}) {
		t.Fatalf("tools later = %v", got)
	}
}

// raw is a socket connection driven by hand, one JSON line at a time.
type raw struct {
	conn net.Conn
	r    *bufio.Reader
}

func (e env) dialRaw(t *testing.T) *raw {
	t.Helper()
	conn, err := net.Dial("unix", e.sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	return &raw{conn: conn, r: bufio.NewReader(conn)}
}

func (r *raw) send(t *testing.T, lines ...string) {
	t.Helper()
	if _, err := r.conn.Write([]byte(strings.Join(lines, "\n") + "\n")); err != nil {
		t.Fatal(err)
	}
}

func (r *raw) recv(t *testing.T, v any) {
	t.Helper()
	line, err := r.r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := json.Unmarshal(line, v); err != nil {
		t.Fatalf("parse %q: %v", line, err)
	}
}

type rpcReply struct {
	ID     string `json:"id"`
	Result *struct {
		ProtocolVersion string `json:"protocolVersion"`
	} `json:"result"`
	Error *struct {
		Code int `json:"code"`
	} `json:"error"`
}

const (
	sessionHello = `{"v":1,"kind":"session","dir":"/"}`
	initialize   = `{"jsonrpc":"2.0","id":"i1","method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"raw","version":"0"}}}`
)

func TestDiscoverRejected(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	r := e.dialRaw(t)
	r.send(t, sessionHello)
	r.send(t, `{"jsonrpc":"2.0","id":"p1","method":"server/discover","params":{}}`)
	var rep rpcReply
	r.recv(t, &rep)
	if rep.ID != "p1" || rep.Error == nil || rep.Error.Code != -32601 {
		t.Fatalf("server/discover reply = %+v, want error -32601", rep)
	}
	r.send(t, initialize)
	rep = rpcReply{}
	r.recv(t, &rep)
	if rep.ID != "i1" || rep.Error != nil || rep.Result == nil || rep.Result.ProtocolVersion != "2025-11-25" {
		t.Fatalf("initialize reply = %+v", rep)
	}
}

func TestHelloAndInitializeInOneWrite(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	r := e.dialRaw(t)
	r.send(t, sessionHello, initialize)
	var rep rpcReply
	r.recv(t, &rep)
	if rep.ID != "i1" || rep.Result == nil || rep.Result.ProtocolVersion != "2025-11-25" {
		t.Fatalf("initialize reply = %+v", rep)
	}
}

func TestBadHelloVersion(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	r := e.dialRaw(t)
	r.send(t, `{"v":2,"kind":"session"}`)
	var resp struct{ Error string }
	r.recv(t, &resp)
	if !strings.Contains(resp.Error, "hello version 2") {
		t.Fatalf("reply = %+v", resp)
	}
}

func TestShutdownWithPendingHello(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	d := New(e.options())
	*e.d = d
	go func() { done <- d.Run(ctx) }()
	eventually(t, 5*time.Second, "daemon up", func() bool {
		return Control(e.sock, Hello{Cmd: "status"}, nil) == nil
	})
	conn, err := net.Dial("unix", e.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	time.Sleep(50 * time.Millisecond) // let the daemon accept it
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run waited for a connection that never sent its hello")
	}
}

// A daemon with Wait queues behind the one holding the lock and serves once it exits.
func TestWaitTakesOver(t *testing.T) {
	e := newEnv(t)
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	done1 := make(chan error, 1)
	go func() { done1 <- New(e.options()).Run(ctx1) }()
	eventually(t, 5*time.Second, "first daemon up", func() bool {
		return Control(e.sock, Hello{Cmd: "status"}, nil) == nil
	})

	o := e.options()
	o.Wait, o.Version = true, "second"
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	done2 := make(chan error, 1)
	go func() { done2 <- New(o).Run(ctx2) }()
	select {
	case err := <-done2:
		t.Fatalf("waiting daemon returned: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if st := e.status(t); st.Version != "test" {
		t.Fatalf("version = %q, want the first daemon", st.Version)
	}

	cancel1()
	if err := <-done1; err != nil {
		t.Fatalf("first Run: %v", err)
	}
	eventually(t, 5*time.Second, "second daemon serves", func() bool {
		var st StatusReply
		return Control(e.sock, Hello{Cmd: "status"}, &st) == nil && st.Version == "second"
	})
	cancel2()
	select {
	case err := <-done2:
		if err != nil {
			t.Fatalf("second Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second Run did not return")
	}
}

func TestWaitCancelled(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	o := e.options()
	o.Wait = true
	o.SocketPath = filepath.Join(e.dir, "other.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := New(o).Run(ctx); err != nil {
		t.Fatalf("cancelled waiting Run = %v, want nil", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("waiting Run took %v after cancel", d)
	}
	e.status(t) // the first daemon is unaffected
}

func TestStatusPath(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	if st := e.status(t); st.Path != os.Getenv("PATH") {
		t.Fatalf("status Path = %q, want %q", st.Path, os.Getenv("PATH"))
	}
}

func TestControlShutdown(t *testing.T) {
	e := newEnv(t)
	e.start(t) // its cleanup checks that Run returned nil and removed the socket
	if err := Control(e.sock, Hello{Cmd: "shutdown"}, nil); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	eventually(t, 5*time.Second, "socket removed", func() bool {
		_, err := os.Stat(e.sock)
		return errors.Is(err, os.ErrNotExist)
	})
}

// The skill fallback is decided by the client name once initialize ran, also for clients
// without roots; a client of a natively served harness loses the skill tools.
func TestSkillFallbackFollowsClient(t *testing.T) {
	e := newEnv(t)
	skills := t.TempDir()
	if err := os.MkdirAll(filepath.Join(skills, "alpha"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skills, "alpha", "SKILL.md"), []byte("---\nname: alpha\ndescription: Demo skill alpha.\n---\nBody.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := cfgOf(map[string]*config.Server{})
	c.Sources = map[string]*config.Source{"demo": {Type: "local", Path: skills}}
	e.writeConfig(t, c)
	e.start(t)
	has := func(cl *client) bool { return slices.Contains(cl.allTools(t), "skilltri_load_skill") }

	plain := e.connectAs(t, "claude-code", Hello{Dir: t.TempDir()})
	eventually(t, 5*time.Second, "skill tools without SkillsNative", func() bool { return has(plain) })

	native := e.connectAs(t, "claude-code", Hello{Dir: t.TempDir(), SkillsNative: []string{"claude-code"}})
	other := e.connectAs(t, "other-harness", Hello{Dir: t.TempDir(), SkillsNative: []string{"claude-code"}})
	eventually(t, 5*time.Second, "skill tools for a non-native client", func() bool { return has(other) })
	time.Sleep(200 * time.Millisecond) // the post-initialize Refresh of native has run by now
	if has(native) {
		t.Fatalf("native client lists the skill tools: %v", native.allTools(t))
	}
}

// "restart" stops the server's instances; the next session starts a fresh process. Unknown or
// missing server names are errors.
func TestControlRestart(t *testing.T) {
	e := newEnv(t)
	p, _ := projectDirs(t)
	e.writeConfig(t, cfgOf(map[string]*config.Server{"a": fake("a", "x")}))
	e.start(t)
	c := e.connect(t, Hello{Dir: p})
	pid := c.pid(t, "a_x")
	if err := Control(e.sock, Hello{Cmd: "restart", Args: []string{"a"}}, nil); err != nil {
		t.Fatalf("restart: %v", err)
	}
	eventually(t, 5*time.Second, "restarted server", func() bool {
		res, err := c.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "a_x", Arguments: map[string]any{}})
		if err != nil || res.IsError {
			return false
		}
		var echo fakemcp.Echo
		return json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &echo) == nil && echo.PID != pid
	})
	if err := Control(e.sock, Hello{Cmd: "restart", Args: []string{"nope"}}, nil); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("restart of an unknown server: %v", err)
	}
	if err := Control(e.sock, Hello{Cmd: "restart"}, nil); err == nil {
		t.Fatal("restart without a server succeeded")
	}
}

// TestControlContext: a cancelled context stops waiting for a daemon that does not answer; an
// answer is decoded as with Control.
func TestControlContext(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "st")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "c.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	answer := make(chan bool, 2)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				if _, err := bufio.NewReader(conn).ReadBytes('\n'); err != nil {
					return
				}
				if <-answer {
					conn.Write([]byte(`{"result":{"Sources":[{"name":"demo","commit":"abc"}]}}` + "\n"))
					return
				}
				conn.Read(make([]byte, 1)) // wait until the client hangs up
			}()
		}
	}()

	answer <- false
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ControlContext(ctx, sock, Hello{Cmd: "update"}, nil) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ControlContext did not return after cancel")
	}

	answer <- true
	var r UpdateReply
	if err := ControlContext(context.Background(), sock, Hello{Cmd: "update"}, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Sources) != 1 || r.Sources[0].Name != "demo" {
		t.Fatalf("reply = %+v", r)
	}
	if err := ControlContext(context.Background(), filepath.Join(dir, "missing.sock"), Hello{Cmd: "status"}, nil); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("missing socket: %v", err)
	}
}
