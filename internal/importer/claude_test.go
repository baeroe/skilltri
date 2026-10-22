package importer

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baeroe/skilltri/internal/config"
)

// fakeHome copies testdata/claude-home into a temp dir and fills in the install locations.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	src := "testdata/claude-home"
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		dst := filepath.Join(home, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, info.Mode().Perm())
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		b = bytes.ReplaceAll(b, []byte("@HOME@"), []byte(home))
		return os.WriteFile(dst, b, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
	return home
}

func TestFromClaude(t *testing.T) {
	home := fakeHome(t)
	r, err := FromClaude(home)
	if err != nil {
		t.Fatal(err)
	}
	alpha := r.Sources["alpha"]
	if alpha == nil || alpha.Type != "marketplace" || alpha.URL != "demo/alpha-plugins" {
		t.Fatalf("alpha = %+v", alpha)
	}
	// Only what Claude Code has installed and enabled is on; plugins added upstream later stay off.
	if alpha.PluginsDefault != config.PluginsDisabled || len(alpha.Plugins) != 1 {
		t.Fatalf("alpha plugins = %q %+v", alpha.PluginsDefault, alpha.Plugins)
	}
	if n := alpha.Plugins["on-plugin"]; n == nil || n.Disabled {
		t.Errorf("on-plugin must be explicitly enabled: %+v", n)
	}
	for _, p := range []string{"off-plugin", "proj-plugin", "missing-plugin", "added-later"} {
		if alpha.PluginEnabled(p) {
			t.Errorf("%s should be disabled", p)
		}
	}
	beta := r.Sources["beta-local"]
	if beta == nil || beta.Type != "marketplace" || beta.Path != "/demo/beta" || beta.PluginsDefault != config.PluginsDisabled ||
		len(beta.Plugins) != 1 || !beta.PluginEnabled("beta-tool") {
		t.Fatalf("beta-local = %+v", beta)
	}

	if len(r.UserSkills) != 1 || filepath.Base(r.UserSkills[0]) != "demo-skill" {
		t.Errorf("UserSkills = %v", r.UserSkills)
	}
	if got := r.Uninstall["alpha"]; len(got) != 1 || got[0] != "on-plugin@alpha" {
		t.Errorf("Uninstall[alpha] = %v", got)
	}
	if got := r.Uninstall["beta-local"]; len(got) != 1 || got[0] != "beta-tool@beta.local" {
		t.Errorf("Uninstall[beta-local] = %v", got)
	}
	if !strings.Contains(strings.Join(r.Steps, "\n"), "skilltri plugin-server list") {
		t.Errorf("steps = %v", r.Steps)
	}
	if !strings.Contains(strings.Join(r.Warnings, "\n"), "proj-plugin@alpha") {
		t.Errorf("no warning for the project-scoped plugin: %v", r.Warnings)
	}
}

func TestFromClaudeMissingMarketplaceJSON(t *testing.T) {
	home := fakeHome(t)
	if err := os.Remove(filepath.Join(home, ".claude/plugins/marketplaces/alpha/.claude-plugin/marketplace.json")); err != nil {
		t.Fatal(err)
	}
	r, err := FromClaude(home)
	if err != nil {
		t.Fatal(err)
	}
	// the enabled plugins come from Claude Code's settings, not from the plugin list
	if s := r.Sources["alpha"]; s == nil || s.PluginsDefault != config.PluginsDisabled || !s.PluginEnabled("on-plugin") || s.PluginEnabled("off-plugin") {
		t.Errorf("alpha = %+v", s)
	}
}

func TestFromClaudeNothingThere(t *testing.T) {
	r, err := FromClaude(t.TempDir())
	if err != nil || len(r.Sources) != 0 || len(r.UserSkills) != 0 {
		t.Errorf("r = %+v, err = %v", r, err)
	}
}

func TestApplyClaude(t *testing.T) {
	home := fakeHome(t)
	r, err := FromClaude(home)
	if err != nil {
		t.Fatal(err)
	}
	// a symlink inside a skill must be copied as a symlink, never followed
	skill := r.UserSkills[0]
	if err := os.Symlink("../outside", filepath.Join(skill, "link")); err != nil {
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

	var out bytes.Buffer
	if err := ApplyClaude(cfgPath, skillsDir, r, false, &out); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sources["alpha"].URL != "keep/me" {
		t.Errorf("existing source overwritten: %+v", cfg.Sources["alpha"])
	}
	if cfg.Sources["beta-local"] == nil {
		t.Error("beta-local not imported")
	}
	if l := cfg.Sources["local"]; l == nil || l.Type != "local" || l.Path != skillsDir {
		t.Errorf("local = %+v", l)
	}
	for _, f := range []string{"demo-skill/SKILL.md", "demo-skill/refs/notes.md"} {
		if _, err := os.Stat(filepath.Join(skillsDir, f)); err != nil {
			t.Errorf("not copied: %s", f)
		}
	}
	if _, err := os.Stat(filepath.Join(skillsDir, "synced")); err == nil {
		t.Error("synced was copied")
	}
	if st, err := os.Stat(filepath.Join(skillsDir, "demo-skill")); err != nil {
		t.Error(err)
	} else if st.Mode().Perm() != 0o755 {
		t.Errorf("skill folder mode = %v, want 0755", st.Mode().Perm())
	}
	if st, err := os.Stat(filepath.Join(skillsDir, "demo-skill/run.sh")); err != nil || st.Mode().Perm() != 0o755 {
		t.Errorf("mode not preserved: %v %v", st, err)
	}
	if target, err := os.Readlink(filepath.Join(skillsDir, "demo-skill/link")); err != nil || target != "../outside" {
		t.Errorf("symlink = %q, %v", target, err)
	}
	text := out.String()
	for _, want := range []string{"imported source beta-local", `skipped source "alpha"`, "copied skill demo-skill", "Next steps",
		"claude plugin uninstall beta-tool@beta.local", "remove " + filepath.Join(home, ".claude", "skills", "demo-skill")} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "uninstall on-plugin@alpha") {
		t.Errorf("skipped source got an uninstall step:\n%s", text)
	}

	// second run: skill exists → skipped with a warning; --force overwrites
	if err := os.WriteFile(filepath.Join(skillsDir, "demo-skill/SKILL.md"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := ApplyClaude(cfgPath, skillsDir, r, false, &out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(skillsDir, "demo-skill/SKILL.md")); string(b) != "edited" {
		t.Error("existing skill overwritten without --force")
	}
	if !strings.Contains(out.String(), `skipped skill "demo-skill"`) || strings.Contains(out.String(), "skills/demo-skill") {
		t.Errorf("output = %s", out.String())
	}
	out.Reset()
	if err := ApplyClaude(cfgPath, skillsDir, r, true, &out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(skillsDir, "demo-skill/SKILL.md")); string(b) == "edited" {
		t.Error("--force did not overwrite")
	}
	cfg, _ = config.Load(cfgPath)
	if cfg.Sources["alpha"].URL != "demo/alpha-plugins" {
		t.Errorf("--force did not replace alpha: %+v", cfg.Sources["alpha"])
	}
}

func TestApplyClaudeLocalSourceConflict(t *testing.T) {
	home := fakeHome(t)
	r, err := FromClaude(home)
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.Update(cfgPath, func(c *config.Config) error {
		c.Sources = map[string]*config.Source{"local": {Type: "local", Path: "/elsewhere"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := ApplyClaude(cfgPath, filepath.Join(t.TempDir(), "skills"), r, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `source "local" does not point at`) {
		t.Errorf("output = %s", out.String())
	}
}
