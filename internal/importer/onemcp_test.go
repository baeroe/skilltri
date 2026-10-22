package importer

import (
	"os"
	"strings"
	"testing"
)

func TestFrom1MCP(t *testing.T) {
	data, err := os.ReadFile("testdata/1mcp.json")
	if err != nil {
		t.Fatal(err)
	}
	res, err := From1MCP(data)
	if err != nil {
		t.Fatal(err)
	}
	s := res.Servers

	docs := s["docs"]
	if docs == nil || docs.Type != "http" || docs.RequestTimeoutMs != 120000 {
		t.Fatalf("docs = %+v", docs)
	}
	if got := docs.Headers["Authorization"]; got != "${secret:docs-authorization}" {
		t.Errorf("header = %q", got)
	}
	if res.Secrets["docs-authorization"] != "Bearer xyz" {
		t.Errorf("secrets = %v", res.Secrets)
	}

	if b := s["browser"]; b == nil || b.Type != "stdio" || b.Command != "demo-browser-mcp" || len(b.Args) != 1 {
		t.Errorf("browser = %+v", b)
	}
	if w := s["wiki"]; w == nil || w.Type != "stdio" || w.Command != "mcp-remote" {
		t.Errorf("wiki = %+v", w)
	}
	if o := s["off"]; o == nil || !o.Disabled {
		t.Errorf("off = %+v", o)
	}

	n := s["notes"]
	if n == nil {
		t.Fatal("notes missing")
	}
	if n.Env["NOTES_API_TOKEN"] != "${secret:notes-notes_api_token}" || n.Env["NOTES_MODE"] != "fast" {
		t.Errorf("notes env = %v", n.Env)
	}
	if res.Secrets["notes-notes_api_token"] != "s3cret" {
		t.Errorf("secrets = %v", res.Secrets)
	}

	if _, ok := s["legacy"]; ok {
		t.Error("legacy should not be imported")
	}
	if s["my-tool-v2"] == nil || s["my_tool.v2"] != nil {
		t.Errorf("renamed server missing: %v", s)
	}

	warn := strings.Join(res.Warnings, "\n")
	for _, want := range []string{"sse", "set folder rules instead", "web", `"my_tool.v2"`, `"my-tool-v2"`} {
		if !strings.Contains(warn, want) {
			t.Errorf("warnings lack %q:\n%s", want, warn)
		}
	}
	for _, v := range []string{"s3cret", "Bearer xyz"} {
		if strings.Contains(warn, v) {
			t.Errorf("warnings leak secret %q", v)
		}
	}
}

func TestFrom1MCPErrors(t *testing.T) {
	if _, err := From1MCP([]byte("{")); err == nil {
		t.Error("want parse error")
	}
	res, err := From1MCP([]byte(`{"mcpServers":{"_":{"command":"x"},"a":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Servers) != 0 || len(res.Warnings) != 2 {
		t.Errorf("res = %+v", res)
	}
}

func TestFrom1MCPSecretNameCollisions(t *testing.T) {
	res, err := From1MCP([]byte(`{"mcpServers":{
 "a":{"url":"https://x.example.com","headers":{"X-Key":"v1","x-key":"v2"}},
 "b":{"url":"https://y.example.com","headers":{"c-k":"v3"}},
 "b-c":{"url":"https://z.example.com","headers":{"k":"v4"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Secrets) != 4 {
		t.Fatalf("secrets = %v", res.Secrets)
	}
	seen := map[string]bool{}
	for _, v := range res.Secrets {
		seen[v] = true
	}
	for _, v := range []string{"v1", "v2", "v3", "v4"} {
		if !seen[v] {
			t.Errorf("value %s lost", v)
		}
	}
	for _, names := range res.SecretsByServer {
		for _, n := range names {
			if _, ok := res.Secrets[n]; !ok {
				t.Errorf("unknown name %s", n)
			}
		}
	}
	if strings.Contains(strings.Join(res.Warnings, "\n"), "v1") {
		t.Error("warning leaks value")
	}
	if len(res.Warnings) < 2 {
		t.Errorf("warnings = %v", res.Warnings)
	}
}
