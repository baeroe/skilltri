package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSnippets(t *testing.T) {
	s := map[string]Snippet{}
	for _, x := range Snippets("/opt/demo/bin/skilltri") {
		s[x.Name] = x
	}
	if len(s) != 7 {
		t.Fatalf("snippets: %d", len(s))
	}
	if s["Claude Code"].Text != "claude mcp add --scope user skilltri -- /opt/demo/bin/skilltri mcp" {
		t.Fatal(s["Claude Code"].Text)
	}
	if s["Codex CLI"].Text != "codex mcp add skilltri -- /opt/demo/bin/skilltri mcp" {
		t.Fatal(s["Codex CLI"].Text)
	}
	if s["Codex CLI (config)"].Text != "[mcp_servers.skilltri]\ncommand = \"/opt/demo/bin/skilltri\"\nargs = [\"mcp\"]" {
		t.Fatal(s["Codex CLI (config)"].Text)
	}
	var cursor struct {
		MCPServers map[string]struct {
			Type, Command string
			Args          []string
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(s["Cursor"].Text), &cursor); err != nil || cursor.MCPServers["skilltri"].Args[0] != "mcp" || cursor.MCPServers["skilltri"].Type != "stdio" {
		t.Fatalf("cursor: %v %+v", err, cursor)
	}
	var oc struct {
		MCP map[string]struct{ Command []string } `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(s["opencode"].Text), &oc); err != nil || !reflect.DeepEqual(oc.MCP["skilltri"].Command, []string{"/opt/demo/bin/skilltri", "mcp"}) {
		t.Fatalf("opencode: %v %+v", err, oc)
	}
	var gm struct {
		MCPServers map[string]struct {
			Command string
			Args    []string
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(s["Gemini CLI (config)"].Text), &gm); err != nil || gm.MCPServers["skilltri"].Command != "/opt/demo/bin/skilltri" {
		t.Fatalf("gemini: %v %+v", err, gm)
	}
	if s["Gemini CLI"].Note == "" || s["Cursor"].Note == "" {
		t.Fatal("unverified notes missing")
	}
	if Snippets("/path with space/skilltri")[0].Text != "claude mcp add --scope user skilltri -- '/path with space/skilltri' mcp" {
		t.Fatal("quoting")
	}
}

func writeFile(t *testing.T, home, rel, content string) {
	t.Helper()
	p := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRegistered(t *testing.T) {
	home := t.TempDir()
	if r := Registered(home); len(r) != 5 || r[Claude] || r[Codex] {
		t.Fatalf("empty home: %v", r)
	}
	writeFile(t, home, ".claude.json", `{"mcpServers": {"skilltri": {"command": "/x/skilltri", "args": ["mcp"]}}, "projects": {}}`)
	writeFile(t, home, ".codex/config.toml", "model = \"o3\"\n\n[mcp_servers.skilltri]\ncommand = \"/x/skilltri\"\n")
	writeFile(t, home, ".gemini/settings.json", `{broken`)
	writeFile(t, home, ".config/opencode/opencode.json", "{\n  // comment\n  \"mcp\": {\"skilltri\": {\"type\": \"local\"}}\n}")
	writeFile(t, home, ".cursor/mcp.json", `{"mcpServers": {"other": {}}}`)
	r := Registered(home)
	if !r[Claude] || !r[Codex] || r[Gemini] || !r[OpenCode] || r[Cursor] {
		t.Fatalf("detection: %v", r)
	}
	writeFile(t, home, ".codex/config.toml", "[mcp_servers.skilltrix]\n")
	if Registered(home)[Codex] {
		t.Fatal("prefix matched")
	}
}

// Registrations under another name are found by their command; Claude Code's project entries count.
func TestRegisteredByCommand(t *testing.T) {
	home := t.TempDir()
	writeFile(t, home, ".claude.json", `{"mcpServers": {"other": {"command": "demo"}},
		"projects": {"/work/demo": {"mcpServers": {"gw": {"command": "/opt/demo/skilltri", "args": ["mcp"]}}}}}`)
	writeFile(t, home, ".codex/config.toml", "[mcp_servers.gateway] # mine\ncommand = '/opt/demo/skilltri'\nargs = [ \"mcp\", \"--quiet\" ]\n\n[mcp_servers.x]\ncommand = \"skilltri\"\nargs = [\"serve\"]\n")
	writeFile(t, home, ".gemini/settings.json", `{"mcpServers": {"gw": {"command": "skilltri", "args": ["mcp"]}}}`)
	writeFile(t, home, ".config/opencode/opencode.jsonc", "{/* c */ \"mcp\": {\"gw\": {\"type\": \"local\", \"command\": [\"/bin/skilltri\", \"mcp\"]}}}")
	writeFile(t, home, ".cursor/mcp.json", `{"mcpServers": {"gw": {"command": "/bin/skilltri", "args": ["run"]}}}`)
	r := Registered(home)
	if !r[Claude] || !r[Codex] || !r[Gemini] || !r[OpenCode] || r[Cursor] {
		t.Fatalf("detection: %v", r)
	}
	writeFile(t, home, ".codex/config.toml", "[mcp_servers.x]\ncommand = \"skilltri\"\nargs = [\"serve\"]\n[other]\nargs = [\"mcp\"]\n")
	if Registered(home)[Codex] {
		t.Fatal("codex: args of another table counted")
	}
}

func TestOneMCPRegistered(t *testing.T) {
	home := t.TempDir()
	if got := OneMCPRegistered(home); len(got) != 0 {
		t.Fatalf("empty home: %v", got)
	}
	writeFile(t, home, ".claude.json", `{"mcpServers": {"skilltri": {"command": "skilltri", "args": ["mcp"]}},
		"projects": {"/work/demo": {"mcpServers": {"one": {"type": "http", "url": "http://127.0.0.1:3050/mcp"}}}}}`)
	writeFile(t, home, ".codex/config.toml", "[mcp_servers.one]\ncommand = \"npx\"\nargs = [\"-y\", \"@1mcp/agent\"]\n")
	writeFile(t, home, ".gemini/settings.json", `{"mcpServers": {"one": {"httpUrl": "http://127.0.0.1:3050/mcp"}}}`)
	writeFile(t, home, ".config/opencode/opencode.json", `{"mcp": {"docs": {"type": "remote", "url": "https://docs.example.com/mcp"}}}`)
	writeFile(t, home, ".cursor/mcp.json", "{\n // 1mcp in a comment does not count\n \"mcpServers\": {\"x\": {\"command\": \"demo\"}}}")
	if got, want := OneMCPRegistered(home), []string{Claude, Codex, Gemini}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	writeFile(t, home, ".cursor/mcp.json", `{"mcpServers": {"one": {"command": "1mcp", "args": ["serve"]}}}`)
	if got := OneMCPRegistered(home); len(got) != 4 || got[3] != Cursor {
		t.Fatalf("cursor command: %v", got)
	}
}

func TestStripJSONC(t *testing.T) {
	in := "{\"a\": \"// not a comment\", /* block */ \"b\": \"x\\\"/*y\" // tail\n}"
	var v map[string]string
	if err := json.Unmarshal(stripJSONC([]byte(in)), &v); err != nil || v["a"] != "// not a comment" || v["b"] != `x"/*y` {
		t.Fatalf("%v %v", err, v)
	}
}
