package marketplace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/skills"
)

// Plugin is a plugin checked out in Dir.
type Plugin struct {
	Entry      string // name in marketplace.json
	Name       string // plugin.json name, else Entry
	Dir        string
	SkillRoots []string                 // Dir/skills plus extra `skills` paths from plugin.json and the entry
	Servers    map[string]config.Server // from Dir/.mcp.json and plugin.json mcpServers; ${CLAUDE_PLUGIN_ROOT} replaced with Dir
	// Extras is what the plugin holds that skilltri does not deliver, sorted: "LSP servers",
	// "agents", "commands", "hooks" (from the folders, .lsp.json or the plugin.json fields).
	Extras []string
}

// HasSkills reports whether any skill root holds at least one SKILL.md.
func (p Plugin) HasSkills() bool {
	for _, r := range p.SkillRoots {
		if found, _ := skills.Discover(r); len(found) > 0 {
			return true
		}
	}
	return false
}

// extras lists the parts of the plugin in dir skilltri does not deliver; manifest holds the
// plugin.json fields that can declare them.
func extras(dir string, manifest map[string]json.RawMessage) []string {
	declared := func(field string) bool {
		v := manifest[field]
		return len(v) > 0 && string(v) != "null" && string(v) != "[]" && string(v) != "{}" && string(v) != `""`
	}
	exists := func(rel string) bool {
		_, err := os.Stat(filepath.Join(dir, rel))
		return err == nil
	}
	var out []string
	for _, k := range []struct{ name, path, field string }{
		{"LSP servers", ".lsp.json", "lspServers"},
		{"agents", "agents", "agents"},
		{"commands", "commands", "commands"},
		{"hooks", "hooks", "hooks"},
	} {
		if exists(k.path) || declared(k.field) {
			out = append(out, k.name)
		}
	}
	return out
}

// LoadPlugin reads the plugin in dir. Servers are merged in the order
// .mcp.json, plugin.json mcpServers, marketplace entry mcpServers; a later
// definition of the same name wins and produces a warning. Problems that do not stop the plugin
// from working (unsafe paths, unsupported servers) come back as warnings.
func LoadPlugin(entry Entry, dir string) (Plugin, []string, error) {
	p := Plugin{Entry: entry.Name, Name: entry.Name, Dir: dir, Servers: map[string]config.Server{}}
	var warns []string

	var manifest struct {
		Name       string          `json:"name"`
		Skills     json.RawMessage `json:"skills"`
		MCPServers json.RawMessage `json:"mcpServers"`
	}
	var fields map[string]json.RawMessage
	mf := filepath.Join(dir, ".claude-plugin", "plugin.json")
	if b, err := os.ReadFile(mf); err == nil {
		if err := json.Unmarshal(b, &manifest); err != nil {
			return p, nil, fmt.Errorf("parse %s: %w", mf, err)
		}
		_ = json.Unmarshal(b, &fields) // an object, as the line above checked
	} else if !errors.Is(err, os.ErrNotExist) {
		return p, nil, err
	}
	p.Extras = extras(dir, fields)
	if manifest.Name != "" {
		p.Name = manifest.Name
	}

	extra, err := stringOrList(manifest.Skills)
	if err != nil {
		return p, nil, fmt.Errorf("%s: skills: %w", mf, err)
	}
	seen := map[string]bool{}
	add := func(abs string) {
		if !seen[abs] {
			seen[abs] = true
			p.SkillRoots = append(p.SkillRoots, abs)
		}
	}
	add(filepath.Join(dir, "skills"))
	for _, s := range append(extra, entry.Skills...) {
		rel, err := safeRel(s)
		if err != nil {
			warns = append(warns, fmt.Sprintf("plugin %s: skills path ignored: %v", p.Entry, err))
			continue
		}
		add(filepath.Join(dir, filepath.FromSlash(rel)))
	}

	load := func(origin string, raw json.RawMessage) {
		w, err := p.mergeServers(origin, raw)
		warns = append(warns, w...)
		if err != nil {
			warns = append(warns, fmt.Sprintf("plugin %s: %s: %v", p.Entry, origin, err))
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, ".mcp.json")); err == nil {
		load(".mcp.json", b)
	} else if !errors.Is(err, os.ErrNotExist) {
		warns = append(warns, fmt.Sprintf("plugin %s: .mcp.json: %v", p.Entry, err))
	}
	load("plugin.json mcpServers", manifest.MCPServers)
	load("marketplace mcpServers", entry.MCPServers)
	return p, warns, nil
}

// mergeServers adds the servers described by raw: an object of servers, a
// {"mcpServers": {...}} wrapper or, for a string, a relative path to a JSON
// file holding either (read once, its content must be an object).
func (p *Plugin) mergeServers(origin string, raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var rel string
	if json.Unmarshal(raw, &rel) == nil {
		c, err := safeRel(rel)
		if err != nil {
			return []string{fmt.Sprintf("plugin %s: %s ignored: %v", p.Entry, origin, err)}, nil
		}
		b, err := os.ReadFile(filepath.Join(p.Dir, filepath.FromSlash(c)))
		if err != nil {
			return nil, err
		}
		return p.mergeObject(origin, b) // file content is never followed again
	}
	return p.mergeObject(origin, raw)
}

// mergeObject parses raw as a server map or an {"mcpServers": {...}} wrapper.
func (p *Plugin) mergeObject(origin string, raw json.RawMessage) ([]string, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if inner, ok := m["mcpServers"]; ok {
		var wrapped map[string]json.RawMessage
		if err := json.Unmarshal(inner, &wrapped); err != nil {
			return nil, fmt.Errorf("mcpServers: %w", err)
		}
		m = wrapped
	}
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	var warns []string
	for _, n := range names {
		srv, warn, ok := p.convertServer(n, m[n])
		if warn != "" {
			warns = append(warns, warn)
		}
		if ok {
			if _, dup := p.Servers[n]; dup {
				warns = append(warns, fmt.Sprintf("plugin %s: mcp server %q defined more than once, %s wins", p.Entry, n, origin))
			}
			p.Servers[n] = srv
		}
	}
	return warns, nil
}

func (p *Plugin) convertServer(name string, raw json.RawMessage) (config.Server, string, bool) {
	var c struct {
		Type    string            `json:"type"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
		Cwd     string            `json:"cwd"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return config.Server{}, fmt.Sprintf("plugin %s: mcp server %q skipped: %v", p.Entry, name, err), false
	}
	typ := strings.ToLower(c.Type)
	switch typ {
	case "":
		switch {
		case c.Command != "":
			typ = "stdio"
		case c.URL != "":
			typ = "http"
		default:
			return config.Server{}, fmt.Sprintf("plugin %s: mcp server %q skipped: no command or url", p.Entry, name), false
		}
	case "stdio", "http":
	case "streamable-http":
		typ = "http"
	case "sse":
		return config.Server{}, fmt.Sprintf("plugin %s: mcp server %q skipped: sse transport is not supported", p.Entry, name), false
	default:
		return config.Server{}, fmt.Sprintf("plugin %s: mcp server %q skipped: unknown type %q", p.Entry, name, c.Type), false
	}
	sub := func(s string) string { return strings.ReplaceAll(s, "${CLAUDE_PLUGIN_ROOT}", p.Dir) }
	srv := config.Server{Type: typ}
	if typ == "stdio" {
		if c.Command == "" {
			return config.Server{}, fmt.Sprintf("plugin %s: mcp server %q skipped: stdio without command", p.Entry, name), false
		}
		srv.Command = sub(c.Command)
		for _, a := range c.Args {
			srv.Args = append(srv.Args, sub(a))
		}
		srv.Cwd = sub(c.Cwd)
	} else {
		if c.URL == "" {
			return config.Server{}, fmt.Sprintf("plugin %s: mcp server %q skipped: http without url", p.Entry, name), false
		}
		srv.URL = sub(c.URL)
		srv.Headers = subMap(c.Headers, sub)
	}
	srv.Env = subMap(c.Env, sub)
	return srv, "", true
}

func subMap(m map[string]string, sub func(string) string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = sub(v)
	}
	return out
}
