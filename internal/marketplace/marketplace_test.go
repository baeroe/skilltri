package marketplace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const demo = "testdata/demo-market"

func TestLoad(t *testing.T) {
	m, err := Load(demo)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "demo-market" || len(m.Plugins) != 3 {
		t.Fatalf("got %+v", m)
	}
	w := m.Plugins[0]
	if w.Name != "writing" || len(w.Skills) != 1 || w.Skills[0] != "./extra-skills" {
		t.Fatalf("entry %+v", w)
	}
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("want error for missing marketplace.json")
	}
}

func TestParseSource(t *testing.T) {
	tests := []struct {
		name, raw string
		local     string
		remote    *Remote
		err       string
	}{
		{"local", `"./plugins/x"`, "./plugins/x", nil, ""},
		{"parent", `"./../x"`, "", nil, "outside"},
		{"absolute", `"/etc/x"`, "", nil, "relative"},
		{"bare", `"x"`, "", nil, "relative"},
		{"github", `{"source":"github","repo":"demo/r","ref":"v1"}`, "", &Remote{URL: "demo/r", Ref: "v1"}, ""},
		{"github sha wins", `{"source":"github","repo":"demo/r","ref":"main","sha":"abc123"}`, "", &Remote{URL: "demo/r", Ref: "abc123"}, ""},
		{"url", `{"source":"url","url":"https://git.example.com/r.git"}`, "", &Remote{URL: "https://git.example.com/r.git"}, ""},
		{"subdir url", `{"source":"git-subdir","url":"https://git.example.com/r.git","path":"plugins/a","ref":"main"}`, "", &Remote{URL: "https://git.example.com/r.git", Ref: "main", Path: "plugins/a"}, ""},
		{"subdir repo shorthand", `{"source":"git-subdir","repo":"demo/r","path":"a"}`, "", &Remote{URL: "demo/r", Path: "a"}, ""},
		{"subdir traversal", `{"source":"git-subdir","repo":"demo/r","path":"../a"}`, "", nil, "outside"},
		{"github no repo", `{"source":"github"}`, "", nil, "repo"},
		{"npm", `{"source":"npm","package":"x"}`, "", nil, "unsupported plugin source npm"},
		{"archive", `{"source":"archive","url":"x"}`, "", nil, "unsupported plugin source archive"},
		{"command", `{"source":"command","command":"x"}`, "", nil, "unsupported plugin source command"},
		{"unknown", `{"source":"ftp"}`, "", nil, "unsupported plugin source ftp"},
		{"empty", ``, "", nil, "missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local, remote, err := ParseSource(json.RawMessage(tt.raw))
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("err = %v, want %q", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if local != tt.local {
				t.Fatalf("local = %q, want %q", local, tt.local)
			}
			if (remote == nil) != (tt.remote == nil) || (remote != nil && *remote != *tt.remote) {
				t.Fatalf("remote = %+v, want %+v", remote, tt.remote)
			}
		})
	}
}

func TestLoadPlugin(t *testing.T) {
	m, err := Load(demo)
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := filepath.Abs(filepath.Join(demo, "plugins", "writing"))
	p, warns, err := LoadPlugin(m.Plugins[0], dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.Entry != "writing" || p.Name != "writing-tools" || p.Dir != dir {
		t.Fatalf("plugin %+v", p)
	}
	want := []string{filepath.Join(dir, "skills"), filepath.Join(dir, "extra-skills")}
	if strings.Join(p.SkillRoots, "|") != strings.Join(want, "|") {
		t.Fatalf("roots = %v, want %v", p.SkillRoots, want)
	}
	n, ok := p.Servers["notes"]
	if !ok || n.Type != "stdio" || n.Command != filepath.Join(dir, "bin", "notes") ||
		n.Args[1] != dir+"/data" || n.Env["MODE"] != "demo" {
		t.Fatalf("notes = %+v", n)
	}
	d := p.Servers["docs"]
	if d.Type != "http" || d.URL != "https://mcp.example.com/mcp" || d.Headers["X-Demo"] != "1" {
		t.Fatalf("docs = %+v", d)
	}
	if _, ok := p.Servers["legacy"]; ok {
		t.Fatal("sse server must be skipped")
	}
	joined := strings.Join(warns, "\n")
	if !strings.Contains(joined, "legacy") || !strings.Contains(joined, "sse") {
		t.Fatalf("warnings = %v", warns)
	}
	if !strings.Contains(joined, "..") {
		t.Fatalf("want warning for ../escape, got %v", warns)
	}
}

func TestLoadPluginVariants(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, s string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// no plugin.json, flat .mcp.json, entry-level extra skills and mcpServers by file path
	write(".mcp.json", `{"flat":{"command":"run","type":"stdio"}}`)
	write("srv.json", `{"mcpServers":{"viafile":{"url":"https://x.example/mcp"}}}`)
	write(".claude-plugin/plugin.json", `{"mcpServers":"./srv.json"}`)
	entry := Entry{Name: "bare", Skills: []string{"./more", "/abs"}}
	p, warns, err := LoadPlugin(entry, dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "bare" {
		t.Fatalf("name = %q", p.Name)
	}
	if p.Servers["flat"].Type != "stdio" || p.Servers["flat"].Command != "run" {
		t.Fatalf("flat = %+v", p.Servers["flat"])
	}
	if p.Servers["viafile"].Type != "http" {
		t.Fatalf("viafile = %+v", p.Servers["viafile"])
	}
	if len(p.SkillRoots) != 2 || p.SkillRoots[1] != filepath.Join(dir, "more") {
		t.Fatalf("roots = %v", p.SkillRoots)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "/abs") {
		t.Fatalf("warnings = %v", warns)
	}
	// entry-level inline mcpServers
	entry = Entry{Name: "inline", MCPServers: json.RawMessage(`{"inl":{"command":"x"}}`)}
	p, _, err = LoadPlugin(entry, t.TempDir())
	if err != nil || p.Servers["inl"].Type != "stdio" {
		t.Fatalf("inline: %+v %v", p.Servers, err)
	}
	// broken plugin.json is an error
	write(".claude-plugin/plugin.json", `{`)
	if _, _, err := LoadPlugin(Entry{Name: "b"}, dir); err == nil {
		t.Fatal("want error for broken plugin.json")
	}
}

func TestPluginRootPrefix(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	mj := `{"name":"m","owner":{"name":"o"},"metadata":{"pluginRoot":"./plugins"},
	"plugins":[{"name":"writing","source":"writing"},{"name":"abs","source":"./other/abs"},{"name":"gh","source":{"source":"github","repo":"a/b"}}]}`
	if err := os.WriteFile(filepath.Join(root, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if m.PluginRoot != "./plugins" {
		t.Fatalf("PluginRoot = %q", m.PluginRoot)
	}
	local, _, err := ParseSource(m.Plugins[0].Source)
	if err != nil || local != "./plugins/writing" {
		t.Fatalf("local = %q, %v", local, err)
	}
	if local, _, _ := ParseSource(m.Plugins[1].Source); local != "./other/abs" {
		t.Fatalf("explicit ./ path must stay: %q", local)
	}
	if _, r, _ := ParseSource(m.Plugins[2].Source); r == nil || r.URL != "a/b" {
		t.Fatalf("remote untouched: %+v", r)
	}
}

func TestMergeServersSelfReference(t *testing.T) {
	dir := t.TempDir()
	must := func(rel, s string) {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("a.json", `"./a.json"`)
	must(".claude-plugin/plugin.json", `{"mcpServers":"./a.json"}`)
	p, warns, err := LoadPlugin(Entry{Name: "loop"}, dir)
	if err != nil || len(p.Servers) != 0 || len(warns) != 1 || !strings.Contains(warns[0], "a.json") && !strings.Contains(warns[0], "mcpServers") {
		t.Fatalf("servers=%v warns=%v err=%v", p.Servers, warns, err)
	}
}

func TestDuplicateServerWarning(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{"x":{"command":"a"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	entry := Entry{Name: "d", MCPServers: json.RawMessage(`{"x":{"command":"b"}}`)}
	p, warns, err := LoadPlugin(entry, dir)
	if err != nil || p.Servers["x"].Command != "b" || len(warns) != 1 || !strings.Contains(warns[0], "more than once") {
		t.Fatalf("%+v %v %v", p.Servers, warns, err)
	}
}

func TestLoadRejectsBadEntries(t *testing.T) {
	for name, body := range map[string]string{
		"duplicate": `{"name":"m","plugins":[{"name":"a","source":"./a"},{"name":"a","source":"./b"}]}`,
		"absolute":  `{"name":"m","metadata":{"pluginRoot":"./p"},"plugins":[{"name":"a","source":"/etc"}]}`,
	} {
		root := t.TempDir()
		_ = os.MkdirAll(filepath.Join(root, ".claude-plugin"), 0o755)
		_ = os.WriteFile(filepath.Join(root, ".claude-plugin", "marketplace.json"), []byte(body), 0o644)
		if _, err := Load(root); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestPluginExtrasAndSkills(t *testing.T) {
	mk := func(files map[string]string) string {
		t.Helper()
		dir := t.TempDir()
		for rel, s := range files {
			p := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	skill := "---\nname: s\ndescription: Demo.\n---\n"
	for _, tc := range []struct {
		name   string
		files  map[string]string
		extras []string
		skills bool
	}{
		{"skills only", map[string]string{"skills/s/SKILL.md": skill}, nil, true},
		{"lsp only", map[string]string{".lsp.json": `{"go":{"command":"gopls"}}`}, []string{"LSP servers"}, false},
		{"folders", map[string]string{"hooks/hooks.json": "{}", "agents/a.md": "a", "commands/c.md": "c", "skills/s/SKILL.md": skill},
			[]string{"agents", "commands", "hooks"}, true},
		{"manifest fields", map[string]string{".claude-plugin/plugin.json": `{"name":"m","hooks":"./h.json","lspServers":{"x":{}},"commands":["./c"]}`},
			[]string{"LSP servers", "commands", "hooks"}, false},
		{"empty skills folder", map[string]string{"skills/readme.txt": "x"}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _, err := LoadPlugin(Entry{Name: "demo"}, mk(tc.files))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(p.Extras, ",") != strings.Join(tc.extras, ",") {
				t.Errorf("extras = %q, want %q", p.Extras, tc.extras)
			}
			if p.HasSkills() != tc.skills {
				t.Errorf("HasSkills = %v", p.HasSkills())
			}
		})
	}
}
