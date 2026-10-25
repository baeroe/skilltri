package agenttools

import (
	"cmp"
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/doctor"
	"github.com/baeroe/skilltri/internal/rules"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const readOnly = " Read-only: changes nothing."

func init() {
	register(
		Tool{Def: &mcp.Tool{
			Name:        "skilltri_get_status",
			Description: "Show skilltri's state: the daemon (pid, version, config error), every MCP server with its state, tool count and error, every skill source with its commit and last error, the number of harness sessions and the agentAccess setting." + readOnly,
			InputSchema: object(nil),
		}, Handle: getStatus},
		Tool{Def: &mcp.Tool{
			Name:        "skilltri_which",
			Description: "Show what a session in a folder gets: every MCP server, its tools and every skill, each active or not with the deciding reason (rules, disabled flags)." + readOnly,
			InputSchema: object(map[string]any{"dir": str("Folder to decide for (default: this session's folder)")}),
		}, Handle: which},
		Tool{Def: &mcp.Tool{
			Name:        "skilltri_list_servers",
			Description: "List the configured MCP servers: type, command or url, args, env and headers (values masked: ${secret:NAME} references are shown, literal values as •••; also URL credentials and query values and the values of secret-looking flags such as --api-key), folder rules, per-tool settings and the plugin a server was imported from." + readOnly,
			InputSchema: object(nil),
		}, Handle: listServers},
		Tool{Def: &mcp.Tool{
			Name:        "skilltri_list_skills",
			Description: "List the skills of the catalog with description, source and plugin, whether they are enabled and active in a folder (with the reason) and their own folder rules." + readOnly,
			InputSchema: object(map[string]any{
				"dir":    str("Folder for the active state (default: this session's folder)"),
				"source": str("Only skills of this source"),
				"filter": str("Only skills whose id or description contains this text (case-insensitive)"),
			}),
		}, Handle: listSkills},
		Tool{Def: &mcp.Tool{
			Name:        "skilltri_list_sources",
			Description: "List the skill sources: type, url or path, ref, update interval, enabled state, folder rules, marketplace plugins with their enabled state, and the last fetched commit and error." + readOnly,
			InputSchema: object(nil),
		}, Handle: listSources},
		Tool{Def: &mcp.Tool{
			Name:        "skilltri_run_doctor",
			Description: "Run skilltri's health checks (config, daemon, sources, skills, harness setup) for this session's folder and return the findings with severity and fix." + readOnly,
			InputSchema: object(nil),
		}, Handle: runDoctor},
	)
}

// dirArg is the folder of a call: dir when given (a relative one below the session's folder),
// else the session's folder; normalized.
func dirArg(dir string, c Call) string {
	switch {
	case dir == "":
		dir = c.Dir
	case dir != "~" && !strings.HasPrefix(dir, "~/") && !filepath.IsAbs(dir):
		dir = filepath.Join(c.Dir, dir)
	}
	return rules.Normalize(dir)
}

type sourceStateView struct {
	Name      string    `json:"name"`
	Commit    string    `json:"commit,omitempty"`
	Error     string    `json:"error,omitempty"`
	FetchedAt time.Time `json:"fetchedAt,omitzero"`
}

func getStatus(_ context.Context, b Backend, _ Call, args json.RawMessage) (*mcp.CallToolResult, error) {
	if err := parseArgs(args, &struct{}{}); err != nil {
		return errorResult(err), nil
	}
	st, cfg := b.Status(), b.Config()
	servers := sortedBy(st.Servers, func(s ServerStatus) string { return s.Name })
	for i := range servers {
		servers[i].Error = maskText(servers[i].Error, cfg.Servers[servers[i].Name])
	}
	out := struct {
		Daemon struct {
			PID         int    `json:"pid"`
			Version     string `json:"version"`
			ConfigError string `json:"configError,omitempty"`
		} `json:"daemon"`
		AgentAccess string            `json:"agentAccess"`
		Sessions    int               `json:"sessions"`
		Servers     []ServerStatus    `json:"servers"`
		Sources     []sourceStateView `json:"sources"`
	}{AgentAccess: cfg.AgentAccess(), Sessions: st.Sessions, Servers: servers}
	out.Daemon.PID, out.Daemon.Version, out.Daemon.ConfigError = st.PID, st.Version, st.ConfigError
	out.Sources = []sourceStateView{}
	for _, s := range b.Catalog().Sources {
		out.Sources = append(out.Sources, sourceStateView(s))
	}
	out.Sources = sortedBy(out.Sources, func(s sourceStateView) string { return s.Name })
	return jsonResult(out)
}

func which(_ context.Context, b Backend, c Call, args json.RawMessage) (*mcp.CallToolResult, error) {
	var a struct {
		Dir string `json:"dir"`
	}
	if err := parseArgs(args, &a); err != nil {
		return errorResult(err), nil
	}
	w, err := b.Which(dirArg(a.Dir, c))
	if err != nil {
		return errorResult(err), nil
	}
	w.Servers = sortedBy(w.Servers, func(s WhichServer) string { return s.Name })
	for i := range w.Servers {
		w.Servers[i].Tools = sortedBy(w.Servers[i].Tools, func(t WhichTool) string { return t.Name })
	}
	w.Skills = sortedBy(w.Skills, func(s WhichSkill) string { return s.ID })
	return jsonResult(w)
}

type toolView struct {
	Disabled bool         `json:"disabled,omitempty"`
	Rules    []rules.Rule `json:"rules,omitempty"`
}

type serverView struct {
	Name             string              `json:"name"`
	Type             string              `json:"type"`
	Command          string              `json:"command,omitempty"`
	Args             []string            `json:"args,omitempty"`
	Cwd              string              `json:"cwd,omitempty"`
	URL              string              `json:"url,omitempty"`
	Env              map[string]string   `json:"env,omitempty"`
	Headers          map[string]string   `json:"headers,omitempty"`
	RequestTimeoutMs int                 `json:"requestTimeoutMs,omitempty"`
	Disabled         bool                `json:"disabled,omitempty"`
	PerSession       bool                `json:"perSession,omitempty"`
	Rules            []rules.Rule        `json:"rules,omitempty"`
	Tools            map[string]toolView `json:"tools,omitempty"`
	Origin           *config.Origin      `json:"origin,omitempty"`
}

func listServers(_ context.Context, b Backend, _ Call, args json.RawMessage) (*mcp.CallToolResult, error) {
	if err := parseArgs(args, &struct{}{}); err != nil {
		return errorResult(err), nil
	}
	cfg := b.Config()
	out := struct {
		Servers []serverView `json:"servers"`
	}{Servers: []serverView{}}
	for _, name := range sortedKeys(cfg.Servers) {
		s := cfg.Servers[name]
		if s == nil {
			continue
		}
		v := serverView{Name: name, Type: s.Type, Command: s.Command, Args: MaskArgs(s.Args), Cwd: s.Cwd, URL: MaskURL(s.URL),
			Env: MaskMap(s.Env), Headers: MaskMap(s.Headers), RequestTimeoutMs: s.RequestTimeoutMs,
			Disabled: s.Disabled, PerSession: s.PerSession, Rules: s.Rules, Origin: s.Origin}
		for tn, t := range s.Tools {
			if t == nil {
				continue
			}
			if v.Tools == nil {
				v.Tools = map[string]toolView{}
			}
			v.Tools[tn] = toolView{Disabled: t.Disabled, Rules: t.Rules}
		}
		out.Servers = append(out.Servers, v)
	}
	return jsonResult(out)
}

type skillView struct {
	ID          string       `json:"id"`
	Description string       `json:"description"`
	Source      string       `json:"source"`
	Plugin      string       `json:"plugin,omitempty"`
	Enabled     bool         `json:"enabled"`
	Active      bool         `json:"active"`
	Reason      string       `json:"reason"`
	Rules       []rules.Rule `json:"rules,omitempty"`
}

func listSkills(_ context.Context, b Backend, c Call, args json.RawMessage) (*mcp.CallToolResult, error) {
	var a struct {
		Dir    string `json:"dir"`
		Source string `json:"source"`
		Filter string `json:"filter"`
	}
	if err := parseArgs(args, &a); err != nil {
		return errorResult(err), nil
	}
	cfg, dir := b.Config(), dirArg(a.Dir, c)
	filter := strings.ToLower(a.Filter)
	out := struct {
		Dir    string      `json:"dir"`
		Skills []skillView `json:"skills"`
	}{Dir: dir, Skills: []skillView{}}
	for _, d := range catalog.Evaluate(b.Catalog(), cfg, dir) {
		e := d.Entry
		desc := strings.Join(strings.Fields(e.Skill.Description), " ")
		if a.Source != "" && e.Source != a.Source {
			continue
		}
		if filter != "" && !strings.Contains(strings.ToLower(e.ID), filter) && !strings.Contains(strings.ToLower(desc), filter) {
			continue
		}
		v := skillView{ID: e.ID, Description: desc, Source: e.Source, Plugin: e.Plugin, Enabled: true,
			Active: d.Decision.Active, Reason: d.Decision.Reason}
		for _, l := range catalog.Levels(cfg, e) {
			if l.Disabled {
				v.Enabled = false
			}
		}
		if src := cfg.Sources[e.Source]; src != nil {
			if n := src.Skills[e.ID]; n != nil {
				v.Rules = n.Rules
			}
		}
		out.Skills = append(out.Skills, v)
	}
	return jsonResult(out)
}

type pluginView struct {
	Name    string       `json:"name"`
	Enabled bool         `json:"enabled"`
	Loaded  bool         `json:"loaded,omitempty"`
	Rules   []rules.Rule `json:"rules,omitempty"`
	Commit  string       `json:"commit,omitempty"`
	Error   string       `json:"error,omitempty"`
}

type sourceView struct {
	Name           string       `json:"name"`
	Type           string       `json:"type"`
	URL            string       `json:"url,omitempty"`
	Path           string       `json:"path,omitempty"`
	Ref            string       `json:"ref,omitempty"`
	Subpath        string       `json:"subpath,omitempty"`
	UpdateEvery    string       `json:"updateEvery"` // the effective interval; "manual" for "0"
	Disabled       bool         `json:"disabled,omitempty"`
	PluginsDefault string       `json:"pluginsDefault,omitempty"` // "enabled" or "disabled" (marketplace)
	Rules          []rules.Rule `json:"rules,omitempty"`
	Plugins        []pluginView `json:"plugins,omitempty"`
	Commit         string       `json:"commit,omitempty"`
	Error          string       `json:"error,omitempty"`
	FetchedAt      time.Time    `json:"fetchedAt,omitzero"`
}

func listSources(_ context.Context, b Backend, _ Call, args json.RawMessage) (*mcp.CallToolResult, error) {
	if err := parseArgs(args, &struct{}{}); err != nil {
		return errorResult(err), nil
	}
	cfg, cat := b.Config(), b.Catalog()
	states := map[string]catalog.SourceState{}
	for _, s := range cat.Sources {
		states[s.Name] = s
	}
	out := struct {
		Sources []sourceView `json:"sources"`
	}{Sources: []sourceView{}}
	for _, name := range sortedKeys(cfg.Sources) {
		s := cfg.Sources[name]
		if s == nil {
			continue
		}
		st := states[name]
		v := sourceView{Name: name, Type: s.Type, URL: s.URL, Path: s.Path, Ref: s.Ref, Subpath: s.Subpath,
			Disabled: s.Disabled, Rules: s.Rules, Commit: st.Commit, Error: st.Error, FetchedAt: st.FetchedAt}
		switch d, err := s.Interval(); {
		case err != nil:
			v.UpdateEvery = s.UpdateEvery
		case d == 0:
			v.UpdateEvery = "manual"
		default:
			v.UpdateEvery = d.String()
		}
		if s.Type == "marketplace" {
			v.PluginsDefault = "enabled"
			if s.PluginsDefault == config.PluginsDisabled {
				v.PluginsDefault = config.PluginsDisabled
			}
			v.Plugins = plugins(name, s, cat, states)
		}
		out.Sources = append(out.Sources, v)
	}
	return jsonResult(out)
}

// plugins lists a marketplace source's plugin entries: those in the catalog and those with a
// config node, sorted by name.
func plugins(source string, s *config.Source, cat *catalog.Catalog, states map[string]catalog.SourceState) []pluginView {
	loaded := map[string]bool{}
	for _, p := range cat.Plugins {
		if p.Source == source {
			loaded[p.Plugin] = p.Loaded
		}
	}
	for entry := range s.Plugins {
		if _, ok := loaded[entry]; !ok {
			loaded[entry] = false
		}
	}
	out := []pluginView{}
	for _, entry := range sortedKeys(loaded) {
		st := states[source+"/plugins/"+entry]
		v := pluginView{Name: entry, Enabled: s.PluginEnabled(entry), Loaded: loaded[entry], Commit: st.Commit, Error: st.Error}
		if n := s.Plugins[entry]; n != nil {
			v.Rules = n.Rules
		}
		out = append(out, v)
	}
	return out
}

func runDoctor(ctx context.Context, b Backend, c Call, args json.RawMessage) (*mcp.CallToolResult, error) {
	if err := parseArgs(args, &struct{}{}); err != nil {
		return errorResult(err), nil
	}
	fs := b.Doctor(ctx, c.Dir)
	if ctx.Err() != nil {
		return errorResult(ctx.Err()), nil
	}
	out := struct {
		Findings []doctor.Finding `json:"findings"`
	}{Findings: append([]doctor.Finding{}, fs...)} // Run already sorts them
	return jsonResult(out)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// sortedBy returns a sorted copy of s (never nil).
func sortedBy[T any](s []T, key func(T) string) []T {
	out := append([]T{}, s...)
	slices.SortStableFunc(out, func(a, b T) int { return cmp.Compare(key(a), key(b)) })
	return out
}
