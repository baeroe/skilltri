package agentbackend

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baeroe/skilltri/internal/agenttools"
	"github.com/baeroe/skilltri/internal/audit"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/daemon"
	"github.com/baeroe/skilltri/internal/rules"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const literal = "demo-literal-env-value"

// startDaemon runs a daemon with the agent tools on cfg in a short /tmp dir (socket path limit)
// with every skilltri dir and the home below it, and returns a connected session in dir.
func startDaemon(t *testing.T, cfg *config.Config, dir string) (*mcp.ClientSession, string, chan struct{}) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "sta")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	for env, sub := range map[string]string{"SKILLTRI_CONFIG_DIR": "config", "SKILLTRI_STATE_DIR": "state", "SKILLTRI_DATA_DIR": "data", "SKILLTRI_CACHE_DIR": "cache"} {
		t.Setenv(env, filepath.Join(root, sub))
	}
	cfgPath, sock := filepath.Join(root, "config", "config.json"), filepath.Join(root, "d.sock")
	writeConfig(t, cfgPath, cfg)
	o := Options{ConfigPath: cfgPath, Socket: sock, Home: filepath.Join(root, "home"), CacheDir: filepath.Join(root, "cache"),
		StatePath: filepath.Join(root, "state", "skills.lock"), Environ: []string{"PATH=/usr/bin:/bin"}}
	d := daemon.New(daemon.Options{ConfigPath: cfgPath, SocketPath: sock, LockPath: filepath.Join(root, "d.lock"), Version: "test",
		PollInterval: 50 * time.Millisecond, Tools: Tools(o)})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("daemon did not stop")
		}
	})
	var conn net.Conn
	for deadline := time.Now().Add(5 * time.Second); ; {
		if conn, err = net.Dial("unix", sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon not reachable: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	b, _ := json.Marshal(daemon.Hello{V: 1, Kind: "session", Dir: dir, DirExplicit: true})
	if _, err := conn.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
	changed := make(chan struct{}, 16)
	c := mcp.NewClient(&mcp.Implementation{Name: "demo-harness", Version: "0"}, &mcp.ClientOptions{
		Capabilities:           &mcp.ClientCapabilities{},
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { changed <- struct{}{} },
	})
	cs, err := c.Connect(context.Background(), &mcp.IOTransport{Reader: conn, Writer: conn}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, cfgPath, changed
}

func writeConfig(t *testing.T, path string, c *config.Config) {
	t.Helper()
	var old time.Time
	if fi, err := os.Stat(path); err == nil {
		old = fi.ModTime()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(c)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err == nil && !fi.ModTime().After(old) {
		later := old.Add(time.Second)
		_ = os.Chtimes(path, later, later)
	}
}

func agentTools(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, tl := range res.Tools {
		if tl.Name != "skilltri_load_skill" && tl.Name != "skilltri_read_skill_file" {
			out = append(out, tl.Name)
		}
	}
	slices.Sort(out)
	return out
}

func callText(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	text := callAny(t, cs, name, args)
	if !json.Valid([]byte(text)) {
		t.Fatalf("%s: %s", name, text)
	}
	return text
}

// callAny calls a tool that must succeed and returns its text.
func callAny(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if res.IsError {
		t.Fatalf("%s: %s", name, text)
	}
	return text
}

func demoConfig() *config.Config {
	return &config.Config{Version: 1, Servers: map[string]*config.Server{
		// never started: no session folder matches its rule
		"demo": {Type: "stdio", Command: "/nonexistent/demo-mcp", Env: map[string]string{"API_KEY": literal},
			Rules: []rules.Rule{{Path: "/nonexistent/project", Mode: rules.Only}}},
	}}
}

func TestAgentToolsInDaemonSession(t *testing.T) {
	dir := rules.Normalize(t.TempDir())
	cs, cfgPath, changed := startDaemon(t, demoConfig(), dir)
	var want, reads []string
	for _, tl := range agenttools.Tools(config.AgentAccessWrite) {
		want = append(want, tl.Def.Name)
		if !tl.Write {
			reads = append(reads, tl.Def.Name)
		}
	}
	if got := agentTools(t, cs); !slices.Equal(got, want) || len(reads) != 7 {
		t.Fatalf("tools = %v", got)
	}
	status := callText(t, cs, "skilltri_get_status", nil)
	if !strings.Contains(status, `"sessions": 1`) || !strings.Contains(status, `"version": "test"`) {
		t.Fatalf("status = %s", status)
	}
	which := callText(t, cs, "skilltri_which", nil)
	if !strings.Contains(which, `"dir": "`+dir+`"`) || !strings.Contains(which, `"name": "demo"`) {
		t.Fatalf("which = %s", which)
	}
	for _, name := range reads {
		if name == "skilltri_read_skill" { // needs a skill id; covered in package agenttools
			continue
		}
		if text := callText(t, cs, name, nil); strings.Contains(text, literal) {
			t.Fatalf("%s shows the literal env value: %s", name, text)
		}
	}

	off := demoConfig()
	off.Settings.AgentAccess = config.AgentAccessOff
	writeConfig(t, cfgPath, off)
	deadline := time.After(5 * time.Second)
	for len(agentTools(t, cs)) > 0 {
		select {
		case <-changed:
		case <-deadline:
			t.Fatalf("tools after agentAccess off = %v", agentTools(t, cs))
		}
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "skilltri_get_status"})
	if err == nil && !res.IsError {
		t.Fatal("skilltri_get_status still callable after agentAccess off")
	}
}

// run_doctor checks the session's folder, not the daemon's working directory.
func TestRunDoctorUsesSessionDir(t *testing.T) {
	dir := rules.Normalize(t.TempDir())
	cs, _, _ := startDaemon(t, demoConfig(), dir)
	manifest := filepath.Join(dir, ".agents", "skills", ".skilltri.json")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	text := callText(t, cs, "skilltri_run_doctor", nil)
	if !strings.Contains(text, `"fix or delete `+manifest+`"`) {
		t.Fatalf("doctor misses the project-dir finding for %s: %s", manifest, text)
	}
}

// Write tools edit the daemon's config file, restart in process and audit to the state dir.
func TestWriteToolsInDaemonSession(t *testing.T) {
	dir := rules.Normalize(t.TempDir())
	cs, cfgPath, _ := startDaemon(t, demoConfig(), dir)
	callAny(t, cs, "skilltri_add_server", map[string]any{"name": "docs", "type": "http", "url": "https://mcp.example.test/mcp",
		"headers": map[string]string{"Authorization": "Bearer " + literal}})
	c, err := config.Load(cfgPath)
	if err != nil || c.Servers["docs"] == nil || c.Servers["docs"].Headers["Authorization"] != "Bearer "+literal {
		t.Fatalf("config = %+v, %v", c, err)
	}
	callAny(t, cs, "skilltri_restart_server", map[string]any{"name": "demo"})
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "skilltri_restart_server", Arguments: map[string]any{"name": "nope"}})
	if err != nil || !res.IsError {
		t.Fatalf("restart of an unknown server: %v %+v", err, res)
	}
	es, err := audit.Read(filepath.Join(os.Getenv("SKILLTRI_STATE_DIR"), "audit.jsonl"), 0)
	if err != nil || len(es) != 3 {
		t.Fatalf("audit = %+v, %v", es, err)
	}
	if e := es[0]; e.Tool != "skilltri_add_server" || e.Target != "server/docs" || !e.OK || e.Client != "demo-harness" || e.Dir != dir {
		t.Fatalf("audit entry = %+v", e)
	}
	if es[2].OK || es[2].Error == "" {
		t.Fatalf("failed restart audit = %+v", es[2])
	}
	if data, _ := os.ReadFile(filepath.Join(os.Getenv("SKILLTRI_STATE_DIR"), "audit.jsonl")); strings.Contains(string(data), literal) {
		t.Fatalf("audit log shows the literal: %s", data)
	}
}
