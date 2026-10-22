package importer

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/secrets"
)

const demo1MCP = `{"mcpServers":{
 "a":{"url":"https://a.example.com/mcp","headers":{"Authorization":"tok-a"}},
 "b":{"command":"demo","env":{"B_TOKEN":"tok-b"}}}}`

func TestApply1MCP(t *testing.T) {
	keyring.MockInit()
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.Update(cfgPath, func(c *config.Config) error {
		c.Servers = map[string]*config.Server{"a": {Type: "stdio", Command: "old"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Set("a-authorization", "keep-me"); err != nil {
		t.Fatal(err)
	}
	res, err := From1MCP([]byte(demo1MCP))
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(cfgPath)
	p := Preview1MCP(cfg, res, false)
	if !reflect.DeepEqual(p.Imported, []string{"b"}) || len(p.Replaced) != 0 || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], `skipped "a": already exists`) {
		t.Fatalf("preview = %+v", p)
	}
	if fp := Preview1MCP(cfg, res, true); !reflect.DeepEqual(fp.Imported, []string{"a", "b"}) || !reflect.DeepEqual(fp.Replaced, []string{"a"}) {
		t.Fatalf("force preview = %+v", fp)
	}
	if cfg.Servers["a"].Command != "old" || cfg.Servers["b"] != nil {
		t.Fatal("preview changed the config")
	}

	got, err := Apply1MCP(cfgPath, res, false)
	if err != nil || !reflect.DeepEqual(got, p) {
		t.Fatalf("apply = %+v, %v; want the preview %+v", got, err, p)
	}
	cfg, _ = config.Load(cfgPath)
	if cfg.Servers["a"].Command != "old" || cfg.Servers["b"] == nil {
		t.Errorf("servers = %v", cfg.Servers)
	}
	if v, _ := secrets.Get("a-authorization"); v != "keep-me" {
		t.Errorf("skipped server's secret overwritten: %q", v)
	}
	if v, _ := secrets.Get("b-b_token"); v != "tok-b" {
		t.Errorf("b secret = %q", v)
	}
	if _, err := Apply1MCP(cfgPath, res, true); err != nil {
		t.Fatal(err)
	}
	cfg, _ = config.Load(cfgPath)
	if cfg.Servers["a"].Type != "http" {
		t.Errorf("force did not overwrite: %+v", cfg.Servers["a"])
	}
	if v, _ := secrets.Get("a-authorization"); v != "tok-a" {
		t.Errorf("forced secret = %q", v)
	}
}

func TestPreviewClaudeMatchesImport(t *testing.T) {
	home := fakeHome(t)
	r, err := FromClaude(home)
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	skillsDir := filepath.Join(t.TempDir(), "skills")
	if err := config.Update(cfgPath, func(c *config.Config) error {
		c.Sources = map[string]*config.Source{"alpha": {Type: "marketplace", URL: "keep/me"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(cfgPath)
	p := PreviewClaude(cfg, skillsDir, r, false)
	if !reflect.DeepEqual(p.Sources, []string{"beta-local"}) || !p.LocalAdded || !reflect.DeepEqual(p.Skills, []string{"demo-skill"}) {
		t.Fatalf("preview = %+v", p)
	}
	if cfg.Sources["beta-local"] != nil || cfg.Sources["local"] != nil {
		t.Fatal("preview changed the config")
	}
	if _, err := os.Stat(skillsDir); err == nil {
		t.Fatal("preview copied skills")
	}
	got, err := ImportClaude(cfgPath, skillsDir, r, false)
	if err != nil || !reflect.DeepEqual(got, p) {
		t.Fatalf("import = %+v, %v\nwant the preview %+v", got, err, p)
	}
	if len(got.Steps) == 0 || !strings.Contains(strings.Join(got.Steps, "\n"), "remove "+filepath.Join(home, ".claude", "skills", "demo-skill")) {
		t.Errorf("steps = %q", got.Steps)
	}

	// now everything exists: without force all is skipped, with force alpha is replaced
	cfg, _ = config.Load(cfgPath)
	if p := PreviewClaude(cfg, skillsDir, r, false); len(p.Sources)+len(p.Skills) != 0 || p.LocalAdded {
		t.Errorf("second preview = %+v", p)
	}
	fp := PreviewClaude(cfg, skillsDir, r, true)
	if !reflect.DeepEqual(fp.Replaced, []string{"alpha", "beta-local"}) || !reflect.DeepEqual(fp.ReplacedSkills, []string{"demo-skill"}) {
		t.Errorf("force preview replaced = %q %q", fp.Replaced, fp.ReplacedSkills)
	}
}
