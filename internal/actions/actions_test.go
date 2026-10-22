package actions

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/rules"
	"github.com/baeroe/skilltri/internal/sources"
	"github.com/baeroe/skilltri/internal/target"
)

// testEnv returns an Env that stays inside a temp folder, plus the config path.
func testEnv(t *testing.T) Env {
	t.Helper()
	base := t.TempDir()
	return Env{
		ConfigPath:  filepath.Join(base, "config", "config.json"),
		CatalogPath: filepath.Join(base, "cache", "catalog.json"),
		Socket:      filepath.Join(base, "none.sock"),
		Builder:     &catalog.Builder{Fetcher: &sources.Fetcher{Root: filepath.Join(base, "sources")}, Lock: filepath.Join(base, "lock.json")},
	}
}

func load(t *testing.T, e Env) *config.Config {
	t.Helper()
	c, err := config.Load(e.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func raw(t *testing.T, e Env) []byte {
	t.Helper()
	b, err := os.ReadFile(e.ConfigPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return b
}

func parse(t *testing.T, s string) target.Target {
	t.Helper()
	tg, err := target.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return tg
}

func demoMarketCopy(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "demo-market")
	if err := os.CopyFS(dst, os.DirFS("../marketplace/testdata/demo-market")); err != nil {
		t.Fatal(err)
	}
	return dst
}

var stdio = config.Server{Type: "stdio", Command: "demo-server"}

func TestServerActions(t *testing.T) {
	e := testEnv(t)
	if err := ServerAdd(e, "alpha", stdio); err != nil {
		t.Fatal(err)
	}
	if err := ServerAdd(e, "alpha", stdio); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate add = %v, want ErrExists", err)
	}

	// Validation errors leave the file byte-identical.
	before := raw(t, e)
	for _, tc := range []struct {
		name string
		s    config.Server
	}{
		{"bad name", stdio},
		{"skilltri", stdio},
		{"beta", config.Server{Type: "stdio"}},
		{"beta", config.Server{Type: "http"}},
		{"beta", config.Server{Type: "ftp", Command: "x"}},
	} {
		if err := ServerAdd(e, tc.name, tc.s); err == nil {
			t.Errorf("ServerAdd(%q, %+v) succeeded", tc.name, tc.s)
		}
	}
	if !bytes.Equal(before, raw(t, e)) {
		t.Error("config changed by invalid adds")
	}

	// Update keeps disabled, and rules, tools and origin unless set.
	err := config.Update(e.ConfigPath, func(c *config.Config) error {
		s := c.Servers["alpha"]
		s.Disabled = true
		s.Rules = []rules.Rule{{Path: "/w", Mode: rules.Tree}}
		s.Tools = map[string]*config.Tool{"ping": {Disabled: true}}
		s.Origin = &config.Origin{Source: "m", Plugin: "p", Server: "s"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ServerUpdate(e, "alpha", config.Server{Type: "http", URL: "https://example.com/mcp"}); err != nil {
		t.Fatal(err)
	}
	s := load(t, e).Servers["alpha"]
	if s.Type != "http" || s.URL != "https://example.com/mcp" || s.Command != "" {
		t.Errorf("connection not replaced: %+v", s)
	}
	if len(s.Rules) != 1 || !s.Tools["ping"].Disabled || s.Origin == nil || s.Origin.Source != "m" || !s.Disabled {
		t.Errorf("disabled/rules/tools/origin lost: %+v", s)
	}
	if err := ServerUpdate(e, "alpha", config.Server{Type: "http", URL: "https://example.com/mcp", Disabled: false}); err != nil || !load(t, e).Servers["alpha"].Disabled {
		t.Errorf("update changed disabled: %v", err)
	}
	newRules := []rules.Rule{{Path: "/other", Mode: rules.Not}}
	if err := ServerUpdate(e, "alpha", config.Server{Type: "stdio", Command: "x", Rules: newRules}); err != nil {
		t.Fatal(err)
	}
	if got := load(t, e).Servers["alpha"].Rules; len(got) != 1 || got[0].Path != "/other" {
		t.Errorf("passed rules not used: %+v", got)
	}
	if err := ServerUpdate(e, "nope", stdio); !errors.Is(err, ErrNotFound) {
		t.Errorf("update missing = %v, want ErrNotFound", err)
	}
	before = raw(t, e)
	if err := ServerUpdate(e, "alpha", config.Server{Type: "stdio"}); err == nil {
		t.Error("invalid update succeeded")
	}
	if !bytes.Equal(before, raw(t, e)) {
		t.Error("config changed by invalid update")
	}

	// Rename keeps everything.
	if err := ServerRename(e, "alpha", "gamma"); err != nil {
		t.Fatal(err)
	}
	c := load(t, e)
	if c.Servers["alpha"] != nil || c.Servers["gamma"] == nil || len(c.Servers["gamma"].Rules) != 1 || c.Servers["gamma"].Tools["ping"] == nil {
		t.Errorf("rename lost data: %+v", c.Servers)
	}
	if err := ServerAdd(e, "delta", stdio); err != nil {
		t.Fatal(err)
	}
	before = raw(t, e)
	if err := ServerRename(e, "gamma", "delta"); !errors.Is(err, ErrExists) {
		t.Errorf("rename onto existing = %v, want ErrExists", err)
	}
	if err := ServerRename(e, "nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rename missing = %v, want ErrNotFound", err)
	}
	if err := ServerRename(e, "gamma", "bad name"); err == nil {
		t.Error("rename to invalid name succeeded")
	}
	if !bytes.Equal(before, raw(t, e)) {
		t.Error("config changed by failed renames")
	}

	if err := ServerRemove(e, "delta"); err != nil {
		t.Fatal(err)
	}
	if err := ServerRemove(e, "delta"); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove missing = %v, want ErrNotFound", err)
	}
	if load(t, e).Servers["delta"] != nil {
		t.Error("delta still there")
	}
}

func TestSourceActions(t *testing.T) {
	e := testEnv(t)
	local := t.TempDir()
	if err := SourceAdd(e, "mine", config.Source{Type: "local", Path: local}); err != nil {
		t.Fatal(err)
	}
	if err := SourceAdd(e, "mine", config.Source{Type: "local", Path: local}); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate = %v, want ErrExists", err)
	}
	before := raw(t, e)
	for name, s := range map[string]config.Source{
		"bad name": {Type: "local", Path: local},
		"nopath":   {Type: "local"},
		"nourl":    {Type: "git"},
		"badtype":  {Type: "svn", URL: "x"},
	} {
		if err := SourceAdd(e, name, s); err == nil {
			t.Errorf("SourceAdd(%q) succeeded", name)
		}
	}
	if !bytes.Equal(before, raw(t, e)) {
		t.Error("config changed by invalid adds")
	}

	if err := addTreeRule(t, e, "source/mine", "/w"); err != nil {
		t.Fatal(err)
	}
	if err := SourceUpdate(e, "mine", config.Source{Type: "git", URL: "https://example.com/demo/skills.git", Ref: "main"}); err != nil {
		t.Fatal(err)
	}
	s := load(t, e).Sources["mine"]
	if s.Type != "git" || s.Path != "" || s.Ref != "main" || len(s.Rules) != 1 {
		t.Errorf("update = %+v", s)
	}
	if _, err := SetDisabled(e, parse(t, "source/mine"), true); err != nil {
		t.Fatal(err)
	}
	if err := SourceUpdate(e, "mine", config.Source{Type: "git", URL: "https://example.com/demo/skills.git", Disabled: false}); err != nil || !load(t, e).Sources["mine"].Disabled {
		t.Errorf("update changed disabled: %v", err)
	}
	if err := SourceUpdate(e, "nope", config.Source{Type: "local", Path: local}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update missing = %v", err)
	}
	before = raw(t, e)
	if err := SourceUpdate(e, "mine", config.Source{Type: "git"}); err == nil {
		t.Error("invalid update succeeded")
	}
	if !bytes.Equal(before, raw(t, e)) {
		t.Error("config changed by invalid update")
	}

	if err := SourceRemove(e, "mine"); err != nil {
		t.Fatal(err)
	}
	if err := SourceRemove(e, "mine"); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove missing = %v", err)
	}
}

// addTreeRule adds a tree rule and returns only the error.
func addTreeRule(t *testing.T, e Env, tgt, path string) error {
	t.Helper()
	_, err := RuleAdd(e, parse(t, tgt), rules.Rule{Path: path, Mode: rules.Tree})
	return err
}

func TestSkillNew(t *testing.T) {
	e := testEnv(t)
	dir, err := SkillNew(e, "my-skill")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(filepath.Dir(e.ConfigPath), "skills", "my-skill"); dir != want {
		t.Errorf("dir = %s, want %s", dir, want)
	}
	b, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil || !strings.Contains(string(b), "name: my-skill") {
		t.Fatalf("SKILL.md = %q (%v)", b, err)
	}
	if src := load(t, e).Sources["local"]; src == nil || src.Type != "local" || src.Path != filepath.Join(filepath.Dir(e.ConfigPath), "skills") {
		t.Errorf("local source = %+v", src)
	}
	before := raw(t, e)
	if _, err := SkillNew(e, "my-skill"); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate = %v, want ErrExists", err)
	}
	if _, err := SkillNew(e, "Bad_Name"); err == nil {
		t.Error("invalid name accepted")
	}
	if !bytes.Equal(before, raw(t, e)) {
		t.Error("config changed by failed calls")
	}
}

func TestRuleActions(t *testing.T) {
	e := testEnv(t)
	if err := SourceAdd(e, "m", config.Source{Type: "marketplace", Path: demoMarketCopy(t)}); err != nil {
		t.Fatal(err)
	}
	if err := ServerAdd(e, "alpha", stdio); err != nil {
		t.Fatal(err)
	}
	plugin := parse(t, "source/m/plugin/writing")
	for _, r := range []rules.Rule{{Path: "~/work", Mode: rules.Tree}, {Path: "/tmp/x", Mode: rules.Not}} {
		if _, err := RuleAdd(e, plugin, r); err != nil {
			t.Fatal(err)
		}
	}
	if got := load(t, e).Sources["m"].Plugins["writing"].Rules; len(got) != 2 || got[1].Path != "/tmp/x" {
		t.Fatalf("rules = %+v", got)
	}

	before := raw(t, e)
	if _, err := RuleAdd(e, plugin, rules.Rule{Path: "/x", Mode: "bogus"}); err == nil {
		t.Error("bad mode accepted")
	}
	if _, err := RuleAdd(e, plugin, rules.Rule{Path: "relative", Mode: rules.Tree}); err == nil {
		t.Error("relative path accepted")
	}
	if _, err := RuleAdd(e, parse(t, "source/none"), rules.Rule{Path: "/x", Mode: rules.Tree}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing source = %v, want ErrNotFound", err)
	}
	if err := RuleRemove(e, plugin, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("index 0 = %v, want ErrNotFound", err)
	}
	if err := RuleRemove(e, plugin, 3); !errors.Is(err, ErrNotFound) {
		t.Errorf("index 3 = %v, want ErrNotFound", err)
	}
	if err := RuleRemove(e, parse(t, "server/none"), 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing server = %v, want ErrNotFound", err)
	}
	var nr *NoRuleError
	if err := RuleRemove(e, plugin, 5); !errors.As(err, &nr) || nr.Count != 2 || err.Error() != `no rule "5" on source/m/plugin/writing (it has 2)` {
		t.Errorf("out of range = %v", err)
	}
	if _, err := RuleAdd(e, target.Target{Kind: "bogus"}, rules.Rule{Path: "/x", Mode: rules.Tree}); err == nil {
		t.Error("bogus kind accepted")
	} else if _, orig := target.Resolve(&config.Config{}, target.Target{Kind: "bogus"}, true); !errors.Is(err, ErrNotFound) || err.Error() != orig.Error() {
		t.Errorf("bogus kind = %v, want ErrNotFound with the original message %v", err, orig)
	}
	if !bytes.Equal(before, raw(t, e)) {
		t.Error("config changed by failed calls")
	}

	if err := RuleRemove(e, plugin, 1); err != nil {
		t.Fatal(err)
	}
	if got := load(t, e).Sources["m"].Plugins["writing"].Rules; len(got) != 1 || got[0].Path != "/tmp/x" {
		t.Errorf("after remove = %+v", got)
	}
	if err := RuleClear(e, plugin); err != nil {
		t.Fatal(err)
	}
	if got := load(t, e).Sources["m"].Plugins["writing"].Rules; len(got) != 0 {
		t.Errorf("after clear = %+v", got)
	}

	// SetDisabled, with the warning for an unknown skill.
	if ws, err := SetDisabled(e, parse(t, "server/alpha"), true); err != nil || len(ws) != 0 || !load(t, e).Servers["alpha"].Disabled {
		t.Errorf("disable server: %v %v", ws, err)
	}
	if _, err := SetDisabled(e, parse(t, "server/alpha"), false); err != nil || load(t, e).Servers["alpha"].Disabled {
		t.Errorf("enable server: %v", err)
	}
	if _, err := SetDisabled(e, parse(t, "server/none"), true); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing server = %v, want ErrNotFound", err)
	}
	ws, err := SetDisabled(e, parse(t, "source/m/skill/outline"), true)
	if err != nil || len(ws) != 1 || !strings.Contains(ws[0], "writing-tools:outline") {
		t.Errorf("warnings = %v (%v), want a did-you-mean", ws, err)
	}
	ws, err = RuleAdd(e, parse(t, "source/m/skill/nothing-like-it"), rules.Rule{Path: "/x", Mode: rules.Tree})
	if err != nil || len(ws) != 1 || !strings.Contains(ws[0], `no skill "nothing-like-it"`) {
		t.Errorf("warnings = %v (%v)", ws, err)
	}
}

func TestSetAdapter(t *testing.T) {
	e := testEnv(t)
	if err := SetAdapter(e, "nope", true); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown = %v, want ErrInvalid", err)
	}
	if err := SetAdapter(e, "codex", true); err != nil {
		t.Fatal(err)
	}
	if got := load(t, e).Settings.Adapters; len(got) != 2 {
		t.Fatalf("adapters = %v", got)
	}
	if err := SetAdapter(e, "claude-code", false); err != nil {
		t.Fatal(err)
	}
	before := raw(t, e)
	if err := SetAdapter(e, "codex", false); err == nil || errors.Is(err, ErrInvalid) {
		t.Errorf("disabling the last adapter = %v, want a plain error", err)
	}
	if !bytes.Equal(before, raw(t, e)) {
		t.Error("config changed by failed disable")
	}
}

func TestPluginServerActions(t *testing.T) {
	e := testEnv(t)
	market := demoMarketCopy(t)
	if err := SourceAdd(e, "m", config.Source{Type: "marketplace", Path: market}); err != nil {
		t.Fatal(err)
	}
	if err := addTreeRule(t, e, "source/m/plugin/writing", "/srv/plugin"); err != nil {
		t.Fatal(err)
	}
	name, err := PluginServerEnable(e, "m/writing/notes", "")
	if err != nil || name != "writing-notes" {
		t.Fatalf("enable = %q, %v", name, err)
	}
	srv := load(t, e).Servers["writing-notes"]
	if srv == nil || srv.Origin == nil || srv.Origin.DefHash == "" || len(srv.Rules) != 1 || srv.Rules[0].Path != "/srv/plugin" {
		t.Fatalf("server = %+v", srv)
	}
	if name, err := PluginServerEnable(e, "m/writing/docs", "custom"); err != nil || name != "custom" {
		t.Errorf("custom name = %q, %v", name, err)
	}
	before := raw(t, e)
	if _, err := PluginServerEnable(e, "m/writing/notes", ""); !errors.Is(err, ErrExists) {
		t.Errorf("existing = %v, want ErrExists", err)
	}
	if _, err := PluginServerEnable(e, "m/writing/missing", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown = %v, want ErrNotFound", err)
	}
	if _, err := PluginServerEnable(e, "m/writing", ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad ref = %v, want ErrInvalid", err)
	}
	if _, err := PluginServerEnable(e, "m/writing/docs", "bad name"); err == nil {
		t.Error("bad name accepted")
	}
	if !bytes.Equal(before, raw(t, e)) {
		t.Error("config changed by failed calls")
	}

	if err := PluginServerAccept(e, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("accept missing = %v, want ErrNotFound", err)
	}
	if err := ServerAdd(e, "plain", stdio); err != nil {
		t.Fatal(err)
	}
	if err := PluginServerAccept(e, "plain"); err == nil {
		t.Error("accept of a server without origin succeeded")
	}

	mcp := filepath.Join(market, "plugins", "writing", ".mcp.json")
	b, _ := os.ReadFile(mcp)
	if err := os.WriteFile(mcp, bytes.Replace(b, []byte(`"MODE": "demo"`), []byte(`"MODE": "changed"`), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PluginServerAccept(e, "writing-notes"); err != nil {
		t.Fatal(err)
	}
	srv = load(t, e).Servers["writing-notes"]
	if srv.Env["MODE"] != "changed" || len(srv.Rules) != 1 {
		t.Errorf("after accept: %+v", srv)
	}
}

func TestRefreshCatalogOffline(t *testing.T) {
	e := testEnv(t)
	local := t.TempDir()
	if err := os.MkdirAll(filepath.Join(local, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "alpha", "SKILL.md"), []byte("---\nname: alpha\ndescription: Demo.\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SourceAdd(e, "mine", config.Source{Type: "local", Path: local}); err != nil {
		t.Fatal(err)
	}
	c, err := catalog.Load(e.CatalogPath)
	if err != nil || len(c.Skills) != 1 {
		t.Fatalf("catalog = %+v, %v", c, err)
	}
}

func TestRuleSet(t *testing.T) {
	e := testEnv(t)
	if err := ServerAdd(e, "alpha", stdio); err != nil {
		t.Fatal(err)
	}
	srv := parse(t, "server/alpha")
	want := []rules.Rule{{Path: "~/a", Mode: rules.Tree}, {Path: "/b", Mode: rules.Not}}
	if err := RuleSet(e, srv, want); err != nil {
		t.Fatal(err)
	}
	if got := load(t, e).Servers["alpha"].Rules; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("rules = %+v", got)
	}
	before := raw(t, e)
	if err := RuleSet(e, srv, []rules.Rule{{Path: "/x", Mode: rules.Only}, {Path: "relative", Mode: rules.Tree}}); err == nil {
		t.Error("relative path accepted")
	}
	if err := RuleSet(e, parse(t, "server/none"), want); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing server = %v, want ErrNotFound", err)
	}
	if !bytes.Equal(before, raw(t, e)) {
		t.Error("config changed by failed calls")
	}
	if err := RuleSet(e, parse(t, "server/alpha/tool/ping"), want[:1]); err != nil {
		t.Fatal(err)
	}
	if got := load(t, e).Servers["alpha"].Tools["ping"]; got == nil || len(got.Rules) != 1 {
		t.Errorf("tool node = %+v", got)
	}
	if err := RuleSet(e, srv, nil); err != nil || len(load(t, e).Servers["alpha"].Rules) != 0 {
		t.Errorf("clear: %v", err)
	}
}
