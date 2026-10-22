// Package views builds harness views of the active skill set outside any project folder. A view
// lives in <cache>/views/<hash of the set>/ and is content-addressed: it is built once into a temp
// dir and renamed into place, never modified afterwards, shared by every folder with the same set
// and garbage-collected once its .used marker is old.
package views

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/baeroe/skilltri/internal/catalog"
)

const (
	// LocalPlugin is the Claude plugin that holds skills from local and git sources.
	LocalPlugin = "skilltri"
	usedFile    = ".used"
)

// Claude returns the plugin dirs of the Claude Code view of active (for CLAUDE_CODE_PLUGIN_DIRS),
// sorted. The view is cacheDir/views/<hash>/claude/<plugin>, one plugin per source plugin (named
// like the original plugin, so skills keep their <plugin>:<skill> namespace) plus LocalPlugin for
// local and git skills; each has .claude-plugin/plugin.json and skills/<name> symlinks to the skill
// folders. An existing view is reused as is; every call touches <view>/.used. An empty set has no
// plugin dirs and builds nothing.
func Claude(cacheDir string, active []catalog.Entry) ([]string, error) {
	plugins := group(active)
	if len(plugins) == 0 {
		return nil, nil
	}
	root := filepath.Join(cacheDir, "views")
	view := filepath.Join(root, hash(active))
	for attempt := 0; ; attempt++ {
		if _, err := os.Stat(view); errors.Is(err, os.ErrNotExist) {
			if err := build(root, view, plugins); err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		}
		if beforeTouch != nil {
			beforeTouch(view)
		}
		err := touch(filepath.Join(view, usedFile))
		if err == nil {
			break
		}
		if attempt > 0 || !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		// GC removed the view after the existence check: rebuild once
	}
	names := make([]string, 0, len(plugins))
	for name := range plugins {
		names = append(names, name)
	}
	sort.Strings(names)
	dirs := make([]string, len(names))
	for i, name := range names {
		dirs[i] = filepath.Join(view, "claude", name)
	}
	return dirs, nil
}

// GC removes every view whose .used marker (or, without one, the view dir itself) was last modified
// more than maxAge ago, and leftover temp dirs of the same age. Removing a view removes its
// symlinks, never the skill folders they point to. A missing views dir is not an error.
func GC(cacheDir string, maxAge time.Duration) error {
	root := filepath.Join(cacheDir, "views")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-maxAge)
	var errs []error
	for _, e := range entries {
		if !e.IsDir() { // also skips symlinks: GC never follows a link out of the views dir
			continue
		}
		dir := filepath.Join(root, e.Name())
		st, err := os.Lstat(filepath.Join(dir, usedFile))
		if err != nil {
			st, err = os.Lstat(dir)
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if st.ModTime().Before(cutoff) {
			if err := os.RemoveAll(dir); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// group maps each Claude plugin name to its skills (link name → skill dir). The plugin of a
// marketplace skill is the part of its ID before ":" (the plugin.json name), local and git skills
// go into LocalPlugin. Names that are not a single path element are skipped; on a duplicate link
// name the first entry wins. Skill dirs are made absolute (symlink targets).
func group(active []catalog.Entry) map[string]map[string]string {
	plugins := map[string]map[string]string{}
	for _, e := range active {
		plugin, name := LocalPlugin, e.ID
		if e.Plugin != "" {
			i := strings.Index(e.ID, ":")
			if i < 0 {
				continue
			}
			plugin, name = e.ID[:i], e.ID[i+1:]
		}
		if !SafeName(plugin) || !SafeName(name) || e.Skill.Dir == "" {
			continue
		}
		dir, err := filepath.Abs(e.Skill.Dir)
		if err != nil {
			continue
		}
		if plugins[plugin] == nil {
			plugins[plugin] = map[string]string{}
		}
		if _, ok := plugins[plugin][name]; !ok {
			plugins[plugin][name] = dir
		}
	}
	return plugins
}

// SafeName reports whether name can be used as one path element (not empty, ".", ".." or containing
// a separator or NUL).
func SafeName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}

// hash is the first 16 hex chars of the sha256 of the sorted "id\tdir" lines of active (absolute dirs).
func hash(active []catalog.Entry) string {
	lines := make([]string, len(active))
	for i, e := range active {
		dir := e.Skill.Dir
		if abs, err := filepath.Abs(dir); err == nil && dir != "" {
			dir = abs
		}
		lines[i] = e.ID + "\t" + dir + "\n"
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "")))
	return hex.EncodeToString(sum[:])[:16]
}

// build writes the view into a temp dir under root and renames it to view. If a concurrent builder
// renamed its view first, the temp dir is dropped and the existing view is used.
func build(root, view string, plugins map[string]map[string]string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(root, "."+filepath.Base(view)+"-*.tmp")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp) // no-op after a successful rename
	for plugin, links := range plugins {
		dir := filepath.Join(tmp, "claude", plugin)
		if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(dir, "skills"), 0o755); err != nil {
			return err
		}
		manifest, err := json.Marshal(map[string]string{"name": plugin})
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), manifest, 0o644); err != nil {
			return err
		}
		for name, target := range links {
			if err := os.Symlink(target, filepath.Join(dir, "skills", name)); err != nil {
				return err
			}
		}
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, view); err != nil {
		if _, statErr := os.Stat(view); statErr == nil {
			return nil // lost the race: the other builder's view has the same content
		}
		return err
	}
	return nil
}

// touch sets the mtime of path to now, creating it if needed.
func touch(path string) error {
	now := time.Now()
	if err := os.Chtimes(path, now, now); err == nil {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chtimes(path, now, now)
}

// beforeTouch is a test hook called with the view dir before .used is touched.
var beforeTouch func(view string)
