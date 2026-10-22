package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/importer"
	"github.com/baeroe/skilltri/internal/secrets"
)

const src = `{"mcpServers":{
 "a":{"url":"https://a.example.com/mcp","headers":{"Authorization":"tok-a"}},
 "b":{"command":"demo","env":{"B_TOKEN":"tok-b"}}}}`

func TestImportInto(t *testing.T) {
	keyring.MockInit()
	cfg := filepath.Join(t.TempDir(), "config.json")
	// Existing server "a" with its own secret value.
	if err := config.Update(cfg, func(c *config.Config) error {
		c.Servers = map[string]*config.Server{"a": {Type: "stdio", Command: "old"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Set("a-authorization", "keep-me"); err != nil {
		t.Fatal(err)
	}
	res, err := importer.From1MCP([]byte(src))
	if err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if code := importInto(cfg, res, false, &out, &errOut); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	c, _ := config.Load(cfg)
	if c.Servers["a"].Command != "old" || c.Servers["b"] == nil {
		t.Errorf("servers = %v", c.Servers)
	}
	if v, _ := secrets.Get("a-authorization"); v != "keep-me" {
		t.Errorf("skipped server's secret overwritten: %q", v)
	}
	if v, _ := secrets.Get("b-b_token"); v != "tok-b" {
		t.Errorf("b secret = %q", v)
	}
	if !strings.Contains(errOut.String(), "already exists") || strings.Contains(out.String()+errOut.String(), "tok-") {
		t.Errorf("out=%q err=%q", out.String(), errOut.String())
	}

	out.Reset()
	errOut.Reset()
	if code := importInto(cfg, res, true, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	c, _ = config.Load(cfg)
	if c.Servers["a"].Type != "http" {
		t.Errorf("--force did not overwrite: %+v", c.Servers["a"])
	}
	if v, _ := secrets.Get("a-authorization"); v != "tok-a" {
		t.Errorf("forced secret = %q", v)
	}
}
