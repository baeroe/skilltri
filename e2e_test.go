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

// toolNames lists the session's downstream tools, without the skill fallback's skilltri_ tools
// (see TestMCPSkillFallback).
func toolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	return slices.DeleteFunc(allToolNames(t, cs), func(n string) bool { return strings.HasPrefix(n, "skilltri_") })
}

func allToolNames(t *testing.T, cs *mcp.ClientSession) []string {
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

// Without SKILLTRI_SKILLS_NATIVE every session gets the skill fallback.
func TestMCPSkillFallback(t *testing.T) {
	sb := newSandbox(t)
	skills := t.TempDir()
	if err := os.MkdirAll(filepath.Join(skills, "alpha"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skills, "alpha", "SKILL.md"), []byte("---\nname: alpha\ndescription: Demo skill alpha.\n---\nAlpha body.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sb.writeConfig(t, &config.Config{Version: 1, Servers: map[string]*config.Server{}, Sources: map[string]*config.Source{
		"demo": {Type: "local", Path: skills},
	}})
	cs := sb.connect(t, t.TempDir(), noRoots())
	if got := allToolNames(t, cs); !slices.Contains(got, "skilltri_load_skill") || !slices.Contains(got, "skilltri_read_skill_file") {
		t.Fatalf("tools = %v, want the skill tools", got)
	}
	if instr := cs.InitializeResult().Instructions; !strings.Contains(instr, "- alpha: Demo skill alpha.") {
		t.Errorf("instructions = %q", instr)
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "skilltri_load_skill", Arguments: map[string]any{"name": "alpha"}})
	if err != nil || res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "Alpha body.") {
		t.Fatalf("load_skill: %v %+v", err, res)
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

// A shim-started daemon gets a defined environment: the harness's own variables never reach the
// downstream servers, and `daemon stop` ends it.
func TestDaemonEnvironmentAndStop(t *testing.T) {
	sb := newSandbox(t)
	envFile := filepath.Join(t.TempDir(), "env")
	srv := &config.Server{Type: "stdio", Command: "sh", Args: []string{"-c", `env > "$ENV_FILE"; exec "$FAKE"`},
		Env: map[string]string{"ENV_FILE": envFile, "FAKE": fakeBin, "FAKEMCP_NAME": "envy", "FAKEMCP_TOOLS": "e"}}
	sb.writeConfig(t, &config.Config{Version: 1, Servers: map[string]*config.Server{"envy": srv}})
	sb.env = append(sb.env, "DEMO_HARNESS_TOKEN=demo-secret", "SKILLTRI_CWD=/nowhere")
	cs := sb.connect(t, t.TempDir(), noRoots())
	if got := toolNames(t, cs); !slices.Equal(got, []string{"envy_e"}) {
		t.Fatalf("tools = %v", got)
	}
	b, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	env := string(b)
	for _, bad := range []string{"DEMO_HARNESS_TOKEN=", "SKILLTRI_CWD="} {
		if strings.Contains(env, bad) {
			t.Errorf("downstream env has %s:\n%s", bad, env)
		}
	}
	for _, want := range []string{"HOME=", "PATH=", "SKILLTRI_SOCKET=" + sb.sock} {
		if !strings.Contains(env, want) {
			t.Errorf("downstream env lacks %s:\n%s", want, env)
		}
	}
	if st := sb.status(t); st.Path == "" {
		t.Error("status has no daemon PATH")
	}

	cs.Close()
	cmd := exec.Command(skilltriBin, "daemon", "stop")
	cmd.Env = sb.env
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "daemon stopped") {
		t.Fatalf("daemon stop: %v: %s", err, out)
	}
	if _, err := os.Stat(sb.sock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket still there after stop: %v", err)
	}
	cmd = exec.Command(skilltriBin, "daemon", "stop")
	cmd.Env = sb.env
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "daemon not running") {
		t.Fatalf("second daemon stop: %v: %s", err, out)
	}
}

// TestHookCommands runs `skilltri env`, `shell-init` and `run` as the shell hook and the launcher do.
func TestHookCommands(t *testing.T) {
	sb := newSandbox(t)
	var env []string
	for _, kv := range sb.env {
		if !strings.HasPrefix(kv, "CLAUDE_CODE_PLUGIN_DIRS=") {
			env = append(env, kv)
		}
	}
	src := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(filepath.Join(src, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "alpha", "SKILL.md"), []byte("---\nname: alpha\ndescription: Demo.\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sb.writeConfig(t, &config.Config{Version: 1, Servers: map[string]*config.Server{}, Sources: map[string]*config.Source{
		"demo": {Type: "local", Path: src},
	}})
	work := t.TempDir()
	cmd := func(dir string, extra []string, args ...string) (stdout, stderr string, code int) {
		c := exec.Command(skilltriBin, args...)
		c.Dir = dir
		c.Env = append(append([]string(nil), env...), extra...)
		var o, e strings.Builder
		c.Stdout, c.Stderr = &o, &e
		err := c.Run()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return o.String(), e.String(), code
	}
	bashParses := func(script string) {
		t.Helper()
		if out, err := exec.Command("bash", "-n", "-c", script).CombinedOutput(); err != nil {
			t.Fatalf("bash rejects %q: %v\n%s", script, err, out)
		}
	}

	out, _, code := cmd(work, nil, "env", "--shell", "bash")
	if code != 0 || !strings.Contains(out, "export CLAUDE_CODE_PLUGIN_DIRS='") || !strings.Contains(out, "export SKILLTRI_SKILLS_NATIVE='claude-code'") {
		t.Fatalf("env: %d %q", code, out)
	}
	bashParses(out)
	if _, _, code := cmd(work, nil, "env", "--shell", "tcsh"); code != 2 {
		t.Fatalf("bad shell exit %d", code)
	}

	// a folder deleted while the shell is in it: exit 0, only valid shell on stdout
	gone := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("bash", "-c", `cd "$1" && rmdir "$1" && exec "$2" env --shell bash`, "_", gone, skilltriBin)
	c.Env = env
	b, err := c.Output()
	if err != nil || strings.Contains(string(b), "export CLAUDE_CODE_PLUGIN_DIRS=") || !strings.Contains(string(b), "unset CLAUDE_CODE_PLUGIN_DIRS") {
		t.Fatalf("deleted folder: %q, %v", b, err)
	}
	bashParses(string(b))

	// a broken config: exit 0, the note only on stderr
	sb.writeConfigRaw(t, "{")
	out, errOut, code := cmd(work, []string{"CLAUDE_CODE_PLUGIN_DIRS=/mine"}, "env", "--shell", "bash")
	if code != 0 || strings.Contains(out, "config") || !strings.Contains(errOut, "config") || !strings.Contains(out, "export CLAUDE_CODE_PLUGIN_DIRS='/mine'") {
		t.Fatalf("broken config: %d %q %q", code, out, errOut)
	}
	bashParses(out)
	sb.writeConfig(t, &config.Config{Version: 1, Servers: map[string]*config.Server{}, Sources: map[string]*config.Source{
		"demo": {Type: "local", Path: src},
	}})

	if out, _, code := cmd(work, nil, "shell-init", "zsh"); code != 0 || !strings.Contains(out, "chpwd_functions") {
		t.Fatalf("shell-init zsh: %d %q", code, out)
	}
	if _, _, code := cmd(work, nil, "shell-init", "tcsh"); code != 2 {
		t.Fatalf("shell-init tcsh exit %d", code)
	}

	// run: unknown harness, harness not on PATH, then a fake harness that prints its env and args
	if _, errOut, code := cmd(work, nil, "run", "nope"); code != 2 || !strings.Contains(errOut, "codex") {
		t.Fatalf("run nope: %d %q", code, errOut)
	}
	bin := t.TempDir()
	if _, errOut, code := cmd(work, []string{"PATH=" + bin}, "run", "codex"); code != 127 || !strings.Contains(errOut, "codex") {
		t.Fatalf("run without codex: %d %q", code, errOut)
	}
	fake := "#!/bin/sh\nprintf '%s\\n' \"$@\"\necho \"dirs=$CLAUDE_CODE_PLUGIN_DIRS\"\necho \"native=$SKILLTRI_SKILLS_NATIVE\"\necho \"keep=$DEMO_KEEP\"\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	out, errOut, code = cmd(work, []string{"PATH=" + bin + ":/usr/bin:/bin", "DEMO_KEEP=yes", "SKILLTRI_SKILLS_NATIVE=stale"}, "run", "claude-code", "a b", "--flag")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || len(lines) != 5 || lines[0] != "a b" || lines[1] != "--flag" ||
		!strings.HasPrefix(lines[2], "dirs=") || !strings.HasSuffix(lines[2], "/claude/skilltri") ||
		lines[3] != "native=claude-code" || lines[4] != "keep=yes" {
		t.Fatalf("run claude-code: %d %q %q", code, out, errOut)
	}
}

func (sb *sandbox) writeConfigRaw(t *testing.T, content string) {
	t.Helper()
	for _, kv := range sb.env {
		if v, ok := strings.CutPrefix(kv, "SKILLTRI_CONFIG_DIR="); ok {
			if err := os.WriteFile(filepath.Join(v, "config.json"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
