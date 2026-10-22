package harness

import (
	"path/filepath"
	"testing"

	"github.com/baeroe/skilltri/internal/config"
)

func TestDuplicates(t *testing.T) {
	home := t.TempDir()
	writeFile(t, home, filepath.Join(".claude.json"), `{
	  "mcpServers": {
	    "docs": {"command": "/opt/other/docs"},
	    "skilltri": {"command": "/opt/demo/bin/skilltri", "args": ["mcp"]},
	    "unrelated": {"command": "weather-mcp"}
	  },
	  "projects": {"/work/demo": {"mcpServers": {"notes-local": {"command": "notes-mcp", "args": ["--stdio"]}}}}
	}`)
	writeFile(t, home, filepath.Join(".codex", "config.toml"), "[mcp_servers.tracker]\nurl = \"https://tracker.example.test/mcp/\"\n")
	writeFile(t, home, filepath.Join(".gemini", "settings.json"), `{"mcpServers":{"tracker2":{"httpUrl":"https://tracker.example.test/mcp"}}}`)
	writeFile(t, home, filepath.Join(".config", "opencode", "opencode.json"), `{"mcp":{"n":{"type":"local","command":["notes-mcp","--stdio"]},"old":{"type":"remote","url":"http://127.0.0.1:3050/mcp"}}}`)
	writeFile(t, home, filepath.Join(".cursor", "mcp.json"), `{"mcpServers":{"notes":{"command":"other"}, "x":{"command":"notes-mcp"}}}`)
	servers := map[string]*config.Server{
		"docs":    {Type: "stdio", Command: "docs-mcp"},
		"notes":   {Type: "stdio", Command: "notes-mcp", Args: []string{"--stdio"}},
		"tracker": {Type: "http", URL: "https://tracker.example.test/mcp"},
		"off":     {Type: "stdio", Command: "weather-mcp", Disabled: true},
	}
	type row struct{ harness, project, name, server, reason, file string }
	var got []row
	for _, d := range Duplicates(home, servers) {
		got = append(got, row{d.Harness, d.Entry.Project, d.Entry.Name, d.Server, d.Reason, d.Entry.File})
	}
	want := []row{
		{Claude, "", "docs", "docs", "same name", filepath.Join(home, ".claude.json")},
		{Claude, "/work/demo", "notes-local", "notes", "same command", filepath.Join(home, ".claude.json")},
		{Codex, "", "tracker", "tracker", "same name", filepath.Join(home, ".codex", "config.toml")},
		{Gemini, "", "tracker2", "tracker", "same URL", filepath.Join(home, ".gemini", "settings.json")},
		{OpenCode, "", "n", "notes", "same command", filepath.Join(home, ".config", "opencode", "opencode.json")},
		{Cursor, "", "notes", "notes", "same name", filepath.Join(home, ".cursor", "mcp.json")},
	}
	if len(got) != len(want) {
		t.Fatalf("duplicates:\n got %+v\nwant %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("duplicate %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if Duplicates(t.TempDir(), servers) != nil {
		t.Error("no harness configs: want none")
	}
}

func TestEntryURL(t *testing.T) {
	home := t.TempDir()
	writeFile(t, home, filepath.Join(".codex", "config.toml"), "[mcp_servers.a]\nurl = 'https://a.example.test/mcp'\n")
	if e := codexEntries(filepath.Join(home, ".codex", "config.toml")); len(e) != 1 || e[0].URL != "https://a.example.test/mcp" {
		t.Fatalf("codex url: %+v", e)
	}
}
