// Package marketplace reads Claude Code plugin marketplaces
// (.claude-plugin/marketplace.json), resolves plugin sources and loads a
// plugin's skill roots and bundled MCP servers.
package marketplace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Entry is one plugin listed in a marketplace.
type Entry struct {
	Name       string
	Source     json.RawMessage
	Skills     []string        // extra skill paths declared on the entry
	MCPServers json.RawMessage // inline servers or a relative path to a JSON file
}

// UnmarshalJSON accepts `skills` as a string or an array of strings.
func (e *Entry) UnmarshalJSON(b []byte) error {
	var aux struct {
		Name       string          `json:"name"`
		Source     json.RawMessage `json:"source"`
		Skills     json.RawMessage `json:"skills"`
		MCPServers json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	skills, err := stringOrList(aux.Skills)
	if err != nil {
		return fmt.Errorf("plugin %q: skills: %w", aux.Name, err)
	}
	*e = Entry{Name: aux.Name, Source: aux.Source, Skills: skills, MCPServers: aux.MCPServers}
	return nil
}

// Marketplace is a parsed marketplace.json.
type Marketplace struct {
	Name       string
	PluginRoot string // metadata.pluginRoot, as written ("" if unset)
	Plugins    []Entry
}

// Load reads root/.claude-plugin/marketplace.json. Bare relative sources are
// prefixed with metadata.pluginRoot, so Entry.Source is ready for ParseSource.
func Load(root string) (*Marketplace, error) {
	file := filepath.Join(root, ".claude-plugin", "marketplace.json")
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read marketplace: %w", err)
	}
	var raw struct {
		Name     string `json:"name"`
		Metadata struct {
			PluginRoot string `json:"pluginRoot"`
		} `json:"metadata"`
		Plugins []Entry `json:"plugins"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", file, err)
	}
	if raw.Name == "" {
		return nil, fmt.Errorf("%s: missing name", file)
	}
	m := &Marketplace{Name: raw.Name, PluginRoot: raw.Metadata.PluginRoot, Plugins: raw.Plugins}
	names := map[string]bool{}
	for i, e := range m.Plugins {
		if e.Name == "" {
			return nil, fmt.Errorf("%s: plugin #%d has no name", file, i+1)
		}
		if names[e.Name] {
			return nil, fmt.Errorf("%s: duplicate plugin name %q", file, e.Name)
		}
		names[e.Name] = true
		var s string
		if json.Unmarshal(e.Source, &s) == nil && s != "" && !strings.HasPrefix(s, "./") && m.PluginRoot != "" {
			if _, err := safeRel(s); err != nil {
				return nil, fmt.Errorf("%s: plugin %q source %w", file, e.Name, err)
			}
			joined := "./" + path.Join(m.PluginRoot, s)
			m.Plugins[i].Source, _ = json.Marshal(joined)
		}
	}
	return m, nil
}

func stringOrList(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []string{s}, nil
	}
	var l []string
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, errors.New("want a string or an array of strings")
	}
	return l, nil
}

// safeRel cleans a relative path and rejects absolute paths and traversal.
func safeRel(p string) (string, error) {
	if p == "" || path.IsAbs(p) || filepath.IsAbs(p) {
		return "", fmt.Errorf("%q must be a relative path", p)
	}
	c := path.Clean(p)
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("%q points outside the plugin", p)
	}
	return c, nil
}
