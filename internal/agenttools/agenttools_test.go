package agenttools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/baeroe/skilltri/internal/actions"
	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/doctor"
	"github.com/baeroe/skilltri/internal/rules"
	"github.com/baeroe/skilltri/internal/skills"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	literalEnv    = "demo-literal-env-value"
	literalHeader = "Bearer demo-literal-header-value"
)

// demoBackend serves a fixed demo config and catalog and records the folders it was asked about.
type demoBackend struct {
	cfg *config.Config
	cat *catalog.Catalog

	mu        sync.Mutex
	whichDirs []string
	doctorDir string
}

func newDemoBackend() *demoBackend {
	cfg := &config.Config{Version: 1, Servers: map[string]*config.Server{
		"demo": {Type: "stdio", Command: "demo-mcp", Args: []string{"--verbose"},
			Env:   map[string]string{"API_KEY": literalEnv, "TOKEN": "${secret:demo-token}", "MIXED": "x-${secret:demo-mixed}-y"},
			Rules: []rules.Rule{{Path: "/work/demo", Mode: rules.Tree}},
			Tools: map[string]*config.Tool{"delete": {Disabled: true}}},
		"web": {Type: "http", URL: "https://mcp.example.test/mcp",
			Headers: map[string]string{"Authorization": literalHeader, "X-Key": "${secret:web-key}"},
			Origin:  &config.Origin{Source: "market", Plugin: "web-plugin", Server: "web", DefHash: "abc"}},
	}, Sources: map[string]*config.Source{
		"local": {Type: "local", Path: "/work/skills"},
		"market": {Type: "marketplace", URL: "demo/market", Ref: "main", UpdateEvery: "2h", PluginsDefault: config.PluginsDisabled,
			Plugins: map[string]*config.Node{"web-plugin": {}}},
	}}
	cat := &catalog.Catalog{
		Skills: []catalog.Entry{
			{ID: "alpha", Source: "local", Skill: skills.Skill{Name: "alpha", Description: "Demo skill alpha."}},
			{ID: "beta", Source: "market", Plugin: "web-plugin", Skill: skills.Skill{Name: "beta", Description: "Demo skill beta for the web."}},
			{ID: "gamma", Source: "market", Plugin: "other-plugin", Skill: skills.Skill{Name: "gamma", Description: "Demo skill gamma."}},
		},
		Plugins: []catalog.PluginInfo{{Source: "market", Plugin: "other-plugin"}, {Source: "market", Plugin: "web-plugin", Loaded: true}},
		Sources: []catalog.SourceState{{Name: "local"}, {Name: "market", Commit: "0123456789abcdef0123456789abcdef01234567"}, {Name: "market/plugins/web-plugin", Error: "fetch failed"}},
	}
	return &demoBackend{cfg: cfg, cat: cat}
}

func (b *demoBackend) Config() *config.Config    { return b.cfg }
func (b *demoBackend) Catalog() *catalog.Catalog { return b.cat }
func (b *demoBackend) Status() Status {
	return Status{PID: 42, Version: "0.0.0-demo", Sessions: 2, Servers: []ServerStatus{
		{Name: "demo", State: "ready", Tools: 3, Sessions: 1},
		{Name: "web", State: "error", Error: "connection refused"},
	}}
}
func (b *demoBackend) Which(dir string) Which {
	b.mu.Lock()
	b.whichDirs = append(b.whichDirs, dir)
	b.mu.Unlock()
	return Which{Dir: dir, Servers: []WhichServer{{Name: "demo", Active: true, Reason: "rule", State: "ready",
		Tools: []WhichTool{{Name: "read", Exposed: "demo_read", Active: true}}}}}
}
func (b *demoBackend) Doctor(_ context.Context, dir string) []doctor.Finding {
	b.mu.Lock()
	b.doctorDir = dir
	b.mu.Unlock()
	return []doctor.Finding{{Severity: doctor.Warning, Check: "demo", Subject: "demo", Message: "demo finding"}}
}
func (b *demoBackend) Update(context.Context, []string) ([]catalog.SourceState, error) {
	return nil, nil
}
func (b *demoBackend) Env() actions.Env { return actions.Env{} }

func call(t *testing.T, b Backend, c Call, name string, args any) (string, bool) {
	t.Helper()
	var tool *Tool
	for _, tl := range Tools(config.AgentAccessWrite) {
		if tl.Def.Name == name {
			tool = &tl
		}
	}
	if tool == nil {
		t.Fatalf("no tool %s", name)
	}
	raw, _ := json.Marshal(args)
	res, err := tool.Handle(context.Background(), b, c, raw)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("%s: %d contents", name, len(res.Content))
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("%s: content is %T", name, res.Content[0])
	}
	return tc.Text, res.IsError
}

var readTools = []string{"skilltri_get_status", "skilltri_list_servers", "skilltri_list_skills", "skilltri_list_sources", "skilltri_run_doctor", "skilltri_which"}

func names(ts []Tool) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Def.Name)
	}
	return out
}

func TestToolsAccess(t *testing.T) {
	if got := Tools(config.AgentAccessOff); len(got) != 0 {
		t.Fatalf("off: %v", names(got))
	}
	if got := Tools("bogus"); len(got) != 0 {
		t.Fatalf("unknown access: %v", names(got))
	}
	read := Tools(config.AgentAccessRead)
	for _, tl := range read {
		if tl.Write {
			t.Errorf("read access offers write tool %s", tl.Def.Name)
		}
	}
	if got := names(read); !slices.Equal(got, readTools) {
		t.Fatalf("read tools = %v, want %v", got, readTools)
	}
	all := Tools(config.AgentAccessWrite)
	if len(all) < len(read) || !slices.IsSortedFunc(all, func(a, b Tool) int { return strings.Compare(a.Def.Name, b.Def.Name) }) {
		t.Fatalf("write tools = %v", names(all))
	}
	for _, tl := range all {
		n := tl.Def.Name
		if !strings.HasPrefix(n, "skilltri_") || len(n) > 64 {
			t.Errorf("bad tool name %q", n)
		}
		if tl.Def.Description == "" || tl.Def.InputSchema == nil || tl.Handle == nil {
			t.Errorf("%s: incomplete definition", n)
		}
		if n == "skilltri_load_skill" || n == "skilltri_read_skill_file" {
			t.Errorf("%s belongs to the skill fallback", n)
		}
	}
}

func TestMaskValue(t *testing.T) {
	for in, want := range map[string]string{
		"":                        "",
		"plain":                   "•••",
		"${secret:demo}":          "${secret:demo}",
		"Bearer ${secret:demo}":   "•••${secret:demo}",
		"${secret:a}:${secret:b}": "${secret:a}•••${secret:b}",
		"${secret:bad name}":      "•••",
	} {
		if got := MaskValue(in); got != want {
			t.Errorf("MaskValue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReadToolsReturnJSON(t *testing.T) {
	b := newDemoBackend()
	c := Call{Client: "demo-client", Dir: t.TempDir()}
	for _, name := range readTools {
		t.Run(name, func(t *testing.T) {
			text, isErr := call(t, b, c, name, map[string]any{})
			if isErr {
				t.Fatalf("error result: %s", text)
			}
			if !json.Valid([]byte(text)) {
				t.Fatalf("not JSON: %s", text)
			}
			if !strings.Contains(text, "\n  ") {
				t.Fatalf("not indented: %s", text)
			}
		})
	}
}

func TestReadToolsRejectBadArgs(t *testing.T) {
	text, isErr := call(t, newDemoBackend(), Call{Dir: "/"}, "skilltri_which", "not an object")
	if !isErr || !strings.Contains(text, "invalid arguments") {
		t.Fatalf("result = %q, isError %v", text, isErr)
	}
}

func TestSecretsMasked(t *testing.T) {
	b := newDemoBackend()
	for _, name := range readTools {
		text, _ := call(t, b, Call{Dir: t.TempDir()}, name, map[string]any{})
		for _, lit := range []string{literalEnv, literalHeader, "demo-literal-header"} {
			if strings.Contains(text, lit) {
				t.Errorf("%s output contains the literal %q", name, lit)
			}
		}
	}
	text, _ := call(t, b, Call{Dir: "/"}, "skilltri_list_servers", map[string]any{})
	var out struct {
		Servers []struct {
			Name    string            `json:"name"`
			Env     map[string]string `json:"env"`
			Headers map[string]string `json:"headers"`
			Origin  *config.Origin    `json:"origin"`
			Tools   map[string]any    `json:"tools"`
			Rules   []rules.Rule      `json:"rules"`
		} `json:"servers"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Servers) != 2 || out.Servers[0].Name != "demo" || out.Servers[1].Name != "web" {
		t.Fatalf("servers = %s", text)
	}
	demo, web := out.Servers[0], out.Servers[1]
	if demo.Env["API_KEY"] != "•••" || demo.Env["TOKEN"] != "${secret:demo-token}" || demo.Env["MIXED"] != "•••${secret:demo-mixed}•••" {
		t.Fatalf("env = %v", demo.Env)
	}
	if web.Headers["Authorization"] != "•••" || web.Headers["X-Key"] != "${secret:web-key}" {
		t.Fatalf("headers = %v", web.Headers)
	}
	if web.Origin == nil || web.Origin.Plugin != "web-plugin" || len(demo.Rules) != 1 || demo.Tools["delete"] == nil {
		t.Fatalf("missing config details: %s", text)
	}
}

func TestWhichDefaultsToSessionDir(t *testing.T) {
	b := newDemoBackend()
	dir := t.TempDir()
	other := filepath.Join(t.TempDir(), "other")
	call(t, b, Call{Dir: dir}, "skilltri_which", map[string]any{})
	text, _ := call(t, b, Call{Dir: dir}, "skilltri_which", map[string]any{"dir": other})
	if want := []string{rules.Normalize(dir), rules.Normalize(other)}; !slices.Equal(b.whichDirs, want) {
		t.Fatalf("which dirs = %v, want %v", b.whichDirs, want)
	}
	if !strings.Contains(text, `"exposed": "demo_read"`) {
		t.Fatalf("which output = %s", text)
	}
	call(t, b, Call{Dir: dir}, "skilltri_run_doctor", map[string]any{})
	if b.doctorDir != dir {
		t.Fatalf("doctor dir = %q, want %q", b.doctorDir, dir)
	}
}

func TestListSkills(t *testing.T) {
	b := newDemoBackend()
	type skill struct {
		ID      string `json:"id"`
		Plugin  string `json:"plugin"`
		Enabled bool   `json:"enabled"`
		Active  bool   `json:"active"`
		Reason  string `json:"reason"`
	}
	parse := func(text string) []skill {
		var out struct {
			Dir    string  `json:"dir"`
			Skills []skill `json:"skills"`
		}
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatalf("%v: %s", err, text)
		}
		return out.Skills
	}
	all := parse(func() string {
		s, _ := call(t, b, Call{Dir: "/work"}, "skilltri_list_skills", map[string]any{})
		return s
	}())
	if len(all) != 3 || all[0].ID != "alpha" || !all[0].Active || !all[1].Enabled || all[2].Enabled || all[2].Active {
		t.Fatalf("skills = %+v", all)
	}
	got := parse(func() string {
		s, _ := call(t, b, Call{Dir: "/work"}, "skilltri_list_skills", map[string]any{"source": "market", "filter": "WEB"})
		return s
	}())
	if len(got) != 1 || got[0].ID != "beta" || got[0].Plugin != "web-plugin" {
		t.Fatalf("filtered skills = %+v", got)
	}
}

func TestListSources(t *testing.T) {
	text, _ := call(t, newDemoBackend(), Call{Dir: "/"}, "skilltri_list_sources", map[string]any{})
	var out struct {
		Sources []struct {
			Name           string `json:"name"`
			Type           string `json:"type"`
			UpdateEvery    string `json:"updateEvery"`
			PluginsDefault string `json:"pluginsDefault"`
			Commit         string `json:"commit"`
			Plugins        []struct {
				Name    string `json:"name"`
				Enabled bool   `json:"enabled"`
				Error   string `json:"error"`
			} `json:"plugins"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Sources) != 2 || out.Sources[0].Name != "local" || out.Sources[0].UpdateEvery != "1h0m0s" {
		t.Fatalf("sources = %s", text)
	}
	m := out.Sources[1]
	if m.Commit == "" || m.UpdateEvery != "2h0m0s" || m.PluginsDefault != "disabled" || len(m.Plugins) != 2 {
		t.Fatalf("market = %s", text)
	}
	if p := m.Plugins[0]; p.Name != "other-plugin" || p.Enabled {
		t.Fatalf("other-plugin = %+v", p)
	}
	if p := m.Plugins[1]; p.Name != "web-plugin" || !p.Enabled || p.Error != "fetch failed" {
		t.Fatalf("web-plugin = %+v", p)
	}
}

func TestGetStatus(t *testing.T) {
	text, _ := call(t, newDemoBackend(), Call{Dir: "/"}, "skilltri_get_status", map[string]any{})
	for _, want := range []string{`"pid": 42`, `"sessions": 2`, `"connection refused"`, `"agentAccess": "write"`, `"commit": "0123456789abcdef0123456789abcdef01234567"`} {
		if !strings.Contains(text, want) {
			t.Errorf("status misses %s: %s", want, text)
		}
	}
}
