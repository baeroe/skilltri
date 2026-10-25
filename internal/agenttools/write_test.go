package agenttools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/baeroe/skilltri/internal/actions"
	"github.com/baeroe/skilltri/internal/audit"
	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/rules"
	"github.com/baeroe/skilltri/internal/sources"
	"github.com/baeroe/skilltri/internal/target"
)

const writeLiteral = "demo-write-literal-value"

// writeBackend is a demo backend whose actions edit a real config file in a temp folder.
func writeBackend(t *testing.T) *demoBackend {
	t.Helper()
	base := t.TempDir()
	b := newDemoBackend()
	b.env = actions.Env{
		ConfigPath:  filepath.Join(base, "config", "config.json"),
		CatalogPath: filepath.Join(base, "cache", "catalog.json"),
		Socket:      filepath.Join(base, "none.sock"),
		Builder:     &catalog.Builder{Fetcher: &sources.Fetcher{Root: filepath.Join(base, "sources")}, Lock: filepath.Join(base, "lock.json")},
	}
	b.auditPath = filepath.Join(base, "state", "audit.jsonl")
	market := filepath.Join(base, "demo-market")
	if err := os.CopyFS(market, os.DirFS("../marketplace/testdata/demo-market")); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Version: 1, Servers: map[string]*config.Server{
		"demo": {Type: "stdio", Command: "demo-mcp", Args: []string{"--verbose"}, Env: map[string]string{"API_KEY": literalEnv, "TOKEN": "${secret:demo-token}"},
			Disabled: true, Rules: []rules.Rule{{Path: "/work/demo", Mode: rules.Tree}},
			Tools:  map[string]*config.Tool{"delete": {Disabled: true}},
			Origin: &config.Origin{Source: "m", Plugin: "writing", Server: "notes", DefHash: "abc"}},
	}, Sources: map[string]*config.Source{"m": {Type: "marketplace", Path: market}}}
	if err := config.Update(b.env.ConfigPath, func(c *config.Config) error { *c = *cfg; return nil }); err != nil {
		t.Fatal(err)
	}
	return b
}

func (b *demoBackend) load(t *testing.T) *config.Config {
	t.Helper()
	c, err := config.Load(b.env.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (b *demoBackend) raw(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(b.env.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (b *demoBackend) audits(t *testing.T) []audit.Entry {
	t.Helper()
	es, err := audit.Read(b.auditPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

var demoCall = Call{Client: "demo-client", Dir: "/work/demo"}

// mustWrite calls a write tool that must succeed and returns its text.
func mustWrite(t *testing.T, b *demoBackend, name string, args any) string {
	t.Helper()
	text, isErr := call(t, b, demoCall, name, args)
	if isErr {
		t.Fatalf("%s %v: %s", name, args, text)
	}
	return text
}

// lastAudit checks the newest audit entry and that no audit line contains a literal.
func lastAudit(t *testing.T, b *demoBackend, tool, target string, ok bool) audit.Entry {
	t.Helper()
	data, _ := os.ReadFile(b.auditPath)
	for _, lit := range []string{literalEnv, writeLiteral, "demo-write-header"} {
		if bytes.Contains(data, []byte(lit)) {
			t.Fatalf("audit log contains the literal %q: %s", lit, data)
		}
	}
	es := b.audits(t)
	if len(es) == 0 {
		t.Fatal("no audit entry")
	}
	e := es[len(es)-1]
	if e.Tool != tool || e.Target != target || e.OK != ok || e.Client != "demo-client" || e.Dir != "/work/demo" || e.Time.IsZero() {
		t.Fatalf("audit entry = %+v, want tool %s target %s ok %v", e, tool, target, ok)
	}
	if ok != (e.Error == "") {
		t.Fatalf("audit error = %q with ok %v", e.Error, ok)
	}
	return e
}

func TestWriteToolsOnlyWithWriteAccess(t *testing.T) {
	writes := []string{"skilltri_accept_plugin_server", "skilltri_add_rule", "skilltri_add_server", "skilltri_enable_plugin_server",
		"skilltri_remove_rule", "skilltri_remove_server", "skilltri_restart_server", "skilltri_set_adapter", "skilltri_set_enabled",
		"skilltri_set_rules", "skilltri_update_server"}
	all := map[string]Tool{}
	for _, tl := range Tools(config.AgentAccessWrite) {
		all[tl.Def.Name] = tl
	}
	for _, n := range writes {
		if tl, ok := all[n]; !ok || !tl.Write {
			t.Errorf("%s missing or not a write tool", n)
		}
		if wantNote := "within seconds"; n == "skilltri_restart_server" {
			if !strings.Contains(all[n].Def.Description, "config is not changed") {
				t.Errorf("%s: description misses its effect note", n)
			}
		} else if !strings.Contains(all[n].Def.Description, wantNote) {
			t.Errorf("%s: description does not say how sessions get the change", n)
		}
	}
	for _, tl := range Tools(config.AgentAccessRead) {
		if strings.Contains(strings.Join(writes, " "), tl.Def.Name) {
			t.Errorf("read access offers %s", tl.Def.Name)
		}
	}
	for n := range all {
		if strings.Contains(n, "access") {
			t.Errorf("tool %s may touch agentAccess", n)
		}
	}
}

func TestAddServer(t *testing.T) {
	b := writeBackend(t)
	text := mustWrite(t, b, "skilltri_add_server", map[string]any{"name": "docs", "type": "http", "url": "https://mcp.example.test/mcp",
		"headers": map[string]string{"Authorization": "Bearer demo-write-header", "X-Key": "${secret:docs-key}"}, "requestTimeoutMs": 5000,
		"perSession": true, "disabled": true})
	if !strings.Contains(text, `"docs"`) || strings.Contains(text, "demo-write-header") {
		t.Fatalf("result = %s", text)
	}
	s := b.load(t).Servers["docs"]
	if s == nil || s.Type != "http" || s.URL != "https://mcp.example.test/mcp" || s.Headers["Authorization"] != "Bearer demo-write-header" ||
		s.Headers["X-Key"] != "${secret:docs-key}" || s.RequestTimeoutMs != 5000 || !s.PerSession || !s.Disabled {
		t.Fatalf("server = %+v", s)
	}
	e := lastAudit(t, b, "skilltri_add_server", "server/docs", true)
	if !strings.Contains(e.Summary, "Authorization") {
		t.Errorf("summary = %q, want the header names", e.Summary)
	}

	mustWrite(t, b, "skilltri_add_server", map[string]any{"name": "local", "type": "stdio", "command": "demo-cmd", "args": []string{"--token", writeLiteral},
		"env": map[string]string{"SECRET": writeLiteral}, "cwd": "/work"})
	if s := b.load(t).Servers["local"]; s == nil || s.Command != "demo-cmd" || len(s.Args) != 2 || s.Env["SECRET"] != writeLiteral || s.Cwd != "/work" {
		t.Fatalf("local = %+v", s)
	}
	lastAudit(t, b, "skilltri_add_server", "server/local", true)
}

func TestWriteValidationErrors(t *testing.T) {
	b := writeBackend(t)
	b.restartErr = errors.New(`no such server "nope"`)
	cases := []struct {
		tool, target string
		args         any
	}{
		{"skilltri_add_server", "server/skilltri", map[string]any{"name": "skilltri", "type": "stdio", "command": "x"}},
		{"skilltri_add_server", "server/bad", map[string]any{"name": "bad", "type": "bogus", "env": map[string]string{"K": writeLiteral}}},
		{"skilltri_add_server", "server/bad", map[string]any{"name": "bad", "type": "stdio"}},
		{"skilltri_add_server", "server/demo", map[string]any{"name": "demo", "type": "stdio", "command": "x"}},
		{"skilltri_add_server", "server/bad name", map[string]any{"name": "bad name", "type": "stdio", "command": "x"}},
		{"skilltri_add_server", "", map[string]any{"name": "x", "type": "stdio", "command": "x", "unknown": 1}},
		{"skilltri_add_server", "", map[string]any{"type": "stdio", "command": "x"}},
		{"skilltri_update_server", "server/nope", map[string]any{"name": "nope", "command": "x"}},
		{"skilltri_update_server", "server/demo", map[string]any{"name": "demo", "newName": "skilltri"}},
		{"skilltri_update_server", "server/demo", map[string]any{"name": "demo", "type": "http", "env": map[string]string{"K": writeLiteral}}},
		{"skilltri_update_server", "server/demo", map[string]any{"name": "demo", "command": "", "newName": "demo2"}},
		{"skilltri_remove_server", "server/nope", map[string]any{"name": "nope"}},
		{"skilltri_restart_server", "server/nope", map[string]any{"name": "nope"}},
		{"skilltri_set_enabled", "bogus/x", map[string]any{"target": "bogus/x", "enabled": true}},
		{"skilltri_set_enabled", "server/nope", map[string]any{"target": "server/nope", "enabled": false}},
		{"skilltri_add_rule", "server/demo", map[string]any{"target": "server/demo", "mode": "tree", "path": "relative/path"}},
		{"skilltri_add_rule", "server/demo", map[string]any{"target": "server/demo", "mode": "subtree", "path": "/a"}},
		{"skilltri_add_rule", "source/nope", map[string]any{"target": "source/nope", "mode": "tree", "path": "/a"}},
		{"skilltri_remove_rule", "server/demo", map[string]any{"target": "server/demo", "index": 2, "mode": "tree", "path": "/work/demo"}},
		{"skilltri_remove_rule", "server/demo", map[string]any{"target": "server/demo", "index": 1, "mode": "tree", "path": "/stale"}},
		{"skilltri_set_rules", "server/demo", map[string]any{"target": "server/demo", "rules": []map[string]string{{"mode": "only", "path": "rel"}}}},
		{"skilltri_set_adapter", "adapter/nope", map[string]any{"id": "nope", "enabled": true}},
		{"skilltri_set_adapter", "adapter/claude-code", map[string]any{"id": "claude-code", "enabled": false}},
		{"skilltri_enable_plugin_server", "m/writing", map[string]any{"ref": "m/writing"}},
		{"skilltri_enable_plugin_server", "m/writing/missing", map[string]any{"ref": "m/writing/missing"}},
		{"skilltri_enable_plugin_server", "m/writing/notes", map[string]any{"ref": "m/writing/notes", "name": "demo"}},
		{"skilltri_enable_plugin_server", "m/writing/notes", map[string]any{"ref": "m/writing/notes", "name": "skilltri"}},
		{"skilltri_accept_plugin_server", "server/nope", map[string]any{"name": "nope"}},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%s %v", c.tool, c.args), func(t *testing.T) {
			before := b.raw(t)
			n := len(b.audits(t))
			text, isErr := call(t, b, demoCall, c.tool, c.args)
			if !isErr {
				t.Fatalf("no error: %s", text)
			}
			if strings.Contains(text, writeLiteral) || strings.Contains(text, literalEnv) {
				t.Fatalf("error shows a literal: %s", text)
			}
			if !bytes.Equal(before, b.raw(t)) {
				t.Fatalf("config changed:\n%s", b.raw(t))
			}
			if got := len(b.audits(t)); got != n+1 {
				t.Fatalf("%d new audit entries, want 1", got-n)
			}
			if e := lastAudit(t, b, c.tool, c.target, false); e.Error != text {
				t.Fatalf("audit error %q, result %q", e.Error, text)
			}
		})
	}
}

func TestUpdateServerOnlyGivenFields(t *testing.T) {
	b := writeBackend(t)
	before := *b.load(t).Servers["demo"]
	mustWrite(t, b, "skilltri_update_server", map[string]any{"name": "demo", "command": "new-cmd"})
	s := b.load(t).Servers["demo"]
	if s.Command != "new-cmd" || s.Type != "stdio" || len(s.Args) != 1 || s.Env["API_KEY"] != literalEnv || !s.Disabled ||
		len(s.Rules) != 1 || s.Tools["delete"] == nil || s.Origin == nil || s.Origin.DefHash != before.Origin.DefHash {
		t.Fatalf("after command update: %+v", s)
	}
	e := lastAudit(t, b, "skilltri_update_server", "server/demo", true)
	if !strings.Contains(e.Summary, "command") || strings.Contains(e.Summary, "args") {
		t.Errorf("summary = %q", e.Summary)
	}

	mustWrite(t, b, "skilltri_update_server", map[string]any{"name": "demo", "disabled": false, "args": []string{}})
	if s := b.load(t).Servers["demo"]; s.Disabled || len(s.Args) != 0 || s.Command != "new-cmd" {
		t.Fatalf("after disabled update: %+v", s)
	}

	// a masked value as list_servers shows it keeps the stored literal
	mustWrite(t, b, "skilltri_update_server", map[string]any{"name": "demo", "env": map[string]string{"API_KEY": "•••", "NEW": "${secret:new}"}})
	if env := b.load(t).Servers["demo"].Env; len(env) != 2 || env["API_KEY"] != literalEnv || env["NEW"] != "${secret:new}" {
		t.Fatalf("env = %v", env)
	}
	mustWrite(t, b, "skilltri_update_server", map[string]any{"name": "demo", "env": map[string]string{"API_KEY": writeLiteral}})
	if env := b.load(t).Servers["demo"].Env; len(env) != 1 || env["API_KEY"] != writeLiteral {
		t.Fatalf("env = %v", env)
	}

	mustWrite(t, b, "skilltri_update_server", map[string]any{"name": "demo", "newName": "demo2", "cwd": "/work"})
	c := b.load(t)
	if c.Servers["demo"] != nil || c.Servers["demo2"] == nil || c.Servers["demo2"].Cwd != "/work" || len(c.Servers["demo2"].Rules) != 1 {
		t.Fatalf("after rename: %+v", c.Servers)
	}
	lastAudit(t, b, "skilltri_update_server", "server/demo", true)
	mustWrite(t, b, "skilltri_update_server", map[string]any{"name": "demo2", "newName": "demo3"})
	if c := b.load(t); c.Servers["demo3"] == nil || c.Servers["demo3"].Command != "new-cmd" {
		t.Fatalf("after pure rename: %+v", c.Servers)
	}
}

func TestRemoveAndRestartServer(t *testing.T) {
	b := writeBackend(t)
	mustWrite(t, b, "skilltri_restart_server", map[string]any{"name": "demo"})
	if len(b.restarts) != 1 || b.restarts[0] != "demo" {
		t.Fatalf("restarts = %v", b.restarts)
	}
	lastAudit(t, b, "skilltri_restart_server", "server/demo", true)
	mustWrite(t, b, "skilltri_remove_server", map[string]any{"name": "demo"})
	if b.load(t).Servers["demo"] != nil {
		t.Fatal("demo still configured")
	}
	lastAudit(t, b, "skilltri_remove_server", "server/demo", true)
}

func TestSetEnabled(t *testing.T) {
	b := writeBackend(t)
	mustWrite(t, b, "skilltri_set_enabled", map[string]any{"target": "server/demo", "enabled": true})
	mustWrite(t, b, "skilltri_set_enabled", map[string]any{"target": "server/demo/tool/read", "enabled": false})
	mustWrite(t, b, "skilltri_set_enabled", map[string]any{"target": "source/m/skill/outline", "enabled": false})
	text := mustWrite(t, b, "skilltri_set_enabled", map[string]any{"target": "source/m/plugin/nosuch", "enabled": false})
	if !strings.Contains(text, "warning:") || !strings.Contains(text, "nosuch") {
		t.Errorf("result has no unknown-plugin warning: %s", text)
	}
	lastAudit(t, b, "skilltri_set_enabled", "source/m/plugin/nosuch", true)
	c := b.load(t)
	if s := c.Servers["demo"]; s.Disabled || s.Tools["read"] == nil || !s.Tools["read"].Disabled {
		t.Fatalf("demo = %+v", s)
	}
	if m := c.Sources["m"]; m.Skills["outline"] == nil || !m.Skills["outline"].Disabled || m.Plugins["nosuch"] == nil || !m.Plugins["nosuch"].Disabled {
		t.Fatalf("source m = %+v", m)
	}
}

func TestRuleTools(t *testing.T) {
	b := writeBackend(t)
	mustWrite(t, b, "skilltri_add_rule", map[string]any{"target": "server/demo", "mode": "not-tree", "path": "/work/demo/vendor"})
	lastAudit(t, b, "skilltri_add_rule", "server/demo", true)
	want := []rules.Rule{{Path: "/work/demo", Mode: rules.Tree}, {Path: "/work/demo/vendor", Mode: rules.NotTree}}
	if got := b.load(t).Servers["demo"].Rules; !equalRules(got, want) {
		t.Fatalf("rules = %v", got)
	}
	mustWrite(t, b, "skilltri_remove_rule", map[string]any{"target": "server/demo", "index": 1, "mode": "tree", "path": "/work/demo"})
	lastAudit(t, b, "skilltri_remove_rule", "server/demo", true)
	if got := b.load(t).Servers["demo"].Rules; !equalRules(got, want[1:]) {
		t.Fatalf("rules after remove = %v", got)
	}
	mustWrite(t, b, "skilltri_set_rules", map[string]any{"target": "server/demo/tool/read", "rules": []map[string]string{{"mode": "only", "path": "/a"}, {"mode": "tree", "path": "~/b"}}})
	lastAudit(t, b, "skilltri_set_rules", "server/demo/tool/read", true)
	if got := b.load(t).Servers["demo"].Tools["read"].Rules; !equalRules(got, []rules.Rule{{Path: "/a", Mode: rules.Only}, {Path: "~/b", Mode: rules.Tree}}) {
		t.Fatalf("tool rules = %v", got)
	}
	mustWrite(t, b, "skilltri_set_rules", map[string]any{"target": "server/demo/tool/read", "rules": []map[string]string{}})
	if got := b.load(t).Servers["demo"].Tools["read"].Rules; len(got) != 0 {
		t.Fatalf("tool rules after clearing = %v", got)
	}
}

func equalRules(a, b []rules.Rule) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

// Concurrent add_rule calls (several sessions at once) all land: every edit goes through
// config.Update under the config lock.
func TestConcurrentAddRule(t *testing.T) {
	b := writeBackend(t)
	const per = 10
	var wg sync.WaitGroup
	for g := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range per {
				args, _ := json.Marshal(map[string]any{"target": "server/demo", "mode": "only", "path": fmt.Sprintf("/work/g%d/%d", g, i)})
				res, err := lookup("skilltri_add_rule").Handle(t.Context(), b, demoCall, args)
				if err != nil || res.IsError {
					t.Errorf("add_rule: %v %+v", err, res)
				}
			}
		}()
	}
	wg.Wait()
	if got := len(b.load(t).Servers["demo"].Rules); got != 1+2*per {
		t.Fatalf("%d rules, want %d", got, 1+2*per)
	}
	if got := len(b.audits(t)); got != 2*per {
		t.Fatalf("%d audit entries, want %d", got, 2*per)
	}
}

func lookup(name string) Tool {
	for _, tl := range Tools(config.AgentAccessWrite) {
		if tl.Def.Name == name {
			return tl
		}
	}
	panic("no tool " + name)
}

func TestSetAdapter(t *testing.T) {
	b := writeBackend(t)
	mustWrite(t, b, "skilltri_set_adapter", map[string]any{"id": "codex", "enabled": true})
	lastAudit(t, b, "skilltri_set_adapter", "adapter/codex", true)
	if got := b.load(t).Settings.Adapters; strings.Join(got, ",") != "claude-code,codex" {
		t.Fatalf("adapters = %v", got)
	}
	mustWrite(t, b, "skilltri_set_adapter", map[string]any{"id": "claude-code", "enabled": false})
	if got := b.load(t).Settings.Adapters; strings.Join(got, ",") != "codex" {
		t.Fatalf("adapters = %v", got)
	}
	if b.load(t).Settings.AgentAccess != "" {
		t.Fatal("agentAccess changed")
	}
}

func TestPluginServerTools(t *testing.T) {
	b := writeBackend(t)
	text := mustWrite(t, b, "skilltri_enable_plugin_server", map[string]any{"ref": "m/writing/notes"})
	if !strings.Contains(text, `"writing-notes"`) {
		t.Fatalf("result = %s", text)
	}
	lastAudit(t, b, "skilltri_enable_plugin_server", "m/writing/notes", true)
	s := b.load(t).Servers["writing-notes"]
	if s == nil || s.Origin == nil || s.Env["MODE"] != "demo" {
		t.Fatalf("server = %+v", s)
	}
	mustWrite(t, b, "skilltri_enable_plugin_server", map[string]any{"ref": "m/writing/docs", "name": "docs"})
	if b.load(t).Servers["docs"] == nil {
		t.Fatal("docs not enabled")
	}

	mcpFile := filepath.Join(b.load(t).Sources["m"].Path, "plugins", "writing", ".mcp.json")
	data, _ := os.ReadFile(mcpFile)
	if err := os.WriteFile(mcpFile, bytes.Replace(data, []byte(`"MODE": "demo"`), []byte(`"MODE": "changed"`), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, b, "skilltri_accept_plugin_server", map[string]any{"name": "writing-notes"})
	lastAudit(t, b, "skilltri_accept_plugin_server", "server/writing-notes", true)
	if s := b.load(t).Servers["writing-notes"]; s.Env["MODE"] != "changed" {
		t.Fatalf("after accept: %+v", s)
	}
}

// A rule list changed between reading and writing (another session, the TUI) fails remove_rule
// with ErrChanged and leaves that other change in place.
func TestRemoveRuleConcurrentChange(t *testing.T) {
	b := writeBackend(t)
	t.Cleanup(func() { beforeRuleSet = func() {} })
	beforeRuleSet = func() {
		if _, err := actions.RuleAdd(b.env, mustTarget(t, "server/demo"), rules.Rule{Path: "/other", Mode: rules.Only}); err != nil {
			t.Error(err)
		}
	}
	text, isErr := call(t, b, demoCall, "skilltri_remove_rule", map[string]any{"target": "server/demo", "index": 1, "mode": "tree", "path": "/work/demo"})
	if !isErr || !strings.Contains(text, "changed meanwhile") {
		t.Fatalf("result = %q, isError %v", text, isErr)
	}
	if got := b.load(t).Servers["demo"].Rules; len(got) != 2 {
		t.Fatalf("rules = %v", got)
	}
	lastAudit(t, b, "skilltri_remove_rule", "server/demo", false)
}

// set_rules retries when the list changed in between and then replaces it.
func TestSetRulesRetry(t *testing.T) {
	b := writeBackend(t)
	t.Cleanup(func() { beforeRuleSet = func() {} })
	calls := 0
	beforeRuleSet = func() {
		calls++
		if calls == 1 {
			if _, err := actions.RuleAdd(b.env, mustTarget(t, "server/demo"), rules.Rule{Path: "/other", Mode: rules.Only}); err != nil {
				t.Error(err)
			}
		}
	}
	mustWrite(t, b, "skilltri_set_rules", map[string]any{"target": "server/demo", "rules": []map[string]string{{"mode": "only", "path": "/a"}}})
	if got := b.load(t).Servers["demo"].Rules; calls != 2 || !equalRules(got, []rules.Rule{{Path: "/a", Mode: rules.Only}}) {
		t.Fatalf("calls %d, rules %v", calls, got)
	}
}

func mustTarget(t *testing.T, s string) target.Target {
	t.Helper()
	tg, err := target.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return tg
}

// A failed call whose audit line cannot be written says so in its error.
func TestAuditFailureInError(t *testing.T) {
	b := writeBackend(t)
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	b.auditPath = filepath.Join(file, "audit.jsonl")
	text, isErr := call(t, b, demoCall, "skilltri_remove_server", map[string]any{"name": "nope"})
	if !isErr || !strings.Contains(text, `no such server "nope"`) || !strings.Contains(text, "audit log could not be written") {
		t.Fatalf("result = %q, isError %v", text, isErr)
	}
	text = mustWrite(t, b, "skilltri_remove_server", map[string]any{"name": "demo"})
	if !strings.Contains(text, "warning: the audit log could not be written") {
		t.Fatalf("result = %q", text)
	}
}

// Short literal values are not scrubbed from confirmations, where they would garble names;
// error texts are scrubbed of them.
func TestShortLiteralsKeepConfirmations(t *testing.T) {
	b := writeBackend(t)
	text := mustWrite(t, b, "skilltri_add_server", map[string]any{"name": "docs", "type": "stdio", "command": "x", "env": map[string]string{"K": "docs"}})
	if !strings.Contains(text, `"docs"`) {
		t.Fatalf("confirmation garbled: %s", text)
	}
	if e := lastAudit(t, b, "skilltri_add_server", "server/docs", true); !strings.Contains(e.Summary, "env [K]") {
		t.Fatalf("summary = %q", e.Summary)
	}
	text, _ = call(t, b, demoCall, "skilltri_add_server", map[string]any{"name": "demo", "type": "stdio", "command": "x", "env": map[string]string{"K": "demo"}})
	if strings.Contains(text, "demo") {
		t.Fatalf("error text shows the literal: %s", text)
	}
}
