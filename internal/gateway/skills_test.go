package gateway

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/rules"
	"github.com/baeroe/skilltri/internal/skills"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// demoSkills writes the skills alpha (with references/a.md and a .git folder) and beta below a
// temp dir and returns their entries.
func demoSkills(t *testing.T) []catalog.Entry {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"alpha/SKILL.md":        "---\nname: alpha\ndescription: Demo skill alpha.\n---\nAlpha body.\n",
		"alpha/references/a.md": "Reference A.\n",
		"alpha/.git/HEAD":       "ref: refs/heads/main\n",
		"beta/SKILL.md":         "---\nname: beta\ndescription: Demo skill beta.\n---\nBeta body.\n",
	}
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return []catalog.Entry{
		{ID: "alpha", Source: "demo", Skill: skills.Skill{Name: "alpha", Description: "Demo skill alpha.", Dir: filepath.Join(root, "alpha")}},
		{ID: "beta", Source: "demo", Skill: skills.Skill{Name: "beta", Description: "Demo skill beta.", Dir: filepath.Join(root, "beta")}},
	}
}

// skillHarness is a session without servers whose skills are entries in every folder.
func skillHarness(t *testing.T, info SessionInfo, entries []catalog.Entry) *harness {
	t.Helper()
	return connectHarness(t, &config.Config{Version: 1, Servers: map[string]*config.Server{}}, info,
		func(string) []catalog.Entry { return entries })
}

func callSkillTool(t *testing.T, h *harness, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", tool, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func TestSkillToolsListed(t *testing.T) {
	h := skillHarness(t, SessionInfo{ID: "s1", Dir: t.TempDir()}, demoSkills(t))
	got := h.tools(t)
	for _, want := range []string{"skilltri_load_skill", "skilltri_read_skill_file"} {
		if !slices.Contains(got, want) {
			t.Errorf("tools = %v, missing %s", got, want)
		}
	}
	instr := h.cs.InitializeResult().Instructions
	if !strings.Contains(instr, "Agent skills (load with skilltri_load_skill): ") || !strings.Contains(instr, "- alpha: Demo skill alpha.") {
		t.Fatalf("instructions = %q", instr)
	}
}

func TestSkillInstructionsCapped(t *testing.T) {
	var entries []catalog.Entry
	for i := range 300 {
		id := "skill-" + strings.Repeat("x", 20) + "-" + string(rune('a'+i%26)) + strings.Repeat("y", i/26)
		entries = append(entries, catalog.Entry{ID: id, Skill: skills.Skill{Description: strings.Repeat("d", 40)}})
	}
	instr := skillInstructions(entries)
	if len(instr) > maxSkillList+100 {
		t.Fatalf("instructions are %d bytes", len(instr))
	}
	if !strings.Contains(instr, "more; call skilltri_load_skill with a name") {
		t.Fatalf("no overflow note in %d bytes of instructions", len(instr))
	}
}

func TestSkillToolsHiddenForNativeClient(t *testing.T) {
	entries := demoSkills(t)
	for _, tc := range []struct {
		client string
		want   bool
	}{{"claude-code", false}, {"other-harness", true}, {"", false}} {
		t.Run(tc.client, func(t *testing.T) {
			h := skillHarness(t, SessionInfo{ID: "s1", Dir: t.TempDir(), SkillsNative: []string{"claude-code"}}, entries)
			if slices.Contains(h.tools(t), "skilltri_load_skill") {
				t.Fatalf("skill tools listed before the client name is known")
			}
			if strings.Contains(h.cs.InitializeResult().Instructions, "Agent skills") {
				t.Fatalf("instructions list skills although SkillsNative is set")
			}
			h.sess.SetClient(tc.client)
			if err := h.sess.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := slices.Contains(h.tools(t), "skilltri_load_skill"); got != tc.want {
				t.Fatalf("client %q: skill tools listed = %v, want %v", tc.client, got, tc.want)
			}
		})
	}
}

func TestLoadSkill(t *testing.T) {
	h := skillHarness(t, SessionInfo{ID: "s1", Dir: t.TempDir()}, demoSkills(t))
	text, isErr := callSkillTool(t, h, "skilltri_load_skill", map[string]any{"name": "alpha"})
	if isErr {
		t.Fatalf("error result: %s", text)
	}
	if !strings.Contains(text, "Alpha body.") || !strings.Contains(text, "references/a.md") {
		t.Fatalf("load_skill = %q", text)
	}
	if strings.Contains(text, ".git") {
		t.Fatalf("load_skill lists dot folders: %q", text)
	}
	text, isErr = callSkillTool(t, h, "skilltri_read_skill_file", map[string]any{"name": "alpha", "path": "references/a.md"})
	if isErr || text != "Reference A.\n" {
		t.Fatalf("read_skill_file = %q (error %v)", text, isErr)
	}
}

// Review Focus 5: nothing outside the skill folder is served.
func TestReadSkillFileTraversal(t *testing.T) {
	entries := demoSkills(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	alpha := entries[0].Skill.Dir
	if err := os.Symlink(outside, filepath.Join(alpha, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(alpha, "linkdir")); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(alpha, "big.txt")
	if err := os.WriteFile(big, make([]byte, maxSkillFile+1), 0o644); err != nil {
		t.Fatal(err)
	}
	h := skillHarness(t, SessionInfo{ID: "s1", Dir: t.TempDir()}, entries)
	for _, p := range []string{"../x", "../beta/SKILL.md", "references/../../beta/SKILL.md", "/etc/hosts", outside, "link.txt", "linkdir/secret.txt", "references", "big.txt", "missing.md", ""} {
		text, isErr := callSkillTool(t, h, "skilltri_read_skill_file", map[string]any{"name": "alpha", "path": p})
		if !isErr {
			t.Errorf("path %q served: %q", p, text)
		}
		if strings.Contains(text, "secret") && !strings.Contains(text, "secret.txt") {
			t.Errorf("path %q leaked content: %q", p, text)
		}
	}
	if text, _ := callSkillTool(t, h, "skilltri_read_skill_file", map[string]any{"name": "alpha", "path": "big.txt"}); !strings.Contains(text, "256 KiB") {
		t.Errorf("big file error does not name the limit: %q", text)
	}
}

func TestSkillsFollowDir(t *testing.T) {
	entries := demoSkills(t)
	active := rules.Normalize(t.TempDir())
	other := t.TempDir()
	h := connectHarness(t, &config.Config{Version: 1, Servers: map[string]*config.Server{}}, SessionInfo{ID: "s1", Dir: active},
		func(dir string) []catalog.Entry {
			if dir == active {
				return entries
			}
			return entries[1:] // only beta
		})
	if text, isErr := callSkillTool(t, h, "skilltri_load_skill", map[string]any{"name": "alpha"}); isErr {
		t.Fatalf("alpha refused in its folder: %s", text)
	}
	h.sess.SetDir(context.Background(), other)
	text, isErr := callSkillTool(t, h, "skilltri_load_skill", map[string]any{"name": "alpha"})
	want := `skill "alpha" is not active in ` + rules.Normalize(other)
	if !isErr || text != want {
		t.Fatalf("load alpha in other folder = %q (error %v), want %q", text, isErr, want)
	}
	if _, isErr := callSkillTool(t, h, "skilltri_read_skill_file", map[string]any{"name": "alpha", "path": "references/a.md"}); !isErr {
		t.Fatalf("read_skill_file served an inactive skill")
	}
	if _, isErr := callSkillTool(t, h, "skilltri_load_skill", map[string]any{"name": "beta"}); isErr {
		t.Fatalf("beta refused")
	}
}
