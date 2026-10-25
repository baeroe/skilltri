package agenttools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/baeroe/skilltri/internal/actions"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() {
	register(
		writeTool(&mcp.Tool{
			Name:        "skilltri_add_server",
			Description: "Add an MCP server to skilltri: a stdio server (command, args, env, cwd) or an http server (url, headers). Env and header values are stored as given: pass a ${secret:NAME} reference for secrets kept in the keyring, or a literal. The name \"skilltri\" is reserved."+sandboxNote,
			InputSchema: required(object(serverProps(false)), "name", "type"),
		}, addServer),
		writeTool(&mcp.Tool{
			Name:        "skilltri_update_server",
			Description: "Change an MCP server: only the fields given change, everything else (rules, tool settings, plugin origin, disabled flag) stays. A given env or headers map replaces the whole map and given args the whole list. Values sent back as skilltri_list_servers shows them (masked with •••) keep the hidden stored value: an env or header value of the same key, an argument at the same position (else the stored argument with that masked form) and the url; any other value containing ••• is refused. newName renames the server."+sandboxNote,
			InputSchema: required(object(serverProps(true)), "name"),
		}, updateServer),
		writeTool(&mcp.Tool{
			Name:        "skilltri_remove_server",
			Description: "Remove an MCP server from skilltri's config (with its rules and tool settings); its tools disappear from every session.",
			InputSchema: required(object(map[string]any{"name": str("Server name")}), "name"),
		}, removeServer),
		writeToolNote(&mcp.Tool{
			Name:        "skilltri_restart_server",
			Description: "Restart an MCP server: every running instance is stopped at once, so calls in flight in every session fail, and the next use starts a fresh process.",
			InputSchema: required(object(map[string]any{"name": str("Server name")}), "name"),
		}, restartNote, restartServer),
		writeTool(&mcp.Tool{
			Name:        "skilltri_set_adapter",
			Description: "Enable or disable a harness adapter in settings.adapters (claude-code, codex, gemini-cli, opencode, cursor, github-copilot, amp). At least one adapter stays enabled.",
			InputSchema: required(object(map[string]any{"id": str("Adapter id"), "enabled": boolean("true enables, false disables")}), "id", "enabled"),
		}, setAdapter),
		writeTool(&mcp.Tool{
			Name:        "skilltri_enable_plugin_server",
			Description: "Copy an MCP server that a marketplace plugin ships (ref SOURCE/PLUGIN/SERVER, see skilltri_list_sources) into the config as a server with the plugin's rules; name defaults to <plugin>-<server>.",
			InputSchema: required(object(map[string]any{"ref": str("SOURCE/PLUGIN/SERVER"), "name": str("Server name (default <plugin>-<server>)")}), "ref"),
		}, enablePluginServer),
		writeTool(&mcp.Tool{
			Name:        "skilltri_accept_plugin_server",
			Description: "Take over the plugin's current definition for a server imported from a plugin (its connection fields changed upstream); rules, tool settings and flags stay.",
			InputSchema: required(object(map[string]any{"name": str("Server name")}), "name"),
		}, acceptPluginServer),
	)
}

// sandboxNote warns agents that a stdio server's command is not confined by their harness.
const sandboxNote = " For a stdio server, the command runs as the user, outside this harness's sandbox, in the skilltri daemon; only add servers the user asked for."

// restartNote ends the restart tool's description (it changes no config).
const restartNote = " The config is not changed; the call is recorded in skilltri's audit log."

func serverProps(update bool) map[string]any {
	p := map[string]any{
		"name":             str("Server name (letters, digits and '-', at most 32 characters)"),
		"type":             map[string]any{"type": "string", "enum": []string{"stdio", "http"}, "description": "stdio or http"},
		"command":          str("Command of a stdio server"),
		"args":             strList("Arguments of a stdio server"),
		"env":              strMap("Environment of a stdio server; values are ${secret:NAME} references or literals"),
		"cwd":              str("Working directory of a stdio server"),
		"url":              str("URL of an http server"),
		"headers":          strMap("HTTP headers of an http server; values are ${secret:NAME} references or literals"),
		"requestTimeoutMs": integer("Request timeout in milliseconds (0: 60000)"),
		"perSession":       boolean("Start one process per session instead of a shared one"),
		"disabled":         boolean("Keep the server configured but off everywhere"),
	}
	if update {
		p["newName"] = str("Rename the server to this name")
	}
	return p
}

// serverArgs are the server fields of add/update; nil means not given.
type serverArgs struct {
	Name             string             `json:"name"`
	NewName          *string            `json:"newName"`
	Type             *string            `json:"type"`
	Command          *string            `json:"command"`
	Args             *[]string          `json:"args"`
	Env              *map[string]string `json:"env"`
	Cwd              *string            `json:"cwd"`
	URL              *string            `json:"url"`
	Headers          *map[string]string `json:"headers"`
	RequestTimeoutMs *int               `json:"requestTimeoutMs"`
	PerSession       *bool              `json:"perSession"`
	Disabled         *bool              `json:"disabled"`
}

// parse decodes args and records the target and the call's literal values.
func (a *serverArgs) parse(args json.RawMessage, ch *change) error {
	if err := parseArgs(args, a); err != nil {
		return err
	}
	if a.Name != "" {
		ch.target = "server/" + a.Name
	}
	var s config.Server
	_ = a.apply(&s) // only for the literals; masked values are refused by the real apply
	ch.secrets = secretLiterals(&s)
	if a.Name == "" {
		return errors.New("name is required")
	}
	if a.Name == config.ReservedName || (a.NewName != nil && *a.NewName == config.ReservedName) {
		return fmt.Errorf("server name %q is reserved", config.ReservedName)
	}
	return nil
}

// apply sets the given fields on s. A given value that equals the masked form of the stored one
// (and so contains •••) keeps the stored value: an env or header value of the same key, an
// argument at the same position (else the first stored argument with that masked form) and the
// url. A given value that still contains ••• after that is refused, so a masked view sent back
// never replaces a secret with "•••".
func (a *serverArgs) apply(s *config.Server) error {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&s.Type, a.Type)
	set(&s.Command, a.Command)
	set(&s.Cwd, a.Cwd)
	if a.URL != nil {
		u := *a.URL
		if strings.Contains(u, mask) && s.URL != "" && u == MaskURL(s.URL) {
			u = s.URL
		}
		s.URL = u
	}
	if a.Args != nil {
		s.Args = keepMaskedArgs(*a.Args, s.Args)
	}
	if a.Env != nil {
		s.Env = keepMasked(*a.Env, s.Env)
	}
	if a.Headers != nil {
		s.Headers = keepMasked(*a.Headers, s.Headers)
	}
	if a.RequestTimeoutMs != nil {
		s.RequestTimeoutMs = *a.RequestTimeoutMs
	}
	if a.PerSession != nil {
		s.PerSession = *a.PerSession
	}
	if a.Disabled != nil {
		s.Disabled = *a.Disabled
	}
	return a.checkMasked(s)
}

// checkMasked refuses a given field of s that still contains ••• (a masked value that matched
// no stored one).
func (a *serverArgs) checkMasked(s *config.Server) error {
	refuse := func(what string) error {
		return fmt.Errorf("%s contains %q, a masked value that matches no stored value: pass the real value or a ${secret:NAME} reference", what, mask)
	}
	if a.URL != nil && strings.Contains(s.URL, mask) {
		return refuse("url")
	}
	if a.Args != nil {
		for i, v := range s.Args {
			if strings.Contains(v, mask) {
				return refuse(fmt.Sprintf("args[%d]", i))
			}
		}
	}
	for _, m := range []struct {
		name  string
		given bool
		vals  map[string]string
	}{{"env", a.Env != nil, s.Env}, {"headers", a.Headers != nil, s.Headers}} {
		if !m.given {
			continue
		}
		for _, k := range slices.Sorted(maps.Keys(m.vals)) {
			if strings.Contains(m.vals[k], mask) {
				return refuse(m.name + " " + k)
			}
		}
	}
	return nil
}

func keepMasked(given, old map[string]string) map[string]string {
	out := make(map[string]string, len(given))
	for k, v := range given {
		if o, ok := old[k]; ok && strings.Contains(v, mask) && v == MaskValue(o) {
			v = o
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// keepMaskedArgs restores every given argument that equals the masked form (MaskArgs) of a stored
// one: the one at the same position, else the first stored argument with that masked form.
func keepMaskedArgs(given, old []string) []string {
	shown := MaskArgs(old)
	out := slices.Clone(given)
	for i, v := range out {
		if !strings.Contains(v, mask) {
			continue
		}
		if i < len(old) && shown[i] == v && old[i] != v {
			out[i] = old[i]
			continue
		}
		for j := range old {
			if shown[j] == v && old[j] != v {
				out[i] = old[j]
				break
			}
		}
	}
	return out
}

// summary names the given fields (env and headers with their keys only).
func (a *serverArgs) summary() string {
	var f []string
	add := func(given bool, name string) {
		if given {
			f = append(f, name)
		}
	}
	add(a.Type != nil, "type")
	add(a.Command != nil, "command")
	add(a.Args != nil, "args")
	if a.Env != nil {
		f = append(f, "env "+keys(*a.Env))
	}
	add(a.Cwd != nil, "cwd")
	add(a.URL != nil, "url")
	if a.Headers != nil {
		f = append(f, "headers "+keys(*a.Headers))
	}
	add(a.RequestTimeoutMs != nil, "requestTimeoutMs")
	add(a.PerSession != nil, "perSession")
	add(a.Disabled != nil, "disabled")
	if a.NewName != nil {
		f = append(f, "newName "+*a.NewName)
	}
	return strings.Join(f, ", ")
}

func keys(m map[string]string) string {
	return "[" + strings.Join(slices.Sorted(maps.Keys(m)), " ") + "]"
}

func addServer(_ context.Context, b Backend, c Call, args json.RawMessage, ch *change) error {
	var a serverArgs
	if err := a.parse(args, ch); err != nil {
		return err
	}
	if a.NewName != nil {
		return errors.New("invalid arguments: newName is only for skilltri_update_server")
	}
	ch.summary = "add: " + a.summary()
	var s config.Server
	if err := a.apply(&s); err != nil {
		return err
	}
	if err := actions.ServerAdd(callEnv(b, c), a.Name, s); err != nil {
		return err
	}
	ch.message = fmt.Sprintf("Added %s server %q.%s", s.Type, a.Name, applied)
	return nil
}

func updateServer(_ context.Context, b Backend, c Call, args json.RawMessage, ch *change) error {
	var a serverArgs
	if err := a.parse(args, ch); err != nil {
		return err
	}
	ch.summary = "update: " + a.summary()
	e := callEnv(b, c)
	if a.NewName != nil && a.onlyRename() {
		if err := actions.ServerRename(e, a.Name, *a.NewName); err != nil {
			return err
		}
	} else if err := actions.ServerPatch(e, a.Name, deref(a.NewName), a.apply); err != nil {
		return err
	}
	ch.message = fmt.Sprintf("Updated server %q.", a.Name)
	if a.NewName != nil && *a.NewName != a.Name {
		ch.message = fmt.Sprintf("Updated server %q, now called %q.", a.Name, *a.NewName)
	}
	ch.message += applied
	return nil
}

// onlyRename reports whether no field but newName is given.
func (a *serverArgs) onlyRename() bool {
	f := *a
	f.Name, f.NewName = "", nil
	return f == serverArgs{}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// nameArg parses {name} and sets the target server/<name>.
func nameArg(args json.RawMessage, ch *change) (string, error) {
	var a struct {
		Name string `json:"name"`
	}
	if err := parseArgs(args, &a); err != nil {
		return "", err
	}
	if a.Name == "" {
		return "", errors.New("name is required")
	}
	ch.target = "server/" + a.Name
	return a.Name, nil
}

func removeServer(_ context.Context, b Backend, c Call, args json.RawMessage, ch *change) error {
	name, err := nameArg(args, ch)
	if err != nil {
		return err
	}
	ch.summary = "remove"
	if err := actions.ServerRemove(callEnv(b, c), name); err != nil {
		return err
	}
	ch.message = fmt.Sprintf("Removed server %q.%s", name, applied)
	return nil
}

func restartServer(_ context.Context, b Backend, _ Call, args json.RawMessage, ch *change) error {
	name, err := nameArg(args, ch)
	if err != nil {
		return err
	}
	ch.summary = "restart"
	if err := b.Restart(name); err != nil {
		return err
	}
	ch.message = fmt.Sprintf("Restarted server %q: running instances were stopped, the next use starts a fresh process.", name)
	return nil
}

func setAdapter(_ context.Context, b Backend, c Call, args json.RawMessage, ch *change) error {
	var a struct {
		ID      string `json:"id"`
		Enabled *bool  `json:"enabled"`
	}
	if err := parseArgs(args, &a); err != nil {
		return err
	}
	if a.ID == "" || a.Enabled == nil {
		return errors.New("id and enabled are required")
	}
	ch.target, ch.summary = "adapter/"+a.ID, onOff(*a.Enabled)
	if err := actions.SetAdapter(callEnv(b, c), a.ID, *a.Enabled); err != nil {
		return err
	}
	ch.message = fmt.Sprintf("Adapter %s %s.%s", a.ID, ch.summary, applied)
	return nil
}

func onOff(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

func enablePluginServer(_ context.Context, b Backend, c Call, args json.RawMessage, ch *change) error {
	var a struct {
		Ref  string `json:"ref"`
		Name string `json:"name"`
	}
	if err := parseArgs(args, &a); err != nil {
		return err
	}
	ch.target = a.Ref
	if a.Ref == "" {
		return errors.New("ref is required")
	}
	ch.summary = "enable plugin server"
	if a.Name != "" {
		ch.summary += " as " + a.Name
	}
	name, err := actions.PluginServerEnable(callEnv(b, c), a.Ref, a.Name)
	if err != nil {
		return err
	}
	ch.summary = "enable plugin server as " + name
	ch.message = fmt.Sprintf("Enabled plugin server %s as server %q.%s", a.Ref, name, applied)
	return nil
}

func acceptPluginServer(_ context.Context, b Backend, c Call, args json.RawMessage, ch *change) error {
	name, err := nameArg(args, ch)
	if err != nil {
		return err
	}
	ch.summary = "accept the plugin's current definition"
	if err := actions.PluginServerAccept(callEnv(b, c), name); err != nil {
		return err
	}
	ch.message = fmt.Sprintf("Server %q now uses the plugin's current definition.%s", name, applied)
	return nil
}
