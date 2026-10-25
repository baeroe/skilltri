package gateway

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/downstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// demoProvider offers skilltri_demo_tool unless agentAccess is off; it answers with the session
// folder and client it was called for.
func demoProvider(cfg *config.Config, _ SessionInfo) []LocalTool {
	if cfg.AgentAccess() == config.AgentAccessOff {
		return nil
	}
	return []LocalTool{
		{
			Tool: &mcp.Tool{Name: "skilltri_demo_tool", Description: "Demo.", InputSchema: map[string]any{"type": "object"}},
			Handle: func(_ context.Context, info SessionInfo, args json.RawMessage) (*mcp.CallToolResult, error) {
				return textResult(info.Dir + "|" + info.ClientName + "|" + string(args)), nil
			},
		},
		{Tool: &mcp.Tool{Name: "demo_bad_prefix", InputSchema: map[string]any{"type": "object"}}, Handle: nil},
	}
}

func TestLocalToolsFollowConfig(t *testing.T) {
	dir := t.TempDir()
	h := &harness{changed: make(chan string, 16)}
	h.cfg.Store(&config.Config{Version: 1, Servers: map[string]*config.Server{}})
	h.mgr = downstream.NewManager(downstream.Options{Lookup: func(string) (*config.Server, bool) { return nil, false }})
	t.Cleanup(h.mgr.Close)
	h.sess = NewSession(SessionInfo{ID: "s1", Dir: dir}, h.cfg.Load, h.mgr, nil, WithTools(demoProvider))
	t.Cleanup(h.sess.Close)
	connectSession(t, h)
	if got := h.tools(t); !slices.Equal(got, []string{"skilltri_demo_tool"}) {
		t.Fatalf("tools = %v", got)
	}
	h.sess.SetClient("demo-client")
	res, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "skilltri_demo_tool", Arguments: map[string]any{"a": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := text(res), h.sess.Info().Dir+`|demo-client|{"a":1}`; got != want {
		t.Fatalf("result = %q, want %q", got, want)
	}

	h.cfg.Store(&config.Config{Version: 1, Servers: map[string]*config.Server{}, Settings: config.Settings{AgentAccess: "off"}})
	if err := h.sess.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.waitChanged(t, "tools")
	if got := h.tools(t); len(got) != 0 {
		t.Fatalf("tools after agentAccess off = %v", got)
	}
}
