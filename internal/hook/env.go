// Package hook computes the skill environment of a folder for the shell hook (`skilltri env`,
// `skilltri shell-init`) and the launcher (`skilltri run`): the Claude Code view in
// CLAUDE_CODE_PLUGIN_DIRS, the opt-in project-folder links, and the marker variables. It reads only
// the config, the cached catalog and existing checkouts: no daemon, no git, no network.
package hook

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/baeroe/skilltri/internal/adapters"
	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/paths"
	"github.com/baeroe/skilltri/internal/rules"
	"github.com/baeroe/skilltri/internal/sources"
	"github.com/baeroe/skilltri/internal/views"
)

// Variables Compute sets or unsets.
const (
	PluginDirsVar = "CLAUDE_CODE_PLUGIN_DIRS"               // Claude Code plugin dirs, ":"-separated
	BaseVar       = "SKILLTRI_BASE_CLAUDE_CODE_PLUGIN_DIRS" // the user's own PluginDirsVar, saved on first use
	NativeVar     = "SKILLTRI_SKILLS_NATIVE"                // comma list of adapters that delivered natively
)

// Env is the change to apply to a shell or process environment. Notes are problems for stderr,
// never for stdout.
type Env struct {
	Set   map[string]string
	Unset []string
	Notes []string
}

// Compute returns the skill environment of dir. environ (KEY=value, later entries win) supplies the
// current PluginDirsVar and BaseVar; skilltri's own dirs come from the process environment. On first
// use (no BaseVar in environ) the current PluginDirsVar becomes the user base, which always stays
// first in PluginDirsVar and is restored when dir has no skills. Compute never fails: a config load
// error, a catalog that cannot be built or a missing folder yield only the marker variables (BaseVar
// and the restored base) plus a note. Without a cached catalog it builds one from the existing
// checkouts and saves it. For enabled project-dir adapters it syncs each distinct ProjectDir once,
// linking skills only where adapters.ProjectDirAllowed (HOME from the environment).
func Compute(dir string, environ []string) Env {
	vars := parse(environ)
	base, seen := vars[BaseVar]
	if !seen {
		base = vars[PluginDirsVar]
	}
	e := Env{Set: map[string]string{BaseVar: base}}

	dirs, native, notes := compute(dir)
	e.Notes = notes

	plugin := dirs
	if base != "" {
		plugin = append([]string{base}, dirs...)
	}
	if len(plugin) > 0 {
		e.Set[PluginDirsVar] = strings.Join(plugin, ":")
	} else {
		e.Unset = append(e.Unset, PluginDirsVar)
	}
	if len(native) > 0 {
		e.Set[NativeVar] = strings.Join(native, ",")
	} else {
		e.Unset = append(e.Unset, NativeVar)
	}
	return e
}

// compute returns the Claude view dirs of dir, the adapters that delivered, and the notes. A fatal
// problem returns only a note.
func compute(dir string) (dirs, native, notes []string) {
	if dir == "" {
		return nil, nil, []string{"no folder given"}
	}
	dir = rules.Normalize(dir)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		if err == nil {
			err = errors.New("not a folder")
		}
		return nil, nil, []string{fmt.Sprintf("%s: %v", dir, err)}
	}
	cfg, err := config.Load(paths.ConfigFile())
	if err != nil {
		return nil, nil, []string{"config: " + err.Error()}
	}
	cat, err := loadCatalog(cfg)
	if err != nil {
		return nil, nil, []string{"catalog: " + err.Error()}
	}
	active := catalog.Active(cat, cfg, dir)

	delivered := map[string]bool{} // ProjectDir → its one sync linked skills without error
	seen := map[string]bool{}
	// project-dir links only inside git work trees and never in HOME or above; elsewhere the sync
	// only removes links skilltri made earlier
	linkable := active
	if home, _ := os.UserHomeDir(); !adapters.ProjectDirAllowed(dir, home) {
		linkable = nil
	}
	for _, id := range cfg.EnabledAdapters() {
		if seen[id] {
			continue
		}
		seen[id] = true
		a, ok := adapters.ByID(id)
		if !ok {
			notes = append(notes, fmt.Sprintf("unknown adapter %q in settings.adapters", id))
			continue
		}
		switch a.Method {
		case adapters.MethodClaudePluginDirs:
			d, err := views.Claude(paths.CacheDir(), active)
			if err != nil {
				notes = append(notes, "claude view: "+err.Error())
				continue
			}
			if len(d) > 0 {
				dirs = d
				native = append(native, a.ID)
			}
		case adapters.MethodProjectDir:
			ok, done := delivered[a.ProjectDir]
			if !done {
				res, err := adapters.SyncProjectDir(dir, a.ProjectDir, linkable)
				if err != nil {
					notes = append(notes, fmt.Sprintf("%s: %v", a.ProjectDir, err))
				}
				ok = err == nil && len(res.Linked) > 0
				delivered[a.ProjectDir] = ok
			}
			if ok {
				native = append(native, a.ID)
			}
		}
	}
	return dirs, native, notes
}

// loadCatalog returns the cached catalog unless config.json is newer (an edit the daemon has not
// rebuilt yet, or no daemon at all); otherwise it builds one from the existing checkouts (never
// fetching) and saves it. A failed save only costs the next call a rebuild.
func loadCatalog(cfg *config.Config) (*catalog.Catalog, error) {
	if fresh(catalog.Path(), paths.ConfigFile()) {
		if c, err := catalog.Load(catalog.Path()); err == nil {
			return c, nil
		}
	}
	b := &catalog.Builder{Fetcher: &sources.Fetcher{Root: filepath.Join(paths.DataDir(), "sources")}}
	c, err := b.Build(context.Background(), cfg, false)
	if err != nil {
		return nil, err
	}
	_ = catalog.Save(catalog.Path(), c)
	return c, nil
}

// fresh reports whether the catalog file exists and is not older than the config file (a missing
// config counts as old).
func fresh(catalogPath, configPath string) bool {
	cat, err := os.Stat(catalogPath)
	if err != nil {
		return false
	}
	cfg, err := os.Stat(configPath)
	return err != nil || !cfg.ModTime().After(cat.ModTime())
}

// parse turns KEY=value entries into a map; later entries win.
func parse(environ []string) map[string]string {
	out := make(map[string]string, len(environ))
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out[k] = v
		}
	}
	return out
}
