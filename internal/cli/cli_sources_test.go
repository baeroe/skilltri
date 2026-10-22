package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/daemon"
	"github.com/baeroe/skilltri/internal/paths"
	"github.com/baeroe/skilltri/internal/rules"
)

// sandbox points every skilltri dir and HOME at temp folders and silences stderr.
func sandbox(t *testing.T) *bytes.Buffer {
	t.Helper()
	base := t.TempDir()
	t.Setenv("HOME", filepath.Join(base, "home"))
	t.Setenv("SKILLTRI_CONFIG_DIR", filepath.Join(base, "config"))
	for _, env := range []string{"SKILLTRI_STATE_DIR", "SKILLTRI_DATA_DIR", "SKILLTRI_CACHE_DIR"} {
		t.Setenv(env, filepath.Join(base, env))
	}
	t.Setenv("SKILLTRI_SOCKET", filepath.Join(base, "none.sock"))
	var errs bytes.Buffer
	old := errOut
	errOut = &errs
	t.Cleanup(func() { errOut = old })
	return &errs
}

func loadCfg(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(paths.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// demoMarketCopy copies the testdata marketplace to a temp folder.
func demoMarketCopy(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "demo-market")
	if err := os.CopyFS(dst, os.DirFS("../marketplace/testdata/demo-market")); err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestSourceAddListRemove(t *testing.T) {
	errs := sandbox(t)
	local := t.TempDir()
	var out bytes.Buffer
	if code := SourceAdd(&out, "mine", config.Source{Type: "local", Path: local}); code != 0 {
		t.Fatalf("add local: %d %s", code, errs)
	}
	if code := SourceAdd(&out, "up", config.Source{Type: "git", URL: "https://example.com/demo/skills.git", Ref: "main", UpdateEvery: "2h"}); code != 0 {
		t.Fatalf("add git: %d %s", code, errs)
	}
	if !strings.Contains(out.String(), `run "skilltri update up" to fetch now`) {
		t.Errorf("no fetch hint: %q", out.String())
	}
	if code := SourceAdd(&out, "mine", config.Source{Type: "local", Path: local}); code != 1 {
		t.Errorf("duplicate add = %d, want 1", code)
	}
	if code := SourceAdd(&out, "bad name", config.Source{Type: "local", Path: local}); code != 1 {
		t.Errorf("invalid name = %d, want 1", code)
	}
	cfg := loadCfg(t)
	if len(cfg.Sources) != 2 || cfg.Sources["up"].Ref != "main" {
		t.Errorf("sources = %+v", cfg.Sources)
	}
	if _, err := catalog.Load(catalog.Path()); err != nil {
		t.Errorf("catalog not rebuilt offline: %v", err)
	}

	out.Reset()
	if code := SourceList(&out, true); code != 0 {
		t.Fatalf("list: %d", code)
	}
	var rows []SourceRow
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	if len(rows) != 2 || rows[0].Name != "mine" || rows[0].Type != "local" || rows[1].Name != "up" || rows[1].Ref != "main" || rows[1].Error == "" {
		t.Errorf("rows = %+v", rows)
	}
	out.Reset()
	SourceList(&out, false)
	if !strings.Contains(out.String(), "mine") || !strings.Contains(out.String(), "not fetched yet") {
		t.Errorf("human list = %q", out.String())
	}

	if code := SourceRemove(&out, "up"); code != 0 {
		t.Fatalf("remove: %d", code)
	}
	if code := SourceRemove(&out, "up"); code != 1 {
		t.Errorf("remove again = %d, want 1", code)
	}
	if _, ok := loadCfg(t).Sources["up"]; ok {
		t.Error("source still there")
	}
}

func TestSkillNew(t *testing.T) {
	errs := sandbox(t)
	var out bytes.Buffer
	if code := SkillNew(&out, "my-skill"); code != 0 {
		t.Fatalf("new: %d %s", code, errs)
	}
	file := filepath.Join(paths.ConfigDir(), "skills", "my-skill", "SKILL.md")
	if !strings.Contains(out.String(), file) {
		t.Errorf("output = %q", out.String())
	}
	b, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(b), "name: my-skill") || !strings.Contains(string(b), "description: TODO") {
		t.Errorf("SKILL.md = %q (%v)", b, err)
	}
	if code := SkillNew(&out, "second"); code != 0 {
		t.Fatal("second skill failed")
	}
	if code := SkillNew(&out, "second"); code != 1 {
		t.Errorf("existing skill = %d, want 1", code)
	}
	if code := SkillNew(&out, "Bad_Name"); code != 1 {
		t.Errorf("invalid name = %d, want 1", code)
	}
	cfg := loadCfg(t)
	src := cfg.Sources["local"]
	if len(cfg.Sources) != 1 || src == nil || src.Type != "local" || src.Path != filepath.Join(paths.ConfigDir(), "skills") {
		t.Errorf("sources = %+v", cfg.Sources)
	}
	c, err := catalog.Load(catalog.Path())
	if err != nil || len(c.Skills) != 2 {
		t.Errorf("catalog = %+v (%v)", c, err)
	}
}

func TestRuleAddRemove(t *testing.T) {
	errs := sandbox(t)
	var out bytes.Buffer
	SourceAdd(&out, "m", config.Source{Type: "marketplace", Path: demoMarketCopy(t)})
	const tgt = "source/m/plugin/writing"
	if code := RuleAdd(&out, tgt, "tree", "~/work"); code != 0 {
		t.Fatalf("add: %d %s", code, errs)
	}
	if code := RuleAdd(&out, tgt, "not", "/tmp/x"); code != 0 {
		t.Fatalf("add 2: %d %s", code, errs)
	}
	if code := RuleAdd(&out, tgt, "bogus", "/tmp/x"); code != 1 {
		t.Errorf("bad mode = %d, want 1", code)
	}
	if code := RuleAdd(&out, tgt, "tree", "relative"); code != 1 {
		t.Errorf("relative path = %d, want 1", code)
	}
	if code := RuleAdd(&out, "source/none", "tree", "/x"); code != 1 {
		t.Errorf("missing source = %d, want 1", code)
	}
	want := []rules.Rule{{Path: "~/work", Mode: rules.Tree}, {Path: "/tmp/x", Mode: rules.Not}}
	if got := loadCfg(t).Sources["m"].Plugins["writing"].Rules; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("rules = %+v", got)
	}

	out.Reset()
	if code := RuleList(&out, tgt, false); code != 0 || !strings.Contains(out.String(), "1  tree  ~/work") || !strings.Contains(out.String(), "2  not  /tmp/x") {
		t.Errorf("list = %d %q", code, out.String())
	}
	out.Reset()
	RuleList(&out, "", true)
	var all []RuleSet
	if err := json.Unmarshal(out.Bytes(), &all); err != nil || len(all) != 1 || all[0].Target != tgt || len(all[0].Rules) != 2 {
		t.Errorf("list all = %+v (%v) %s", all, err, out.String())
	}

	for _, idx := range []string{"0", "3", "x"} {
		if code := RuleRemove(&out, tgt, idx); code != 1 {
			t.Errorf("remove %q = %d, want 1", idx, code)
		}
	}
	for idx, want := range map[string]string{
		"5": `no rule "5" on ` + tgt + ` (it has 2)`,
		"x": `no rule "x" on ` + tgt + ` (it has 2)`,
	} {
		errs.Reset()
		RuleRemove(&out, tgt, idx)
		if !strings.Contains(errs.String(), want) {
			t.Errorf("remove %q message = %q, want %q", idx, errs.String(), want)
		}
	}
	errs.Reset()
	if code := RuleRemove(&out, "server/none", "x"); code != 1 || !strings.Contains(errs.String(), `no such server "none"`) {
		t.Errorf("non-numeric on missing target = %d %q", code, errs.String())
	}
	if code := RuleRemove(&out, tgt, "1"); code != 0 {
		t.Fatal("remove 1 failed")
	}
	if got := loadCfg(t).Sources["m"].Plugins["writing"].Rules; len(got) != 1 || got[0] != want[1] {
		t.Errorf("after remove: %+v", got)
	}
	if code := RuleRemove(&out, tgt, "all"); code != 0 {
		t.Fatal("remove all failed")
	}
	if got := loadCfg(t).Sources["m"].Plugins["writing"].Rules; len(got) != 0 {
		t.Errorf("after remove all: %+v", got)
	}
}

func TestEnableDisable(t *testing.T) {
	errs := sandbox(t)
	var out bytes.Buffer
	SourceAdd(&out, "m", config.Source{Type: "marketplace", Path: demoMarketCopy(t)})
	if err := config.Update(paths.ConfigFile(), func(c *config.Config) error {
		c.Servers["docs"] = &config.Server{Type: "http", URL: "https://example.com/mcp"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, tgt := range []string{"server/docs", "server/docs/tool/search", "source/m", "source/m/plugin/writing", "source/m/skill/writing-tools:outline"} {
		if code := SetDisabled(&out, tgt, true); code != 0 {
			t.Fatalf("disable %s: %d %s", tgt, code, errs)
		}
	}
	cfg := loadCfg(t)
	if !cfg.Servers["docs"].Disabled || !cfg.Servers["docs"].Tools["search"].Disabled || !cfg.Sources["m"].Disabled ||
		!cfg.Sources["m"].Plugins["writing"].Disabled || !cfg.Sources["m"].Skills["writing-tools:outline"].Disabled {
		t.Errorf("not all disabled: %+v", cfg)
	}
	if code := SetDisabled(&out, "source/m", false); code != 0 {
		t.Fatal("enable failed")
	}
	if loadCfg(t).Sources["m"].Disabled {
		t.Error("source still disabled")
	}
	if code := SetDisabled(&out, "server/nope", true); code != 1 {
		t.Errorf("missing server = %d, want 1", code)
	}
	if code := SetDisabled(&out, "garbage", true); code != 2 {
		t.Errorf("bad target = %d, want 2", code)
	}
}

func TestAdapterEnableDisable(t *testing.T) {
	errs := sandbox(t)
	if err := os.MkdirAll(filepath.Join(os.Getenv("HOME"), ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := AdapterList(&out, true); code != 0 {
		t.Fatal("list failed")
	}
	var rows []AdapterRow
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	byID := map[string]AdapterRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	if !byID["claude-code"].Enabled || byID["codex"].Enabled || !byID["codex"].Detected || byID["cursor"].Detected || byID["codex"].Method != "project-dir" {
		t.Errorf("rows = %+v", rows)
	}
	if code := AdapterSet(&out, "codex", true); code != 0 {
		t.Fatalf("enable: %d %s", code, errs)
	}
	if got := loadCfg(t).Settings.Adapters; len(got) != 2 || got[0] != "claude-code" || got[1] != "codex" {
		t.Errorf("adapters = %v", got)
	}
	if code := AdapterSet(&out, "codex", true); code != 0 || len(loadCfg(t).Settings.Adapters) != 2 {
		t.Error("enabling twice must be a no-op")
	}
	if code := AdapterSet(&out, "claude-code", false); code != 0 {
		t.Fatal("disable failed")
	}
	if got := loadCfg(t).Settings.Adapters; len(got) != 1 || got[0] != "codex" {
		t.Errorf("adapters = %v", got)
	}
	if code := AdapterSet(&out, "codex", false); code != 1 {
		t.Errorf("disabling the last adapter = %d, want 1", code)
	}
	if code := AdapterSet(&out, "nope", true); code != 2 {
		t.Errorf("unknown id = %d, want 2", code)
	}
}

func TestPluginServerEnableAccept(t *testing.T) {
	errs := sandbox(t)
	market := demoMarketCopy(t)
	var out bytes.Buffer
	SourceAdd(&out, "m", config.Source{Type: "marketplace", Path: market})
	RuleAdd(&out, "source/m", "tree", "/srv/src")
	RuleAdd(&out, "source/m/plugin/writing", "tree", "/srv/plugin")

	out.Reset()
	PluginServerList(&out, true)
	var rows []PluginServerRow
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 2 || rows[0].State != "available" {
		t.Fatalf("rows = %+v (%v)", rows, err)
	}

	if code := PluginServerEnable(&out, "m/writing/notes", ""); code != 0 {
		t.Fatalf("enable: %d %s", code, errs)
	}
	cfg := loadCfg(t)
	srv := cfg.Servers["writing-notes"]
	if srv == nil || srv.Origin == nil || srv.Origin.Source != "m" || srv.Origin.Plugin != "writing" || srv.Origin.Server != "notes" || srv.Origin.DefHash == "" {
		t.Fatalf("server = %+v", srv)
	}
	if len(srv.Rules) != 1 || srv.Rules[0].Path != "/srv/plugin" {
		t.Errorf("rules = %+v, want the plugin's", srv.Rules)
	}
	if code := PluginServerEnable(&out, "m/writing/notes", ""); code != 1 {
		t.Errorf("existing name = %d, want 1", code)
	}
	if code := PluginServerEnable(&out, "m/writing/docs", "custom"); code != 0 || loadCfg(t).Servers["custom"] == nil {
		t.Error("--name not honored")
	}
	if code := PluginServerEnable(&out, "m/writing/missing", ""); code != 1 {
		t.Errorf("unknown server = %d, want 1", code)
	}
	if code := PluginServerEnable(&out, "m/writing", ""); code != 2 {
		t.Errorf("bad reference = %d, want 2", code)
	}

	// The plugin changes the server definition.
	mcp := filepath.Join(market, "plugins", "writing", ".mcp.json")
	b, _ := os.ReadFile(mcp)
	if err := os.WriteFile(mcp, bytes.Replace(b, []byte(`"MODE": "demo"`), []byte(`"MODE": "changed"`), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	PluginServerList(&out, true)
	rows = nil
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, r := range rows {
		states[r.Server] = r.State
	}
	if states["notes"] != "needs-review" || states["docs"] != "enabled" {
		t.Errorf("states = %v", states)
	}
	out.Reset()
	PluginServerList(&out, false)
	if !strings.Contains(out.String(), "needs-review") || !strings.Contains(out.String(), "enabled as custom") {
		t.Errorf("human list = %q", out.String())
	}
	before := loadCfg(t).Servers["writing-notes"]
	if before.Env["MODE"] != "demo" {
		t.Fatalf("definition changed before accept: %+v", before.Env)
	}

	if code := PluginServerAccept(&out, "writing-notes"); code != 0 {
		t.Fatalf("accept: %d %s", code, errs)
	}
	after := loadCfg(t).Servers["writing-notes"]
	if after.Env["MODE"] != "changed" || after.Origin.DefHash == before.Origin.DefHash || len(after.Rules) != 1 {
		t.Errorf("after accept: %+v origin %+v", after, after.Origin)
	}
	c, _ := catalog.Load(catalog.Path())
	if st := catalog.ReviewState(loadCfg(t), c)["writing-notes"]; st != "ok" {
		t.Errorf("review state = %q", st)
	}
	if code := PluginServerAccept(&out, "nope"); code != 1 {
		t.Errorf("unknown = %d, want 1", code)
	}
	if err := config.Update(paths.ConfigFile(), func(c *config.Config) error {
		c.Servers["plain"] = &config.Server{Type: "http", URL: "https://example.com/mcp"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if code := PluginServerAccept(&out, "plain"); code != 1 {
		t.Errorf("no origin = %d, want 1", code)
	}

	// A disabled source is reported as such, not as gone.
	SetDisabled(&out, "source/m", true)
	out.Reset()
	PluginServerList(&out, true)
	rows = nil
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.State != "source disabled" {
			t.Errorf("row with the source disabled: %+v", r)
		}
	}
	errs.Reset()
	if code := PluginServerAccept(&out, "writing-notes"); code != 1 || !strings.Contains(errs.String(), "source disabled") {
		t.Errorf("accept with the source disabled: %d %s", code, errs)
	}
}

func TestWhichSkills(t *testing.T) {
	sandbox(t)
	var out bytes.Buffer
	local, other := t.TempDir(), t.TempDir()
	for _, n := range []string{"alpha", "beta"} {
		if err := os.MkdirAll(filepath.Join(local, n), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(local, n, "SKILL.md"), []byte("---\nname: "+n+"\ndescription: Demo "+n+".\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	SourceAdd(&out, "mine", config.Source{Type: "local", Path: local})
	dir := rules.Normalize(t.TempDir())
	RuleAdd(&out, "source/mine/skill/beta", "only", other)

	out.Reset()
	if code := Which(&out, dir, true); code != 0 {
		t.Fatal("which failed")
	}
	var r daemon.WhichReply
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Skills) != 2 || r.Skills[0].ID != "alpha" || r.Skills[0].Source != "mine" || !r.Skills[0].Decision.Active || r.Skills[1].ID != "beta" || r.Skills[1].Decision.Active {
		t.Errorf("skills = %+v", r.Skills)
	}
	out.Reset()
	Which(&out, dir, false)
	s := out.String()
	if !strings.Contains(s, "skills:") || !strings.Contains(s, "✓ alpha") || !strings.Contains(s, "✗ beta") {
		t.Errorf("human output:\n%s", s)
	}
}

func TestUnknownTargetWarnings(t *testing.T) {
	errs := sandbox(t)
	var out bytes.Buffer
	SourceAdd(&out, "m", config.Source{Type: "marketplace", Path: demoMarketCopy(t)})
	for _, tc := range []struct {
		do   func() int
		want []string // "" = no warning
	}{
		{func() int { return RuleAdd(&out, "source/m/plugin/writing", "tree", "/x") }, nil},
		{func() int { return SetDisabled(&out, "source/m/skill/writing-tools:outline", true) }, nil},
		// the plugin.json name instead of the entry name
		{func() int { return RuleAdd(&out, "source/m/plugin/writing-tools", "tree", "/x") },
			[]string{`no plugin "writing-tools"`, "marketplace entry name", "source/m/plugin/writing"}},
		{func() int { return SetDisabled(&out, "source/m/plugin/writin", false) }, []string{"source/m/plugin/writing"}},
		// a plugin skill without its prefix
		{func() int { return SetDisabled(&out, "source/m/skill/outline", true) },
			[]string{`no skill "outline"`, "<plugin>:<skill>", "source/m/skill/writing-tools:outline"}},
		{func() int { return RuleAdd(&out, "source/m/skill/nothing-like-it", "tree", "/x") }, []string{`no skill "nothing-like-it"`}},
	} {
		errs.Reset()
		if code := tc.do(); code != 0 {
			t.Fatalf("exit %d: %s", code, errs)
		}
		if len(tc.want) == 0 && errs.Len() > 0 {
			t.Errorf("unexpected warning: %s", errs)
		}
		for _, w := range tc.want {
			if !strings.Contains(errs.String(), w) {
				t.Errorf("warning lacks %q: %s", w, errs)
			}
		}
	}
}

func TestRenderWhichPluginSkill(t *testing.T) {
	var out bytes.Buffer
	renderWhich(&out, daemon.WhichReply{Dir: "/w", Skills: []daemon.WhichSkill{
		{ID: "writing-tools:outline", Source: "m", Plugin: "writing", Decision: rules.Decision{Active: true}},
		{ID: "alpha", Source: "loc", Decision: rules.Decision{Active: true}},
	}})
	for _, want := range []string{"writing-tools:outline  (m, plugin writing)", "alpha  (loc)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

// TestRefreshCatalogAnyDialError: a socket that cannot even be dialed (here a path too long for a
// unix socket) means no daemon, so the CLI rebuilds the catalog itself.
func TestRefreshCatalogAnyDialError(t *testing.T) {
	sandbox(t)
	t.Setenv("SKILLTRI_SOCKET", filepath.Join(t.TempDir(), strings.Repeat("x", 120), "d.sock"))
	local := t.TempDir()
	if err := os.MkdirAll(filepath.Join(local, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "alpha", "SKILL.md"), []byte("---\nname: alpha\ndescription: Demo.\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := SourceAdd(&out, "mine", config.Source{Type: "local", Path: local}); code != 0 {
		t.Fatal("add failed")
	}
	c, err := catalog.Load(catalog.Path())
	if err != nil || len(c.Skills) != 1 {
		t.Fatalf("catalog not rebuilt: %+v %v", c, err)
	}
}
