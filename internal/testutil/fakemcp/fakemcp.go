// Package fakemcp is a configurable MCP server for tests. Every tool echoes its arguments together
// with the server name and process ID, so tests can tell which server and which process answered.
package fakemcp

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Spec describes the fake server's tools, prompts and resources.
type Spec struct {
	Name      string // server name in echoes; "" → "fake"
	Tools     []string
	Prompts   []string
	Resources []string // names; each is served at ResourceURI(name)
	SlowTool  string   // tool that sleeps SlowMs before echoing
	SlowMs    int
}

// Echo is the JSON text a tool call returns.
type Echo struct {
	Server string          `json:"server"`
	PID    int             `json:"pid"`
	Args   json.RawMessage `json:"args"`
}

// ResourceURI is the URI of the fake resource name.
func ResourceURI(name string) string { return "fake:///" + name }

// New returns a server for spec. Callers may add tools later with AddTool/EchoTool.
func New(spec Spec) *mcp.Server {
	if spec.Name == "" {
		spec.Name = "fake"
	}
	s := mcp.NewServer(&mcp.Implementation{Name: spec.Name, Version: "0.0.1"}, nil)
	for _, t := range spec.Tools {
		if t != spec.SlowTool {
			AddEchoTool(s, spec.Name, t, 0)
		}
	}
	if spec.SlowTool != "" {
		AddEchoTool(s, spec.Name, spec.SlowTool, time.Duration(spec.SlowMs)*time.Millisecond)
	}
	for _, p := range spec.Prompts {
		s.AddPrompt(&mcp.Prompt{Name: p}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			args, _ := json.Marshal(req.Params.Arguments)
			return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: echo(spec.Name, args)},
			}}}, nil
		})
	}
	for _, r := range spec.Resources {
		uri := ResourceURI(r)
		s.AddResource(&mcp.Resource{Name: r, URI: uri, MIMEType: "text/plain"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: "text/plain", Text: spec.Name + ":" + r}}}, nil
		})
	}
	return s
}

// AddEchoTool adds a tool named name that waits delay (or until cancelled) and then echoes its arguments.
func AddEchoTool(s *mcp.Server, server, name string, delay time.Duration) {
	s.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: echo(server, req.Params.Arguments)}}}, nil
		})
}

func echo(server string, args json.RawMessage) string {
	if len(args) == 0 {
		args = json.RawMessage("null")
	}
	b, _ := json.Marshal(Echo{Server: server, PID: os.Getpid(), Args: args})
	return string(b)
}
