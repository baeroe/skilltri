package main

// End-to-end tests: the real skilltri binary runs `skilltri mcp` as a stdio subprocess, which starts
// a detached daemon. Every process gets temp config/state/data/cache dirs and a short socket path in
// /tmp, so the user's real config and daemon are never touched.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/daemon"
	"github.com/baeroe/skilltri/internal/rules"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var skilltriBin, fakeBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "skilltri-bin-")
	if err != nil {
		panic(err)
	}
	skilltriBin = filepath.Join(dir, "skilltri")
	fakeBin = filepath.Join(dir, "fakemcp")
	for bin, pkg := range map[string]string{skilltriBin: ".", fakeBin: "./internal/testutil/fakemcp/cmd/fakemcp"} {
		if out, err := exec.Command("go", "build", "-o", bin, pkg).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "go build %s failed: %v\n%s", pkg, err, out)
			os.Exit(1)
		}
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// sandbox is one isolated skilltri environment: its own dirs, socket and daemon.
type sandbox struct {
	sock, state string
	env         []string
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	short, err := os.MkdirTemp("/tmp", "ste") // macOS limits socket paths to 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	dir := t.TempDir()
	sb := &sandbox{sock: filepath.Join(short, "d.sock"), state: filepath.Join(dir, "state")}
	sb.env = append(cleanEnv(),
		"SKILLTRI_CONFIG_DIR="+filepath.Join(dir, "config"),
		"SKILLTRI_STATE_DIR="+sb.state,
		"SKILLTRI_DATA_DIR="+filepath.Join(dir, "data"),
		"SKILLTRI_CACHE_DIR="+filepath.Join(dir, "cache"),
		"SKILLTRI_SOCKET="+sb.sock,
	)
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sb.stopDaemon(t) })
	return sb
}

// cleanEnv is the test's environment without anything that could point skilltri at real files.
func cleanEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "SKILLTRI_") || strings.HasPrefix(kv, "XDG_") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

func (sb *sandbox) writeConfig(t *testing.T, c *config.Config) {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var cfgDir string
	for _, kv := range sb.env {
		if v, ok := strings.CutPrefix(kv, "SKILLTRI_CONFIG_DIR="); ok {
			cfgDir = v
		}
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (sb *sandbox) status(t *testing.T) daemon.StatusReply {
	t.Helper()
	var st daemon.StatusReply
	if err := daemon.Control(sb.sock, daemon.Hello{V: 1, Kind: "control", Cmd: "status"}, &st); err != nil {
		t.Fatalf("status: %v", err)
	}
	return st
}

// stopDaemon sends SIGTERM to the sandbox's daemon (if one answers) and waits for its socket to go.
func (sb *sandbox) stopDaemon(t *testing.T) {
	var st daemon.StatusReply
	if err := daemon.Control(sb.sock, daemon.Hello{V: 1, Kind: "control", Cmd: "status"}, &st); err != nil {
		return // no daemon
	}
	if err := syscall.Kill(st.PID, syscall.SIGTERM); err != nil {
		t.Errorf("stop daemon %d: %v", st.PID, err)
		return
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(sb.sock); errors.Is(err, os.ErrNotExist) {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("daemon %d still has its socket 10s after SIGTERM", st.PID)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// shimCmd is `skilltri mcp args...` running in dir with the sandbox environment.
func (sb *sandbox) shimCmd(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command(skilltriBin, append([]string{"mcp"}, args...)...)
	cmd.Dir = dir
	cmd.Env = sb.env
	return cmd
}

// connect starts `skilltri mcp` in dir and connects a go-sdk client to it.
func (sb *sandbox) connect(t *testing.T, dir string, opts *mcp.ClientOptions, args ...string) *mcp.ClientSession {
	t.Helper()
	cmd := sb.shimCmd(dir, args...)
	c := mcp.NewClient(&mcp.Implementation{Name: "e2e-harness", Version: "0"}, opts)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cs, err := c.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect from %s: %v", dir, err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// noRoots makes the client not advertise roots, so the session folder is the shim's cwd.
func noRoots() *mcp.ClientOptions {
	return &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}}
}

func toolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
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

func fake(name, tools string) *config.Server {
	return &config.Server{Type: "stdio", Command: fakeBin, Env: map[string]string{"FAKEMCP_NAME": name, "FAKEMCP_TOOLS": tools}}
}

// project is a demo project folder with a subfolder; beta is limited to the project folder itself.
func project(t *testing.T, sb *sandbox) (proj, sub string) {
	t.Helper()
	proj = t.TempDir()
	sub = filepath.Join(proj, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	beta := fake("beta", "pong")
	beta.Rules = []rules.Rule{{Path: proj, Mode: rules.Only}}
	sb.writeConfig(t, &config.Config{Version: 1, Servers: map[string]*config.Server{
		"alpha": fake("alpha", "ping"),
		"beta":  beta,
	}})
	return proj, sub
}

func TestMCPEndToEnd(t *testing.T) {
	sb := newSandbox(t)
	proj, sub := project(t, sb)

	inSub := sb.connect(t, sub, noRoots())
	if got, want := toolNames(t, inSub), []string{"alpha_ping"}; !slices.Equal(got, want) {
		t.Errorf("tools in %s = %v, want %v", sub, got, want)
	}
	res, err := inSub.CallTool(context.Background(), &mcp.CallToolParams{Name: "alpha_ping", Arguments: map[string]any{"x": 1}})
	if err != nil || res.IsError {
		t.Fatalf("CallTool alpha_ping: %v %+v", err, res)
	}

	inProj := sb.connect(t, proj, noRoots())
	if got, want := toolNames(t, inProj), []string{"alpha_ping", "beta_pong"}; !slices.Equal(got, want) {
		t.Errorf("tools in %s = %v, want %v", proj, got, want)
	}

	st := sb.status(t)
	if len(st.Sessions) != 2 {
		t.Fatalf("status sessions = %+v, want 2", st.Sessions)
	}
	for _, s := range st.Sessions {
		if s.ClientName != "e2e-harness" {
			t.Errorf("session %s client = %q, want e2e-harness", s.ID, s.ClientName)
		}
	}

	// --cwd overrides the working directory.
	explicit := sb.connect(t, sub, nil, "--cwd", proj)
	if got, want := toolNames(t, explicit), []string{"alpha_ping", "beta_pong"}; !slices.Equal(got, want) {
		t.Errorf("tools with --cwd %s = %v, want %v", proj, got, want)
	}
}

func TestConcurrentStarts(t *testing.T) {
	sb := newSandbox(t)
	proj, _ := project(t, sb)

	const n = 3
	sessions := make([]*mcp.ClientSession, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			c := mcp.NewClient(&mcp.Implementation{Name: "e2e-harness", Version: "0"}, noRoots())
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			sessions[i], errs[i] = c.Connect(ctx, &mcp.CommandTransport{Command: sb.shimCmd(proj)}, nil)
		})
	}
	wg.Wait()
	for i, cs := range sessions {
		if errs[i] != nil {
			t.Fatalf("shim %d: connect: %v", i, errs[i])
		}
		t.Cleanup(func() { cs.Close() })
	}
	for i, cs := range sessions {
		if got := toolNames(t, cs); !slices.Contains(got, "alpha_ping") {
			t.Errorf("shim %d tools = %v, want alpha_ping", i, got)
		}
	}

	st := sb.status(t)
	if len(st.Sessions) != n {
		t.Errorf("status sessions = %d, want %d (all on one daemon)", len(st.Sessions), n)
	}
	if err := syscall.Kill(st.PID, 0); err != nil {
		t.Errorf("daemon pid %d not alive: %v", st.PID, err)
	}
	log, err := os.ReadFile(filepath.Join(sb.state, "daemon.log"))
	if err != nil {
		t.Fatalf("read daemon log: %v", err)
	}
	if c := strings.Count(string(log), "listening on"); c != 1 {
		t.Errorf("%d daemons listened, want exactly 1; log:\n%s", c, log)
	}
}

// rawShim is `skilltri mcp` driven with newline-delimited JSON on its stdin.
type rawShim struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	lines chan []byte
	done  chan struct{} // closed when the process has exited; err is then its Wait result
	err   error
}

func startRaw(t *testing.T, sb *sandbox, dir string) *rawShim {
	t.Helper()
	cmd := sb.shimCmd(dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r := &rawShim{cmd: cmd, stdin: stdin, lines: make(chan []byte, 16), done: make(chan struct{})}
	go func() {
		defer close(r.lines)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			r.lines <- slices.Clone(sc.Bytes())
		}
		r.err = cmd.Wait() // after stdout hit EOF, as exec requires
		close(r.done)
	}()
	t.Cleanup(func() {
		stdin.Close()
		_ = cmd.Process.Kill()
		<-r.done
	})
	return r
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// call writes one request line and waits up to timeout for the response with the same id.
func (r *rawShim) call(t *testing.T, id, line string, timeout time.Duration) rpcResponse {
	t.Helper()
	if _, err := io.WriteString(r.stdin, line+"\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	deadline := time.After(timeout)
	for {
		select {
		case b, ok := <-r.lines:
			if !ok {
				t.Fatalf("shim stdout closed waiting for %s", id)
			}
			var resp rpcResponse
			if err := json.Unmarshal(b, &resp); err != nil {
				t.Fatalf("bad line %q: %v", b, err)
			}
			if string(resp.ID) == id {
				return resp
			}
		case <-deadline:
			t.Fatalf("no response to %s within %s", id, timeout)
		}
	}
}

func TestUnknownMethod(t *testing.T) {
	sb := newSandbox(t)
	proj, _ := project(t, sb)

	// The default go-sdk client probes server/discover with protocol 2026-07-28 in _meta and falls
	// back to initialize; it must end up connected. This also starts the daemon.
	cs := sb.connect(t, proj, nil)
	if got := toolNames(t, cs); !slices.Contains(got, "alpha_ping") {
		t.Errorf("default client tools = %v, want alpha_ping", got)
	}

	r := startRaw(t, sb, proj)
	resp := r.call(t, `"p1"`, `{"jsonrpc":"2.0","id":"p1","method":"server/discover","params":{}}`, 2*time.Second)
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Fatalf("server/discover response = %+v (result %s), want error -32601", resp.Error, resp.Result)
	}
	resp = r.call(t, `1`, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"raw","version":"0"}}}`, 5*time.Second)
	if resp.Error != nil {
		t.Fatalf("initialize error: %+v", resp.Error)
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(resp.Result, &init); err != nil || init.ProtocolVersion != "2025-11-25" {
		t.Fatalf("initialize result %s: %v", resp.Result, err)
	}

	// Clean EOF on stdin ends the shim with exit code 0.
	r.stdin.Close()
	go func() { // the reader must reach EOF before the process is waited for
		for range r.lines {
		}
	}()
	select {
	case <-r.done:
		if r.err != nil {
			t.Errorf("shim exit after stdin EOF: %v", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Error("shim did not exit after stdin EOF")
	}
}
