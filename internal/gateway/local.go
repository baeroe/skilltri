package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// LocalTool is a tool the session serves itself instead of proxying it, such as skilltri's own
// management tools. Its name must start with "skilltri_", so it never collides with a downstream
// tool (server names cannot contain "_" and "skilltri" is reserved).
type LocalTool struct {
	Tool *mcp.Tool
	// Handle runs a call with the session's identity and folder at call time and the raw arguments.
	Handle func(ctx context.Context, info SessionInfo, args json.RawMessage) (*mcp.CallToolResult, error)
}

// ToolProvider returns a session's local tools. It is called on every Refresh with the config that
// Refresh applies, so tools appear and disappear (list_changed) when the config changes.
type ToolProvider func(cfg *config.Config, info SessionInfo) []LocalTool

// SessionOption configures a Session in NewSession.
type SessionOption func(*Session)

// WithTools adds the tools of p to the session, next to the skill fallback tools.
func WithTools(p ToolProvider) SessionOption {
	return func(s *Session) { s.provider = p }
}

// addLocalTools puts the provider's tools into w. Called after addSkillTools, so the skill tools
// win a name collision; tools without the reserved prefix or a handler are skipped.
func (s *Session) addLocalTools(cfg *config.Config, info SessionInfo, w *wanted) {
	if s.provider == nil {
		return
	}
	for _, lt := range s.provider(cfg, info) {
		if lt.Tool == nil || lt.Handle == nil || !strings.HasPrefix(lt.Tool.Name, config.ReservedName+"_") {
			slog.Warn("gateway: skipping local tool without the skilltri_ prefix or a handler", "session", s.id)
			continue
		}
		name := lt.Tool.Name
		if _, dup := w.tools[name]; dup {
			slog.Warn("gateway: local tool name collision", "session", s.id, "name", name)
			continue
		}
		w.tools[name], w.toolSrc[name] = lt.Tool, route{config.ReservedName, name}
		w.local[name] = s.localHandler(lt.Handle)
	}
}

// localHandler binds handle to the session; a returned error or a panic becomes an error result.
func (s *Session) localHandler(handle func(context.Context, SessionInfo, json.RawMessage) (*mcp.CallToolResult, error)) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (res *mcp.CallToolResult, err error) {
		defer func() {
			if r := recover(); r != nil {
				res, err = errorResult(fmt.Errorf("skilltri: internal error: %v", r)), nil
			}
		}()
		var args json.RawMessage
		if req.Params != nil {
			args = req.Params.Arguments
		}
		res, err = handle(ctx, s.Info(), args)
		if err != nil {
			return errorResult(err), nil
		}
		if res == nil {
			res = textResult("")
		}
		return res, nil
	}
}
