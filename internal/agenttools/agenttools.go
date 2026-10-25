// Package agenttools defines skilltri's own MCP management tools (skilltri_*), which let agents in
// any harness session inspect skilltri and, with settings.agentAccess "write", change it. The tools
// run against a Backend (the daemon in production), so this package imports neither the daemon nor
// the gateway; the daemon side adapts its state to the small types defined here.
//
// Agents never see secret values: env and header values are masked (MaskValue), no tool reads or
// writes the keyring. Every write tool call is appended to the audit log (package audit) without
// env or header values.
package agenttools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/baeroe/skilltri/internal/actions"
	"github.com/baeroe/skilltri/internal/audit"
	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/doctor"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Backend is what the tools read and change. Every method is safe for concurrent use.
type Backend interface {
	// Config is the config the daemon currently serves.
	Config() *config.Config
	// Catalog is the current skill catalog (never nil).
	Catalog() *catalog.Catalog
	// Status is the daemon's state.
	Status() Status
	// Which decides every server, tool and skill for dir (already normalized, never empty).
	Which(dir string) (Which, error)
	// Doctor runs the health checks as if started in dir.
	Doctor(ctx context.Context, dir string) []doctor.Finding
	// Update syncs the named sources (all enabled ones when empty) and rebuilds the catalog.
	Update(ctx context.Context, sources []string) ([]catalog.SourceState, error)
	// Env is where actions edit the config (write tools set Env.Dir to the call's folder).
	Env() actions.Env
	// Restart stops every instance of the configured server name; the next use starts it fresh.
	Restart(name string) error
	// AuditLog is the path of the audit log every write tool call is appended to.
	AuditLog() string
}

// Status is the daemon's state (the agent view of the status control command).
type Status struct {
	PID         int            `json:"pid"`
	Version     string         `json:"version"`
	ConfigError string         `json:"configError,omitempty"`
	Servers     []ServerStatus `json:"servers"`
	Sessions    int            `json:"sessions"`
}

// ServerStatus is one configured server's runtime state. Stderr output is left out: it may
// contain secret values.
type ServerStatus struct {
	Name     string    `json:"name"`
	State    string    `json:"state"` // "stopped" | "starting" | "ready" | "error"
	Error    string    `json:"error,omitempty"`
	Tools    int       `json:"tools"`
	Sessions int       `json:"sessions"`
	LastCall time.Time `json:"lastCall,omitzero"`
}

// Which is what a session in Dir would get.
type Which struct {
	Dir     string        `json:"dir"`
	Servers []WhichServer `json:"servers"`
	Skills  []WhichSkill  `json:"skills"`
}

// WhichServer is one configured server's decision for the folder; Tools only for ready servers.
type WhichServer struct {
	Name   string      `json:"name"`
	Active bool        `json:"active"`
	Reason string      `json:"reason"`
	State  string      `json:"state"`
	Tools  []WhichTool `json:"tools,omitempty"`
}

// WhichTool is one tool of a ready server and its decision.
type WhichTool struct {
	Name    string `json:"name"`
	Exposed string `json:"exposed"`
	Active  bool   `json:"active"`
	Reason  string `json:"reason"`
}

// WhichSkill is one catalog skill's decision for the folder.
type WhichSkill struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Plugin string `json:"plugin,omitempty"`
	Active bool   `json:"active"`
	Reason string `json:"reason"`
}

// Call is the calling session: its client name (for the audit log) and current folder (the
// default folder of tools that take one).
type Call struct {
	Client, Dir string
}

// Tool is one management tool. Write tools change the config and are offered only with
// agentAccess "write".
type Tool struct {
	Def    *mcp.Tool
	Write  bool
	Handle func(ctx context.Context, b Backend, c Call, args json.RawMessage) (*mcp.CallToolResult, error)
}

// registry is every tool, sorted by name (read.go and the write tools add to it).
var registry []Tool

// destructiveTools may remove or overwrite what is there (a server, source, rule list, SKILL.md,
// an enabled node); openWorldTools reach out to the network (git fetches).
var (
	destructiveTools = []string{"skilltri_remove_rule", "skilltri_remove_server", "skilltri_remove_source", "skilltri_set_enabled",
		"skilltri_set_rules", "skilltri_update_server", "skilltri_write_skill"}
	openWorldTools = []string{"skilltri_add_source", "skilltri_update_sources"}
)

// register adds ts to the registry and gives each its MCP annotations: read tools are read-only,
// write tools are destructive when listed in destructiveTools; openWorldTools are open-world.
func register(ts ...Tool) {
	for _, t := range ts {
		openWorld := slices.Contains(openWorldTools, t.Def.Name)
		a := &mcp.ToolAnnotations{ReadOnlyHint: !t.Write, OpenWorldHint: &openWorld}
		if t.Write {
			d := slices.Contains(destructiveTools, t.Def.Name)
			a.DestructiveHint = &d
		}
		t.Def.Annotations = a
	}
	registry = append(registry, ts...)
	slices.SortFunc(registry, func(a, b Tool) int { return strings.Compare(a.Def.Name, b.Def.Name) })
}

// Tools returns the tools offered with access ("off", "read" or "write"), sorted by name. An
// unknown value offers none.
func Tools(access string) []Tool {
	var out []Tool
	for _, t := range registry {
		switch {
		case access == config.AgentAccessWrite, access == config.AgentAccessRead && !t.Write:
			out = append(out, t)
		}
	}
	return out
}

// secretRef matches a ${secret:NAME} reference (the syntax of package secrets).
var secretRef = regexp.MustCompile(`\$\{secret:[A-Za-z0-9._-]+\}`)

// mask is what MaskValue shows instead of literal text.
const mask = "•••"

// MaskValue shows an env or header value to agents: ${secret:…} references stay as they are,
// every run of literal text in between becomes "•••". An empty value stays empty.
func MaskValue(v string) string {
	if v == "" {
		return ""
	}
	var b strings.Builder
	last := 0
	for _, loc := range secretRef.FindAllStringIndex(v, -1) {
		if loc[0] > last {
			b.WriteString(mask)
		}
		b.WriteString(v[loc[0]:loc[1]])
		last = loc[1]
	}
	if last < len(v) {
		b.WriteString(mask)
	}
	return b.String()
}

// MaskMap is MaskValue for every value of m (nil stays nil).
func MaskMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = MaskValue(v)
	}
	return out
}

// parseArgs decodes args (empty or null: no arguments) into v, rejecting unknown fields.
func parseArgs(args json.RawMessage, v any) error {
	if len(bytes.TrimSpace(args)) == 0 || string(bytes.TrimSpace(args)) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid arguments: %v", err)
	}
	return nil
}

// jsonResult is v as one text content of indented JSON.
func jsonResult(v any) (*mcp.CallToolResult, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil
}

// errorResult is err as an error result (the agent sees the text, the session stays up).
func errorResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}
}

// object is a JSON Schema object with properties (nil: no arguments).
func object(props map[string]any) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	return map[string]any{"type": "object", "properties": props, "additionalProperties": false}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

// effect ends every write tool's description.
const effect = " Config changes reach every running session within seconds (the daemon watches the config); the call is recorded in skilltri's audit log."

// applied ends the confirmation of a config change.
const applied = " Running sessions get the change within seconds."

// change is what a write call did, for its result and its audit entry. A run fills target as
// soon as it knows it, so a failed call is audited with it too.
type change struct {
	target   string   // the node, e.g. "server/docs"
	summary  string   // what changed, never env or header values
	message  string   // the confirmation
	warnings []string // e.g. unknown-target warnings of actions
	secrets  []string // literal values of the call, kept out of every text
}

// writeTool is a write tool that changes the config (its description ends with effect).
func writeTool(def *mcp.Tool, run runFunc) Tool { return writeToolNote(def, effect, run) }

type runFunc func(ctx context.Context, b Backend, c Call, args json.RawMessage, ch *change) error

// scrubMin is the shortest literal scrubbed from confirmations and summaries; shorter values
// would garble names there (error texts are scrubbed of every literal of 3 bytes or more).
const scrubMin = 8

// writeToolNote is a write tool whose description ends with note: run does the change, then the
// call is audited (success or failure) and answered with the confirmation and warnings, or with
// the error as an IsError result.
func writeToolNote(def *mcp.Tool, note string, run runFunc) Tool {
	def.Description += note
	return Tool{Def: def, Write: true, Handle: func(ctx context.Context, b Backend, c Call, args json.RawMessage) (*mcp.CallToolResult, error) {
		var ch change
		err := run(ctx, b, c, args, &ch)
		scrub := func(s string, min int) string {
			for _, v := range ch.secrets {
				if len(v) >= min {
					s = strings.ReplaceAll(s, v, mask)
				}
			}
			return s
		}
		e := audit.Entry{Tool: def.Name, Target: ch.target, Summary: scrub(ch.summary, scrubMin), Client: c.Client, Dir: c.Dir, OK: err == nil}
		if err != nil {
			e.Error = scrub(err.Error(), 0)
		}
		aerr := audit.Append(b.AuditLog(), e)
		if err != nil {
			text := e.Error
			if aerr != nil {
				text += "\n(the audit log could not be written: " + aerr.Error() + ")"
			}
			return errorResult(errors.New(text)), nil
		}
		lines := []string{scrub(ch.message, scrubMin)}
		for _, w := range ch.warnings {
			lines = append(lines, "warning: "+scrub(w, scrubMin))
		}
		if aerr != nil {
			lines = append(lines, "warning: the audit log could not be written: "+aerr.Error())
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Join(lines, "\n")}}}, nil
	}}
}

// callEnv is the backend's actions Env for the call's folder.
func callEnv(b Backend, c Call) actions.Env {
	e := b.Env()
	e.Dir = c.Dir
	return e
}

func boolean(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}
func integer(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func strList(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}
func strMap(desc string) map[string]any {
	return map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": desc}
}

// required marks props of an object schema as required.
func required(schema map[string]any, props ...string) map[string]any {
	schema["required"] = props
	return schema
}
