package daemon

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/gateway"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// accessTools is a provider like the agent tools: skilltri_demo_status unless agentAccess is off.
// The tool answers with the daemon's session count and the session folder.
func accessTools(d *Daemon) gateway.ToolProvider {
	return func(cfg *config.Config, _ gateway.SessionInfo) []gateway.LocalTool {
		if cfg.AgentAccess() == config.AgentAccessOff {
			return nil
		}
		return []gateway.LocalTool{{
			Tool: &mcp.Tool{Name: "skilltri_demo_status", InputSchema: map[string]any{"type": "object"}},
			Handle: func(_ context.Context, info gateway.SessionInfo, _ json.RawMessage) (*mcp.CallToolResult, error) {
				b, _ := json.Marshal(map[string]any{"sessions": len(d.Status().Sessions), "dir": info.Dir})
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil
			},
		}}
	}
}

func TestToolsFollowAgentAccess(t *testing.T) {
	e := newEnv(t)
	e.tools = accessTools
	p, _ := projectDirs(t)
	e.writeConfig(t, cfgOf(map[string]*config.Server{"a": fake("a", "x")}))
	e.start(t)
	c := e.connect(t, Hello{Dir: p})
	if got := c.allTools(t); !slices.Contains(got, "skilltri_demo_status") || !slices.Contains(got, "a_x") {
		t.Fatalf("tools = %v", got)
	}
	res, err := c.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "skilltri_demo_status", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	var out struct {
		Sessions int
		Dir      string
	}
	_ = json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &out)
	if out.Sessions != 1 || out.Dir != p {
		t.Fatalf("result = %+v", out)
	}

	off := cfgOf(map[string]*config.Server{"a": fake("a", "x")})
	off.Settings.AgentAccess = config.AgentAccessOff
	e.writeConfig(t, off)
	eventually(t, 3*poll+2*time.Second, "skilltri_demo_status gone after agentAccess off", func() bool {
		return !slices.Contains(c.allTools(t), "skilltri_demo_status")
	})
	if got := c.allTools(t); !slices.Contains(got, "a_x") {
		t.Fatalf("tools after off = %v", got)
	}
	e.writeConfig(t, cfgOf(map[string]*config.Server{"a": fake("a", "x")}))
	eventually(t, 3*poll+2*time.Second, "skilltri_demo_status back", func() bool {
		return slices.Contains(c.allTools(t), "skilltri_demo_status")
	})
}

func TestBackendMethods(t *testing.T) {
	e := newEnv(t)
	p, _ := projectDirs(t)
	e.writeConfig(t, cfgOf(map[string]*config.Server{"a": fake("a", "x")}))
	e.start(t)
	d := *e.d
	if d.Config().Servers["a"] == nil {
		t.Fatal("Config() misses server a")
	}
	w, err := d.Which(p)
	if err != nil || w.Dir != p || len(w.Servers) != 1 || w.Servers[0].Name != "a" {
		t.Fatalf("Which = %+v, %v", w, err)
	}
	if _, err := d.Which(""); err == nil {
		t.Fatal("Which(\"\") must fail")
	}
	r, err := d.UpdateSources(context.Background(), nil)
	if err != nil || len(r.Sources) != 0 {
		t.Fatalf("UpdateSources = %+v, %v", r, err)
	}
}
