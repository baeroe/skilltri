package actions

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baeroe/skilltri/internal/config"
)

func TestSetAgentAccess(t *testing.T) {
	e := testEnv(t)
	if got := load(t, e).AgentAccess(); got != config.AgentAccessWrite {
		t.Fatalf("default = %q", got)
	}
	for _, lvl := range []string{config.AgentAccessOff, config.AgentAccessRead, config.AgentAccessWrite} {
		if err := SetAgentAccess(e, lvl); err != nil {
			t.Fatal(err)
		}
		if got := load(t, e).Settings.AgentAccess; got != lvl {
			t.Fatalf("agentAccess = %q, want %q", got, lvl)
		}
	}
	before := raw(t, e)
	if err := SetAgentAccess(e, "admin"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown level: %v", err)
	}
	if !bytes.Equal(before, raw(t, e)) {
		t.Error("a refused level changed the config")
	}
}

// writeSkill writes dir/name/SKILL.md.
func writeSkill(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func skillMD(name, desc string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n# " + name + "\n"
}

func TestSkillWrite(t *testing.T) {
	e := testEnv(t)
	local := t.TempDir()
	alpha := writeSkill(t, local, "alpha", skillMD("alpha", "Demo skill alpha."))
	// a git source with a checkout and a marketplace source from a folder
	gitRepo := filepath.Join(e.Builder.Fetcher.Root, "remote", "repo")
	if err := os.MkdirAll(filepath.Join(gitRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitSkill := writeSkill(t, gitRepo, "remote-skill", skillMD("remote-skill", "Demo remote skill."))
	market := demoMarketCopy(t)
	if err := config.Update(e.ConfigPath, func(c *config.Config) error {
		c.Sources = map[string]*config.Source{
			"mine":   {Type: "local", Path: local},
			"remote": {Type: "git", URL: "demo/remote"},
			"m":      {Type: "marketplace", Path: market},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	next := skillMD("alpha", "Demo skill alpha, rewritten.") + "\nNew body.\n"
	if err := SkillWrite(e, "alpha", next); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(alpha, "SKILL.md")); string(b) != next {
		t.Errorf("SKILL.md = %q", b)
	}
	if left, _ := filepath.Glob(filepath.Join(alpha, ".*tmp")); len(left) != 0 {
		t.Errorf("temp files left: %v", left)
	}

	for _, c := range []struct{ name, content, want string }{
		{"wrong name", skillMD("beta", "Demo."), `differs from folder "alpha"`},
		{"no frontmatter", "# alpha\n", "missing frontmatter"},
		{"no description", "---\nname: alpha\n---\n", "missing description"},
		{"bad yaml", "---\nname: [alpha\n---\n", "invalid frontmatter"},
		{"too large", skillMD("alpha", "Demo.") + strings.Repeat("x", 256<<10), "256 KiB"},
	} {
		err := SkillWrite(e, "alpha", c.content)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
		if b, _ := os.ReadFile(filepath.Join(alpha, "SKILL.md")); string(b) != next {
			t.Errorf("%s: SKILL.md changed to %q", c.name, b)
		}
	}

	gitBefore, _ := os.ReadFile(filepath.Join(gitSkill, "SKILL.md"))
	err := SkillWrite(e, "remote-skill", skillMD("remote-skill", "Changed."))
	if err == nil || !strings.Contains(err.Error(), `git source "remote"`) || !strings.Contains(err.Error(), "only skills of local sources") {
		t.Errorf("git skill: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(gitSkill, "SKILL.md")); !bytes.Equal(b, gitBefore) {
		t.Error("git skill changed")
	}
	err = SkillWrite(e, "writing-tools:outline", skillMD("outline", "Changed."))
	if err == nil || !strings.Contains(err.Error(), `marketplace source "m"`) {
		t.Errorf("marketplace skill: %v", err)
	}
	if err := SkillWrite(e, "nope", skillMD("nope", "Demo.")); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown skill: %v", err)
	}
}

func TestCheckWritable(t *testing.T) {
	root, outside, checkouts := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "alpha", "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(outside, "beta"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "beta"), filepath.Join(root, "beta")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "gamma"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "x.md"), filepath.Join(root, "gamma", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if err := checkWritable(filepath.Join(root, "alpha"), root, []string{checkouts}); err != nil {
		t.Errorf("alpha: %v", err)
	}
	for dir, want := range map[string]string{"beta": "outside the source folder", "gamma": "symlink"} {
		if err := checkWritable(filepath.Join(root, dir), root, []string{checkouts}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", dir, err, want)
		}
	}
	if err := checkWritable(filepath.Join(root, "alpha"), root, []string{filepath.Dir(root)}); err == nil || !strings.Contains(err.Error(), "checkouts") {
		t.Errorf("in checkouts: %v", err)
	}
}
