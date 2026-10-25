package agenttools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/skills"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const sourceToken = "demo-source-token"

// mustFail calls a write tool that must fail, checks that the config file did not change and
// returns the error text.
func mustFail(t *testing.T, b *demoBackend, name string, args any) string {
	t.Helper()
	before := b.raw(t)
	text, isErr := call(t, b, demoCall, name, args)
	if !isErr {
		t.Fatalf("%s %v succeeded: %s", name, args, text)
	}
	if !bytes.Equal(before, b.raw(t)) {
		t.Fatalf("%s %v changed the config", name, args)
	}
	return text
}

func TestAddRemoveSource(t *testing.T) {
	b := writeBackend(t)
	url := "https://user:" + sourceToken + "@git.example.test/demo/skills.git"
	text := mustWrite(t, b, "skilltri_add_source", map[string]any{"name": "remote", "type": "git", "url": url, "ref": "main", "subpath": "skills", "updateEvery": "2h"})
	if !strings.Contains(text, "skilltri_update_sources") || strings.Contains(text, sourceToken) {
		t.Errorf("confirmation = %q", text)
	}
	if s := b.load(t).Sources["remote"]; s == nil || s.Type != "git" || s.URL != url || s.Ref != "main" || s.Subpath != "skills" || s.UpdateEvery != "2h" {
		t.Fatalf("remote = %+v", s)
	}
	if e := lastAudit(t, b, "skilltri_add_source", "source/remote", true); strings.Contains(e.Summary, sourceToken) || !strings.Contains(e.Summary, "git") {
		t.Errorf("summary = %q", e.Summary)
	}
	if data, _ := os.ReadFile(b.auditPath); bytes.Contains(data, []byte(sourceToken)) {
		t.Fatalf("audit log contains the token: %s", data)
	}

	// a relative local path resolves against the session's folder
	mustWrite(t, b, "skilltri_add_source", map[string]any{"name": "mine", "type": "local", "path": "skills"})
	if s := b.load(t).Sources["mine"]; s == nil || s.Path != "/work/demo/skills" {
		t.Fatalf("mine = %+v", s)
	}
	mustWrite(t, b, "skilltri_add_source", map[string]any{"name": "market2", "type": "marketplace", "url": "demo/market", "plugins": "disabled"})
	if s := b.load(t).Sources["market2"]; s == nil || s.PluginsDefault != config.PluginsDisabled {
		t.Fatalf("market2 = %+v", s)
	}

	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"name": "remote", "type": "git", "url": "demo/other"}, "already exists"},
		{map[string]any{"name": "x", "type": "local"}, "path is required"},
		{map[string]any{"name": "x", "type": "local", "path": "/work/x", "ref": "main"}, "local source takes only path"},
		{map[string]any{"name": "x", "type": "git"}, "url is required"},
		{map[string]any{"name": "x", "type": "git", "url": "demo/x", "plugins": "disabled"}, "plugins"},
		{map[string]any{"name": "x", "type": "marketplace", "url": "demo/x", "subpath": "a"}, "subpath"},
		{map[string]any{"name": "x", "type": "marketplace", "url": "demo/x", "plugins": "some"}, "plugins"},
		{map[string]any{"name": "x", "type": "svn", "url": "demo/x"}, "type"},
		{map[string]any{"name": "bad name", "type": "git", "url": "demo/x"}, "invalid source name"},
		{map[string]any{"type": "git", "url": "demo/x"}, "name is required"},
		{map[string]any{"name": "x", "type": "git", "url": "https://u:" + sourceToken + "@h.test/r", "updateEvery": "soon"}, "updateEvery"},
	} {
		if text := mustFail(t, b, "skilltri_add_source", c.args); !strings.Contains(text, c.want) || strings.Contains(text, sourceToken) {
			t.Errorf("%v: %q, want %q", c.args, text, c.want)
		}
	}

	mustWrite(t, b, "skilltri_remove_source", map[string]any{"name": "remote"})
	if b.load(t).Sources["remote"] != nil {
		t.Fatal("remote not removed")
	}
	lastAudit(t, b, "skilltri_remove_source", "source/remote", true)
	if text := mustFail(t, b, "skilltri_remove_source", map[string]any{"name": "remote"}); !strings.Contains(text, "no such source") {
		t.Errorf("remove missing: %q", text)
	}
	lastAudit(t, b, "skilltri_remove_source", "source/remote", false)
}

func TestUpdateSources(t *testing.T) {
	b := writeBackend(t)
	b.updateStates = []catalog.SourceState{
		{Name: "market", Commit: "0123456789abcdef0123456789abcdef01234567"},
		{Name: "local", Error: `fetch "https://user:` + sourceToken + `@git.example.test/r?token=` + sourceToken + `": refused`},
	}
	text := mustWrite(t, b, "skilltri_update_sources", map[string]any{"sources": []string{"market", "local"}})
	if !slices.Equal(b.updates[len(b.updates)-1], []string{"market", "local"}) {
		t.Fatalf("updates = %v", b.updates)
	}
	if !strings.Contains(text, "market: commit 0123456") || !strings.Contains(text, "local: error") || strings.Contains(text, sourceToken) {
		t.Errorf("result = %q", text)
	}
	if e := lastAudit(t, b, "skilltri_update_sources", "sources", true); e.Summary != "update local, market" {
		t.Errorf("summary = %q", e.Summary)
	}
	mustWrite(t, b, "skilltri_update_sources", map[string]any{})
	if got := b.updates[len(b.updates)-1]; len(got) != 0 {
		t.Errorf("update all = %v", got)
	}
	if e := lastAudit(t, b, "skilltri_update_sources", "sources", true); e.Summary != "update all enabled sources" {
		t.Errorf("summary = %q", e.Summary)
	}

	n := len(b.updates)
	if text := mustFail(t, b, "skilltri_update_sources", map[string]any{"sources": []string{"nosuch"}}); !strings.Contains(text, `no such source "nosuch"`) {
		t.Errorf("unknown source: %q", text)
	}
	if len(b.updates) != n {
		t.Error("unknown source was updated")
	}
	b.updateErr = errors.New("no valid config")
	if text := mustFail(t, b, "skilltri_update_sources", map[string]any{"sources": []string{"market"}}); !strings.Contains(text, "no valid config") {
		t.Errorf("update error: %q", text)
	}
	lastAudit(t, b, "skilltri_update_sources", "source/market", false)
}

func TestNewSkill(t *testing.T) {
	b := writeBackend(t)
	root := filepath.Join(filepath.Dir(b.env.ConfigPath), "skills")
	text := mustWrite(t, b, "skilltri_new_skill", map[string]any{"name": "demo-notes", "description": "Take demo notes: when the user asks for notes.", "body": "# Demo notes\n\nWrite them down.\n"})
	if !strings.Contains(text, filepath.Join(root, "demo-notes", "SKILL.md")) {
		t.Errorf("confirmation = %q", text)
	}
	s, err := skills.Parse(filepath.Join(root, "demo-notes"))
	if err != nil || len(s.Warnings) != 0 || s.Name != "demo-notes" || s.Description != "Take demo notes: when the user asks for notes." {
		t.Fatalf("skill = %+v, %v", s, err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "demo-notes", "SKILL.md")); !strings.HasSuffix(string(data), "\n---\n\n# Demo notes\n\nWrite them down.\n") {
		t.Errorf("SKILL.md = %q", data)
	}
	if l := b.load(t).Sources["local"]; l == nil || l.Type != "local" || l.Path != root {
		t.Fatalf("local source = %+v", l)
	}
	lastAudit(t, b, "skilltri_new_skill", "skill/demo-notes", true)
	// the daemon's config has the source "local": its catalog is rebuilt at once
	if got := b.updates[len(b.updates)-1]; !slices.Equal(got, []string{"local"}) {
		t.Errorf("updates = %v", b.updates)
	}

	mustWrite(t, b, "skilltri_new_skill", map[string]any{"name": "demo-two", "description": "Second demo.", "body": "Body."})
	if n := len(b.load(t).Sources); n != 2 { // "m" and "local", added once
		t.Errorf("%d sources", n)
	}
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"name": "demo-notes", "description": "Again.", "body": "x"}, "already exists"},
		{map[string]any{"name": "Bad_Name", "description": "x", "body": "x"}, "invalid skill name"},
		{map[string]any{"name": "demo-three", "description": " ", "body": "x"}, "description is required"},
		{map[string]any{"name": "demo-three", "description": strings.Repeat("d", 1025), "body": "x"}, "1024"},
		{map[string]any{"name": "demo-three", "description": "x", "body": strings.Repeat("b", 256<<10)}, "256 KiB"},
	} {
		if text := mustFail(t, b, "skilltri_new_skill", c.args); !strings.Contains(text, c.want) {
			t.Errorf("%v: %q, want %q", c.args["name"], text, c.want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "demo-three")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a refused skill left a folder")
	}
	if data, _ := os.ReadFile(filepath.Join(root, "demo-notes", "SKILL.md")); !strings.Contains(string(data), "Write them down.") {
		t.Error("a refused new_skill overwrote the skill")
	}
	lastAudit(t, b, "skilltri_new_skill", "skill/demo-three", false)
}

func TestNewSkillLocalElsewhere(t *testing.T) {
	b := writeBackend(t)
	if err := config.Update(b.env.ConfigPath, func(c *config.Config) error {
		c.Sources["local"] = &config.Source{Type: "local", Path: t.TempDir()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	text := mustWrite(t, b, "skilltri_new_skill", map[string]any{"name": "demo-notes", "description": "Demo.", "body": "Body."})
	if !strings.Contains(text, `warning: the source "local" points at`) {
		t.Errorf("confirmation = %q", text)
	}
}

func TestWriteSkill(t *testing.T) {
	b := writeBackend(t)
	mine := t.TempDir()
	alpha := filepath.Join(mine, "alpha")
	if err := os.MkdirAll(alpha, 0o755); err != nil {
		t.Fatal(err)
	}
	old := "---\nname: alpha\ndescription: Demo skill alpha.\n---\n\n# alpha\n"
	if err := os.WriteFile(filepath.Join(alpha, "SKILL.md"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.Update(b.env.ConfigPath, func(c *config.Config) error {
		c.Sources["mine"] = &config.Source{Type: "local", Path: mine}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b.cat = &catalog.Catalog{Skills: []catalog.Entry{{ID: "alpha", Source: "mine", Skill: skills.Skill{Name: "alpha", Dir: alpha}}}}
	b.cfg.Sources["mine"] = &config.Source{Type: "local", Path: mine}

	next := "---\nname: alpha\ndescription: Demo skill alpha, rewritten.\n---\n\n# alpha\n\nNew.\n"
	if text := mustWrite(t, b, "skilltri_write_skill", map[string]any{"id": "alpha", "content": next}); !strings.Contains(text, "alpha") {
		t.Errorf("confirmation = %q", text)
	}
	if data, _ := os.ReadFile(filepath.Join(alpha, "SKILL.md")); string(data) != next {
		t.Fatalf("SKILL.md = %q", data)
	}
	lastAudit(t, b, "skilltri_write_skill", "skill/alpha", true)
	if got := b.updates[len(b.updates)-1]; !slices.Equal(got, []string{"mine"}) {
		t.Errorf("updates = %v", b.updates)
	}

	for _, c := range []struct{ id, content, want string }{
		{"alpha", "---\nname: beta\ndescription: x\n---\n", `differs from folder "alpha"`},
		{"alpha", "no frontmatter", "missing frontmatter"},
		{"writing-tools:outline", "---\nname: outline\ndescription: x\n---\n", `marketplace source "m"`},
		{"nosuch", "---\nname: nosuch\ndescription: x\n---\n", "no such skill"},
		{"", "x", "id is required"},
	} {
		text, isErr := call(t, b, demoCall, "skilltri_write_skill", map[string]any{"id": c.id, "content": c.content})
		if !isErr || !strings.Contains(text, c.want) {
			t.Errorf("%s: %q, want %q", c.id, text, c.want)
		}
		if data, _ := os.ReadFile(filepath.Join(alpha, "SKILL.md")); string(data) != next {
			t.Errorf("%s: SKILL.md changed", c.id)
		}
	}
	lastAudit(t, b, "skilltri_write_skill", "", false)
}

// callAccess calls a tool offered with access.
func callAccess(t *testing.T, b Backend, access, name string, args any) (string, bool) {
	t.Helper()
	for _, tl := range Tools(access) {
		if tl.Def.Name == name {
			raw, _ := json.Marshal(args)
			res, err := tl.Handle(context.Background(), b, demoCall, raw)
			if err != nil {
				t.Fatal(err)
			}
			return res.Content[0].(*mcp.TextContent).Text, res.IsError
		}
	}
	t.Fatalf("%s is not offered with %s", name, access)
	return "", false
}

func TestReadSkill(t *testing.T) {
	b := newDemoBackend()
	dir := filepath.Join(t.TempDir(), "gamma")
	if err := os.MkdirAll(filepath.Join(dir, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: gamma\ndescription: Demo skill gamma.\n---\n\n# gamma\n"
	for p, c := range map[string]string{"SKILL.md": md, "refs/notes.md": "notes", "run.sh": "echo demo"} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// gamma's plugin is disabled: read_skill still serves it
	b.cat.Skills[2].Skill.Dir = dir
	text, isErr := callAccess(t, b, config.AgentAccessRead, "skilltri_read_skill", map[string]any{"id": "gamma"})
	if isErr || !strings.HasPrefix(text, md) || !strings.Contains(text, "- refs/notes.md\n") || !strings.Contains(text, "- run.sh\n") {
		t.Fatalf("read_skill = %q", text)
	}
	if text, isErr := callAccess(t, b, config.AgentAccessRead, "skilltri_read_skill", map[string]any{"id": "nosuch"}); !isErr || !strings.Contains(text, "no such skill") {
		t.Errorf("unknown skill: %q", text)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(strings.Repeat("x", 256<<10+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if text, isErr := callAccess(t, b, config.AgentAccessRead, "skilltri_read_skill", map[string]any{"id": "gamma"}); !isErr || !strings.Contains(text, "256 KiB") {
		t.Errorf("large SKILL.md: %q", text)
	}
}

func TestUpdateSourcesMasksSourceCredentials(t *testing.T) {
	b := writeBackend(t)
	b.cfg.Sources["git"] = &config.Source{Type: "git", URL: "https://x-access-token:" + sourceToken + "@git.example.test/demo/skills.git"}
	b.updateStates = []catalog.SourceState{{Name: "git", Error: "remote: invalid credentials " + sourceToken}}
	if text := mustWrite(t, b, "skilltri_update_sources", map[string]any{"sources": []string{"git"}}); strings.Contains(text, sourceToken) || !strings.Contains(text, "invalid credentials •••") {
		t.Errorf("result = %q", text)
	}
	b.updateErr = errors.New("sync: token " + sourceToken + " refused")
	if text := mustFail(t, b, "skilltri_update_sources", map[string]any{"sources": []string{"git"}}); strings.Contains(text, sourceToken) {
		t.Errorf("error = %q", text)
	}
}
