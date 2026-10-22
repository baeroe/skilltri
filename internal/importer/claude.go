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
	Sources    map[string]*config.Source    // marketplace sources by (sanitized) name
	UserSkills []string                     // absolute paths of user skill folders to copy
	Steps      []string                     // static manual follow-ups to print; never executed
	Uninstall  map[string][]PluginUninstall // source name → enabled Claude Code plugins whose skills skilltri now delivers
	Home       string                       // the Claude home that was read, for the remove steps
	Warnings   []string
}

// PluginUninstall is a plugin Claude Code still has enabled and that has skills skilltri now
// delivers. Plugins without skills get none: uninstalling them would only lose what they have.
type PluginUninstall struct {
	Key     string   // "plugin@marketplace"
	Extras  []string // what it holds that skilltri does not deliver (marketplace.Plugin.Extras)
	Unknown bool     // its install folder could not be read
}

// Step is the manual follow-up for p.
func (p PluginUninstall) Step() string {
	step := "claude plugin uninstall " + p.Key
	switch {
	case p.Unknown:
		step += " (after checking that it holds only skills and MCP servers: its install folder could not be read)"
	case len(p.Extras) > 0:
		step += " (only if you do not need its " + joinAnd(p.Extras) + ": skilltri does not deliver them, uninstalling loses them)"
	}
	return step
}

// joinAnd joins items as "a", "a and b", "a, b and c".
func joinAnd(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

var invalidSourceChars = regexp.MustCompile(`[^A-Za-z0-9-]`)

// FromClaude reads home/.claude (marketplaces, installed and enabled plugins, user skills).
// It only reads; missing files simply yield nothing.
func FromClaude(home string) (ClaudeResult, error) {
	r := ClaudeResult{Sources: map[string]*config.Source{}, Uninstall: map[string][]PluginUninstall{}, Home: home}
	claude := filepath.Join(home, ".claude")

	var known map[string]struct {
		Source struct {
			Source string `json:"source"`
			Repo   string `json:"repo"`
			URL    string `json:"url"`
			Path   string `json:"path"`
		} `json:"source"`
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
	installPath := map[string]string{}
	for key, raw := range installedFile.Plugins {
		scopes := pluginScopes(raw)
		if p, ok := scopes["user"]; ok {
			installPath[key] = p
		} else {
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
		// Only the plugins Claude Code has installed and enabled are on, also later upstream additions stay off.
		src.PluginsDefault = config.PluginsDisabled
		r.Sources[name] = src
		var keys []string
		for key, on := range active {
			if i := strings.LastIndex(key, "@"); on && i >= 0 && key[i+1:] == mk {
				keys = append(keys, key)
				if src.Plugins == nil {
					src.Plugins = map[string]*config.Node{}
				}
				src.Plugins[key[:i]] = &config.Node{}
			}
		}
		sort.Strings(keys)
		var plugins []PluginUninstall
		for _, key := range keys {
			p, err := inspect(key, installPath[key])
			if err != nil {
				plugins = append(plugins, PluginUninstall{Key: key, Unknown: true})
				continue
			}
			if p.HasSkills() {
				plugins = append(plugins, PluginUninstall{Key: key, Extras: p.Extras})
			}
		}
		r.Uninstall[name] = plugins
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

// inspect loads the installed plugin key ("plugin@marketplace") from Claude Code's install folder.
func inspect(key, dir string) (marketplace.Plugin, error) {
	if dir == "" {
		return marketplace.Plugin{}, errors.New("no install folder")
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return marketplace.Plugin{}, fmt.Errorf("install folder %s missing", dir)
	}
	p, _, err := marketplace.LoadPlugin(marketplace.Entry{Name: key[:strings.LastIndex(key, "@")]}, dir)
	return p, err
}

// pluginScopes returns the scopes of an installed_plugins entry (an array, or a single object in
// old files), each with its install folder.
func pluginScopes(raw json.RawMessage) map[string]string {
	type entry struct {
		Scope       string `json:"scope"`
		InstallPath string `json:"installPath"`
	}
	var list []entry
	if json.Unmarshal(raw, &list) != nil {
		var one entry
		if json.Unmarshal(raw, &one) != nil {
			return nil
		}
		list = []entry{one}
	}
	scopes := map[string]string{}
	for _, e := range list {
		scopes[e.Scope] = e.InstallPath
	}
	return scopes
}

// ApplyClaude writes the sources into the config at cfgPath, copies the user skills into skillsDir
// (ensuring a "local" source for it) and prints what happened, the warnings and the manual steps to
// out. Existing sources and skill folders are skipped unless force is set.
func ApplyClaude(cfgPath, skillsDir string, r ClaudeResult, force bool, out io.Writer) error {
	var warnings, lines []string
	names := make([]string, 0, len(r.Sources))
	for n := range r.Sources {
		names = append(names, n)
	}
	sort.Strings(names)

	var steps []string
	err := config.Update(cfgPath, func(c *config.Config) error {
		lines, steps = nil, nil
		warnings = append([]string(nil), r.Warnings...)
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
			for _, p := range r.Uninstall[n] {
				steps = append(steps, p.Step())
			}
		}
		if len(r.UserSkills) > 0 {
			switch l := c.Sources["local"]; {
			case l == nil:
				c.Sources["local"] = &config.Source{Type: "local", Path: skillsDir}
				lines = append(lines, "added local source local")
			case l.Type != "local" || l.Path != skillsDir:
				warnings = append(warnings, fmt.Sprintf("source \"local\" does not point at %s: the copied skills are in no source", skillsDir))
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	var copied []string
	for _, dir := range r.UserSkills {
		name := filepath.Base(dir)
		target := filepath.Join(skillsDir, name)
		if _, err := os.Lstat(target); err == nil && !force {
			warnings = append(warnings, fmt.Sprintf("skipped skill %q: already exists (use --force to overwrite)", name))
			continue
		}
		if err := installTree(dir, target); err != nil {
			return fmt.Errorf("copy skill %s: %w", name, err)
		}
		copied = append(copied, name)
		steps = append(steps, "remove "+filepath.Join(r.Home, ".claude", "skills", name))
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
	steps = append(steps, r.Steps...)
	if len(steps) > 0 {
		fmt.Fprintln(out, "\nNext steps (not run by skilltri):")
		for _, s := range steps {
			fmt.Fprintln(out, "  - "+s)
		}
	}
	return nil
}

// installTree copies src into a temp folder next to dst and renames it, replacing dst, so a
// failed copy never leaves a partial skill folder.
func installTree(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dst), ".import-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := copyTree(src, tmp); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil { // MkdirTemp creates 0700
		return err
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
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
