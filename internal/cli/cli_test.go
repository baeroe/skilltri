package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baeroe/skilltri/internal/daemon"
	"github.com/baeroe/skilltri/internal/downstream"
	"github.com/baeroe/skilltri/internal/gateway"
	"github.com/baeroe/skilltri/internal/rules"
)

const demoConfig = `{"version":1,"servers":{
 "docs":{"command":"demo-docs"},
 "mail":{"command":"demo-mail","disabled":true}
}}`

// offline points skilltri at a temp config and a socket nobody listens on.
func offline(t *testing.T, config string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SKILLTRI_CONFIG_DIR", dir)
	t.Setenv("SKILLTRI_SOCKET", filepath.Join(dir, "none.sock"))
	if config != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStatusOffline(t *testing.T) {
	offline(t, "")
	var out, errs bytes.Buffer
	old := errOut
	errOut = &errs
	t.Cleanup(func() { errOut = old })
	if code := Status(&out, false); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(errs.String(), "daemon not running") || out.Len() != 0 {
		t.Errorf("stderr = %q, stdout = %q", errs.String(), out.String())
	}
}

func TestStatusOfflineJSON(t *testing.T) {
	offline(t, "")
	var out bytes.Buffer
	if code := Status(&out, true); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if strings.TrimSpace(out.String()) != `{"running":false}` {
		t.Errorf("output = %q", out.String())
	}
}

func TestWhichOffline(t *testing.T) {
	offline(t, demoConfig)
	var out bytes.Buffer
	if code := Which(&out, t.TempDir(), false); code != 0 {
		t.Fatalf("exit = %d, output %q", code, out.String())
	}
	s := out.String()
	for _, want := range []string{"docs", "mail", "✓", "✗", "disabled", "daemon not running: tool lists unavailable"} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q:\n%s", want, s)
		}
	}
}

func TestWhichOfflineJSON(t *testing.T) {
	offline(t, demoConfig)
	dir := t.TempDir()
	var out bytes.Buffer
	if code := Which(&out, dir, true); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var r daemon.WhichReply
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	if len(r.Servers) != 2 || r.Servers[0].Name != "docs" || r.Servers[0].State != "unknown" || len(r.Servers[0].Tools) != 0 {
		t.Errorf("reply = %+v", r)
	}
	if !strings.Contains(out.String(), "\n  ") {
		t.Error("JSON is not indented")
	}
}

func TestWhichOfflineBrokenConfig(t *testing.T) {
	offline(t, "{nope")
	var out bytes.Buffer
	if code := Which(&out, t.TempDir(), false); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}

func TestRenderStatus(t *testing.T) {
	var out bytes.Buffer
	renderStatus(&out, daemon.StatusReply{
		PID: 42, Version: "0.1.0", ConfigError: "parse config.json: bad",
		Servers: []downstream.ServerStatus{
			{Name: "docs", State: "ready", Tools: 7, Sessions: 2},
			{Name: "mail", State: "error", Error: "exec: not found"},
		},
		Sessions: []gateway.SessionInfo{{Dir: "/work/demo", ClientName: "claude-code"}},
	})
	s := out.String()
	for _, want := range []string{"running (pid 42, version 0.1.0)", "config error: parse config.json: bad", "docs", "ready", "exec: not found", "/work/demo", "claude-code"} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q:\n%s", want, s)
		}
	}
}

func TestRenderWhich(t *testing.T) {
	var out bytes.Buffer
	renderWhich(&out, daemon.WhichReply{Dir: "/work/demo", Servers: []daemon.WhichServer{
		{Name: "docs", State: "ready", Decision: rules.Decision{Active: true, Reason: "rule tree /work"}, Tools: []daemon.WhichTool{
			{Name: "search", Exposed: "docs_search", Decision: rules.Decision{Active: true}},
			{Name: "drop", Exposed: "docs_drop", Decision: rules.Decision{Active: false, Reason: "tool disabled"}},
		}},
		{Name: "mail", State: "stopped", Decision: rules.Decision{Reason: "disabled"}},
	}})
	s := out.String()
	for _, want := range []string{"/work/demo", "✓ docs", "rule tree /work", "docs_search", "✗ docs_drop", "tool disabled", "✗ mail", "disabled"} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q:\n%s", want, s)
		}
	}
}
