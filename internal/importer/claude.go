package importer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/marketplace"
)

// ClaudeResult is what FromClaude found in a Claude Code home.
type ClaudeResult struct {
	Sources    map[string]*config.Source // marketplace sources by (sanitized) name
	UserSkills []string                  // absolute paths of user skill folders to copy
	Steps      []string                  // manual follow-ups to print; never executed
	Warnings   []string
}

var invalidSourceChars = regexp.MustCompile(`[^A-Za-z0-9-]`)

// FromClaude reads home/.claude (marketplaces, installed and enabled plugins, user skills).
// It only reads; missing files simply yield nothing.
func FromClaude(home string) (ClaudeResult, error) {
	r := ClaudeResult{Sources: map[string]*config.Source{}}
	claude := filepath.Join(home, ".claude")

	var known map[string]struct {
		Source struct {
			Source string `json:"source"`
			Repo   string `json:"repo"`
			URL    string `json:"url"`
			Path   string `json:"path"`
		} `json:"source"`
		InstallLocation string `json:"installLocation"`
	}
	if err := readJSON(filepath.Join(claude, "plugins", "known_marketplaces.json"), &known); err != nil {
		return r, err
	}
	var installedFile struct {
		Plugins map[string]json.RawMessage `json:"plugins"`
	}
	if err := readJSON(filepath.Join(claude, "plugins", "installed_plugins.json"), &installedFile); err != nil {
		return r, err
	}
	var settings struct {
		Enabled map[string]bool `json:"enabledPlugins"`
	}
	if err := readJSON(filepath.Join(claude, "settings.json"), &settings); err != nil {
		return r, err
	}

	// active = installed for the user and enabled
	active := map[string]bool{}
	for key, raw := range installedFile.Plugins {
		scopes := pluginScopes(raw)
		if !scopes["user"] {
			r.Warnings = append(r.Warnings, fmt.Sprintf("plugin %s is not installed for the user scope; treated as not installed", key))
			continue
		}
		active[key] = settings.Enabled[key]
	}

	mkNames := make([]string, 0, len(known))
	for n := range known {
		mkNames = append(mkNames, n)
	}
	sort.Strings(mkNames)
	for _, mk := range mkNames {
		k := known[mk]
		name := invalidSourceChars.ReplaceAllString(mk, "-")
		if !config.ValidServerName(name) {
			r.Warnings = append(r.Warnings, fmt.Sprintf("skipped marketplace %q: no valid source name", mk))
			continue
		}
		if r.Sources[name] != nil {
			r.Warnings = append(r.Warnings, fmt.Sprintf("skipped marketplace %q: source name %q is taken", mk, name))
			continue
		}
		src := &config.Source{Type: "marketplace"}
		switch k.Source.Source {
		case "github":
			src.URL = k.Source.Repo
		case "git":
			src.URL = k.Source.URL
		case "directory":
			src.Path = k.Source.Path
		}
		if src.URL == "" && src.Path == "" {
			r.Warnings = append(r.Warnings, fmt.Sprintf("skipped marketplace %q: unsupported source %q", mk, k.Source.Source))
			continue
		}
		if m, err := marketplace.Load(k.InstallLocation); err != nil {
			r.Warnings = append(r.Warnings, fmt.Sprintf("marketplace %q: cannot read its plugin list (%v); imported without per-plugin settings", mk, err))
		} else {
			for _, e := range m.Plugins {
				if !active[e.Name+"@"+mk] {
					if src.Plugins == nil {
						src.Plugins = map[string]*config.Node{}
					}
					src.Plugins[e.Name] = &config.Node{Disabled: true}
				}
			}
		}
		r.Sources[name] = src
		var plugins []string
		for key, on := range active {
			if on && strings.HasSuffix(key, "@"+mk) {
				plugins = append(plugins, key)
			}
		}
		sort.Strings(plugins)
		for _, key := range plugins {
			r.Steps = append(r.Steps, "claude plugin uninstall "+key)
		}
	}

	entries, err := os.ReadDir(filepath.Join(claude, "skills"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return r, err
	}
	for _, e := range entries {
		name := e.Name()
		if name == "synced" || strings.HasPrefix(name, ".") {
			continue
		}
		dir := filepath.Join(claude, "skills", name)
		if !e.IsDir() {
			if e.Type()&fs.ModeSymlink != 0 {
				r.Warnings = append(r.Warnings, fmt.Sprintf("skipped skill %q: symlinks are not followed", name))
			}
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
			r.Warnings = append(r.Warnings, fmt.Sprintf("skipped skill %q: no SKILL.md", name))
			continue
		}
		r.UserSkills = append(r.UserSkills, dir)
		r.Steps = append(r.Steps, "remove ~/.claude/skills/"+name)
	}
	if len(r.Sources) > 0 {
		r.Steps = append(r.Steps, `run "skilltri update" to fetch the imported marketplaces`)
		r.Steps = append(r.Steps, `plugin MCP servers are not enabled: see "skilltri plugin-server list"`)
	}
	return r, nil
}

// readJSON decodes path into v; a missing file leaves v untouched.
func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

// pluginScopes returns the scopes of an installed_plugins entry (an array, or a single object in old files).
func pluginScopes(raw json.RawMessage) map[string]bool {
	type entry struct {
		Scope string `json:"scope"`
	}
	var list []entry
	if json.Unmarshal(raw, &list) != nil {
		var one entry
		if json.Unmarshal(raw, &one) != nil {
			return nil
		}
		list = []entry{one}
	}
	scopes := map[string]bool{}
	for _, e := range list {
		scopes[e.Scope] = true
	}
	return scopes
}

// ApplyClaude writes the sources into the config at cfgPath, copies the user skills into skillsDir
// (ensuring a "local" source for it) and prints what happened, the warnings and the manual steps to
// out. Existing sources and skill folders are skipped unless force is set.
func ApplyClaude(cfgPath, skillsDir string, r ClaudeResult, force bool, out io.Writer) error {
	warnings := append([]string(nil), r.Warnings...)
	var lines []string
	names := make([]string, 0, len(r.Sources))
	for n := range r.Sources {
		names = append(names, n)
	}
	sort.Strings(names)

	var copied []string
	for _, dir := range r.UserSkills {
		name := filepath.Base(dir)
		target := filepath.Join(skillsDir, name)
		if _, err := os.Lstat(target); err == nil {
			if !force {
				warnings = append(warnings, fmt.Sprintf("skipped skill %q: already exists (use --force to overwrite)", name))
				continue
			}
			if err := os.RemoveAll(target); err != nil {
				return err
			}
		}
		if err := copyTree(dir, target); err != nil {
			return fmt.Errorf("copy skill %s: %w", name, err)
		}
		copied = append(copied, name)
	}

	err := config.Update(cfgPath, func(c *config.Config) error {
		lines = nil
		if c.Sources == nil {
			c.Sources = map[string]*config.Source{}
		}
		for _, n := range names {
			if c.Sources[n] != nil && !force {
				warnings = append(warnings, fmt.Sprintf("skipped source %q: already exists (use --force to overwrite)", n))
				continue
			}
			c.Sources[n] = r.Sources[n]
			lines = append(lines, "imported source "+n)
		}
		if len(r.UserSkills) > 0 && c.Sources["local"] == nil {
			c.Sources["local"] = &config.Source{Type: "local", Path: skillsDir}
			lines = append(lines, "added local source local")
		}
		return nil
	})
	if err != nil {
		return err
	}

	for _, l := range lines {
		fmt.Fprintln(out, l)
	}
	for _, n := range copied {
		fmt.Fprintln(out, "copied skill "+n)
	}
	for _, w := range warnings {
		fmt.Fprintf(out, "warning: %s\n", w)
	}
	if len(r.Steps) > 0 {
		fmt.Fprintln(out, "\nNext steps (not run by skilltri):")
		for _, s := range r.Steps {
			fmt.Fprintln(out, "  - "+s)
		}
	}
	return nil
}

// copyTree copies src to dst recursively, keeping file modes and copying symlinks as symlinks.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		to := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(target, to)
		case d.IsDir():
			return os.MkdirAll(to, 0o755)
		case info.Mode().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if err := os.WriteFile(to, b, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chmod(to, info.Mode().Perm())
		}
		return nil // sockets, devices
	})
}
