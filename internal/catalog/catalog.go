// Package catalog builds the skill catalog: every skill of every enabled source with its
// source → plugin → skill hierarchy, plus the MCP server definitions bundled with marketplace
// plugins. It is built from the config and the existing checkouts, cached as catalog.json, and
// evaluated per folder against the rules of each level (see active.go).
package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/fsutil"
	"github.com/baeroe/skilltri/internal/marketplace"
	"github.com/baeroe/skilltri/internal/paths"
	"github.com/baeroe/skilltri/internal/skilllock"
	"github.com/baeroe/skilltri/internal/skills"
	"github.com/baeroe/skilltri/internal/sources"
)

// Entry is one skill of the catalog. ID is the skill name for local and git sources and
// "<plugin.json name>:<skill name>" for marketplace plugins; Plugin is the plugin's entry name in
// marketplace.json (the key of config.Source.Plugins), "" for local and git sources.
type Entry struct {
	ID     string       `json:"id"`
	Source string       `json:"source"`
	Plugin string       `json:"plugin,omitempty"`
	Skill  skills.Skill `json:"skill"`
}

// PluginServer is an MCP server bundled with a marketplace plugin. Plugin is the entry name,
// Name the server's key in the plugin, DefHash = DefHash(Def).
type PluginServer struct {
	Source  string        `json:"source"`
	Plugin  string        `json:"plugin"`
	Name    string        `json:"name"`
	Def     config.Server `json:"def"`
	DefHash string        `json:"defHash"`
}

// SourceState is the fetch state of a source or of a remote plugin ("<source>/plugins/<entry>").
// Commit is the checkout in use (the last good one after a failed update).
type SourceState struct {
	Name      string    `json:"name"`
	Commit    string    `json:"commit,omitempty"`
	Error     string    `json:"error,omitempty"`
	FetchedAt time.Time `json:"fetchedAt,omitzero"`
}

// Catalog is the content of catalog.json. Skills are sorted by ID, Servers by source, plugin and
// name, Sources by name, so an unchanged build differs only in Built.
type Catalog struct {
	Skills   []Entry        `json:"skills"`
	Servers  []PluginServer `json:"servers"`
	Sources  []SourceState  `json:"sources"`
	Warnings []string       `json:"warnings"`
	Built    time.Time      `json:"built"`
}

// Builder builds catalogs from checkouts below Fetcher.Root. Lock is the skill lock path
// ("" → skilllock.Path()).
type Builder struct {
	Fetcher *sources.Fetcher
	Lock    string
}

// Path is the cached catalog: paths.CacheDir()/catalog.json.
func Path() string { return filepath.Join(paths.CacheDir(), "catalog.json") }

// Build builds the catalog of cfg. With sync=false it reads only existing checkouts and never runs git.
// With sync=true it first syncs the git and marketplace sources (and the remote plugins that are
// enabled, see config.Source.PluginEnabled) that are due: never fetched, whose checkout vanished, or
// fetched longer than the source's interval ago (interval 0 = manual only). A failed attempt is
// retried after the interval, also when it left no checkout (a failed first clone).
// A failing source never fails the build: its error goes into SourceState.Error and its last
// good checkout stays in use. The lock (commit, error, skill hashes) is written for synced sources;
// an unreadable or unwritable lock only adds a warning. Build currently never returns an error.
func (b *Builder) Build(ctx context.Context, cfg *config.Config, sync bool) (*Catalog, error) {
	lock, warn := b.loadLock()
	var synced map[string]SourceState
	if sync {
		synced = b.sync(ctx, cfg, lock, nil)
	}
	c, keys := b.assemble(cfg, lock)
	if warn != "" {
		c.Warnings = append([]string{warn}, c.Warnings...)
	}
	if len(synced) > 0 {
		recordHashes(lock, c, keys, synced)
		if err := skilllock.Save(b.lockPath(), lock); err != nil {
			c.Warnings = append(c.Warnings, "save skill lock: "+err.Error())
		}
	}
	return c, nil
}

// SyncSources syncs exactly the sources named in only (and their remote plugins), or every due one
// when only is empty, and records the result in the lock. It returns the state of every key it
// attempted, sorted by name; an unknown or disabled name in only comes back with an error.
// Problems with the lock itself (unreadable, unwritable) are appended to the error of every state.
func (b *Builder) SyncSources(ctx context.Context, cfg *config.Config, only []string) []SourceState {
	lock, warn := b.loadLock()
	var lockErrs []string
	if warn != "" {
		lockErrs = append(lockErrs, warn)
	}
	synced := b.sync(ctx, cfg, lock, only)
	if len(synced) > 0 {
		c, keys := b.assemble(cfg, lock)
		recordHashes(lock, c, keys, synced)
		if err := skilllock.Save(b.lockPath(), lock); err != nil {
			lockErrs = append(lockErrs, "save skill lock: "+err.Error())
		}
	}
	for k, st := range synced {
		for _, e := range lockErrs {
			st.Error = strings.TrimPrefix(st.Error+"; ", "; ") + e
		}
		synced[k] = st
	}
	for _, n := range only {
		if _, ok := synced[n]; ok {
			continue
		}
		switch src := cfg.Sources[n]; {
		case src == nil:
			synced[n] = SourceState{Name: n, Error: "unknown source"}
		case src.Disabled:
			synced[n] = SourceState{Name: n, Error: "source is disabled"}
		}
	}
	out := make([]SourceState, 0, len(synced))
	for _, st := range synced {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (b *Builder) lockPath() string {
	if b.Lock != "" {
		return b.Lock
	}
	return skilllock.Path()
}

// loadLock reads the lock; an unreadable lock is replaced by an empty one plus a warning.
func (b *Builder) loadLock() (*skilllock.Lock, string) {
	l, err := skilllock.Load(b.lockPath())
	if err != nil {
		return &skilllock.Lock{Version: 1, Sources: map[string]skilllock.Source{}}, fmt.Sprintf("skill lock ignored: %v", err)
	}
	return l, ""
}

// sync syncs the selected keys into lock and returns the state of each attempted key.
func (b *Builder) sync(ctx context.Context, cfg *config.Config, lock *skilllock.Lock, only []string) map[string]SourceState {
	listed := map[string]bool{}
	for _, n := range only {
		listed[n] = true
	}
	out := map[string]SourceState{}
	now := time.Now()
	for _, name := range sourceNames(cfg) {
		src := cfg.Sources[name]
		if src.Type == "local" || (len(only) > 0 && !listed[name]) {
			continue
		}
		interval, err := src.Interval()
		if err != nil {
			interval = 0
		}
		// due: listed, never fetched, a checkout that vanished, or the interval passed. A missing
		// checkout whose last attempt failed waits for the interval like any other retry.
		due := func(key string) bool {
			if listed[name] {
				return true
			}
			e, ok := lock.Sources[key]
			if !ok || e.FetchedAt.IsZero() || (e.Error == "" && !hasCheckout(b.Fetcher.Dir(key))) {
				return true
			}
			return interval > 0 && now.Sub(e.FetchedAt) >= interval
		}
		if fetched(src) && due(name) {
			out[name] = b.syncKey(ctx, lock, name, sources.RepoURL(src.URL), src.Ref)
		}
		if src.Type != "marketplace" {
			continue
		}
		root, _, err := b.sourceRoot(name, src)
		if err != nil {
			continue
		}
		m, err := marketplace.Load(root)
		if err != nil {
			continue // assemble reports it
		}
		for _, e := range m.Plugins {
			if !src.PluginEnabled(e.Name) {
				continue // disabled plugins are never fetched
			}
			_, remote, err := marketplace.ParseSource(e.Source)
			if err != nil || remote == nil {
				continue
			}
			key, err := pluginKey(name, e.Name)
			if err != nil || !due(key) {
				continue
			}
			out[key] = b.syncKey(ctx, lock, key, sources.RepoURL(remote.URL), remote.Ref)
		}
	}
	return out
}

// syncKey syncs one checkout and records commit, error and fetch time in lock. After a failed
// update the commit is the one still checked out.
func (b *Builder) syncKey(ctx context.Context, lock *skilllock.Lock, key, url, ref string) SourceState {
	prev := lock.Sources[key]
	e := skilllock.Source{FetchedAt: time.Now().UTC().Round(0), Commit: prev.Commit, Skills: prev.Skills}
	if co, err := b.Fetcher.Sync(ctx, key, url, ref); err != nil {
		e.Error = err.Error()
		if cur, ok := b.Fetcher.Current(key); ok {
			e.Commit = cur.Commit
		}
	} else {
		e.Commit = co.Commit
	}
	lock.Sources[key] = e
	return SourceState{Name: key, Commit: e.Commit, Error: e.Error, FetchedAt: e.FetchedAt}
}

// recordHashes replaces the skill hashes of every synced key with those of its catalog skills.
func recordHashes(lock *skilllock.Lock, c *Catalog, keys map[string]string, synced map[string]SourceState) {
	for k := range synced {
		if e, ok := lock.Sources[k]; ok {
			e.Skills = map[string]string{}
			lock.Sources[k] = e
		}
	}
	for _, en := range c.Skills {
		k := keys[en.ID]
		if _, ok := synced[k]; !ok {
			continue
		}
		h, err := skilllock.HashDir(en.Skill.Dir)
		if err != nil {
			c.Warnings = append(c.Warnings, fmt.Sprintf("%s: hash skill %s: %v", en.Source, en.ID, err))
			continue
		}
		lock.Sources[k].Skills[en.ID] = h
	}
}

// assemble builds the catalog from the existing checkouts without running git. The second value
// maps each skill ID to the lock key of the checkout it comes from.
func (b *Builder) assemble(cfg *config.Config, lock *skilllock.Lock) (*Catalog, map[string]string) {
	c := &Catalog{Built: time.Now().UTC().Round(0)}
	owner := map[string]string{}
	keys := map[string]string{}
	add := func(source, key, plugin, id string, s skills.Skill) {
		from := source // "<source>" or, for plugin skills, "<source>/<entry>"
		if plugin != "" {
			from += "/" + plugin
		}
		if first, ok := owner[id]; ok {
			c.Warnings = append(c.Warnings, fmt.Sprintf("skill %s from %s hidden by %s", id, from, first))
			return
		}
		owner[id], keys[id] = from, key
		c.Skills = append(c.Skills, Entry{ID: id, Source: source, Plugin: plugin, Skill: s})
	}
	state := func(key string) SourceState {
		e := lock.Sources[key]
		return SourceState{Name: key, Commit: e.Commit, Error: e.Error, FetchedAt: e.FetchedAt}
	}

	for _, name := range sourceNames(cfg) {
		src := cfg.Sources[name]
		warn := func(format string, args ...any) {
			c.Warnings = append(c.Warnings, name+": "+fmt.Sprintf(format, args...))
		}
		st := SourceState{Name: name}
		if fetched(src) {
			st = state(name)
		}
		root, guard, err := b.sourceRoot(name, src)
		if err != nil && st.Error == "" {
			st.Error = err.Error()
		}
		c.Sources = append(c.Sources, st)
		if err != nil {
			continue
		}

		if src.Type != "marketplace" {
			for _, s := range discover(root, guard, warn) {
				add(name, name, "", s.Name, s)
			}
			continue
		}

		m, err := marketplace.Load(root)
		if err != nil {
			warn("%v", err)
			continue
		}
		entries := append([]marketplace.Entry(nil), m.Plugins...)
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
		for _, e := range entries {
			local, remote, err := marketplace.ParseSource(e.Source)
			if err != nil {
				warn("plugin %s: %v", e.Name, err)
				continue
			}
			enabled := src.PluginEnabled(e.Name)
			key, dir, pguard := name, filepath.Join(root, filepath.FromSlash(path.Clean(local))), guard
			if remote != nil {
				if key, err = pluginKey(name, e.Name); err != nil {
					warn("plugin %s: %v", e.Name, err)
					continue
				}
				ps := state(key)
				co := b.Fetcher.Dir(key)
				switch {
				case !enabled && !hasCheckout(co):
					continue // never fetched while disabled: no state, no servers
				case !enabled:
					ps.Error = "" // not synced while disabled: an old error is stale
				case !hasCheckout(co) && ps.Error == "":
					ps.Error = "not fetched yet"
				}
				c.Sources = append(c.Sources, ps)
				if !hasCheckout(co) {
					continue
				}
				dir, pguard = co, co
				if remote.Path != "" {
					dir = filepath.Join(co, filepath.FromSlash(remote.Path))
				}
			}
			if ok, err := inside(dir, pguard); err != nil || !ok {
				if err == nil {
					err = fmt.Errorf("folder resolves outside %s", pguard)
				}
				warn("plugin %s ignored: %v", e.Name, err)
				continue
			}
			p, ws, err := marketplace.LoadPlugin(e, dir)
			for _, w := range ws {
				warn("%s", w)
			}
			if err != nil {
				warn("plugin %s ignored: %v", e.Name, err)
				continue
			}
			for _, sn := range sortedKeys(p.Servers) {
				def := p.Servers[sn]
				c.Servers = append(c.Servers, PluginServer{Source: name, Plugin: e.Name, Name: sn, Def: def, DefHash: DefHash(def)})
			}
			if !enabled {
				continue
			}
			for i, r := range p.SkillRoots {
				if _, err := os.Stat(r); err != nil {
					if i > 0 || !errors.Is(err, os.ErrNotExist) { // the default skills/ folder is optional
						warn("plugin %s: skills folder: %v", e.Name, err)
					}
					continue
				}
				for _, s := range discover(r, pguard, warn) {
					add(name, key, e.Name, p.Name+":"+s.Name, s)
				}
			}
		}
	}

	sort.Slice(c.Skills, func(i, j int) bool { return c.Skills[i].ID < c.Skills[j].ID })
	sort.Slice(c.Servers, func(i, j int) bool {
		a, b := c.Servers[i], c.Servers[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Plugin != b.Plugin {
			return a.Plugin < b.Plugin
		}
		return a.Name < b.Name
	})
	sort.Slice(c.Sources, func(i, j int) bool { return c.Sources[i].Name < c.Sources[j].Name })
	return c, keys
}

// sourceRoot returns the folder a source's skills (or marketplace.json) live in, and the folder
// they must resolve under: for local sources the expanded Path, else the checkout (or the
// marketplace Path), each plus Subpath.
func (b *Builder) sourceRoot(name string, src *config.Source) (root, guard string, err error) {
	switch {
	case !fetched(src):
		root = expandHome(src.Path)
	default:
		root = b.Fetcher.Dir(name)
		if !hasCheckout(root) {
			return "", "", errors.New("not fetched yet")
		}
	}
	if src.Type != "local" && src.Subpath != "" {
		sub := filepath.FromSlash(src.Subpath)
		if !filepath.IsLocal(sub) {
			return "", "", fmt.Errorf("invalid subpath %q", src.Subpath)
		}
		root = filepath.Join(root, sub)
	}
	st, err := os.Stat(root)
	if err != nil {
		return "", "", err
	}
	if !st.IsDir() {
		return "", "", fmt.Errorf("%s is not a folder", root)
	}
	return root, root, nil
}

// discover returns the skills below dir whose real path lies under guard; others are dropped with a warning.
func discover(dir, guard string, warn func(string, ...any)) []skills.Skill {
	found, ws := skills.Discover(dir)
	for _, w := range ws {
		warn("%s", w)
	}
	var out []skills.Skill
	for _, s := range found {
		if ok, err := inside(s.Dir, guard); err != nil || !ok {
			warn("skill %s resolves outside %s, ignored", s.Dir, guard)
			continue
		}
		out = append(out, s)
	}
	return out
}

// inside reports whether the real path of p is root's real path or below it.
func inside(p, root string) (bool, error) {
	rp, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false, err
	}
	rr, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false, err
	}
	return rp == rr || strings.HasPrefix(rp, strings.TrimSuffix(rr, string(filepath.Separator))+string(filepath.Separator)), nil
}

// pluginKey is the checkout key of a remote plugin; the entry name must be one safe path segment.
func pluginKey(source, entry string) (string, error) {
	if entry == "" || entry == "." || entry == ".." || strings.ContainsAny(entry, `/\`) {
		return "", fmt.Errorf("plugin name %q cannot be used as a folder name", entry)
	}
	return source + "/plugins/" + entry, nil
}

// fetched reports whether the source itself is a checkout: git, or a marketplace from a URL.
func fetched(src *config.Source) bool {
	return src.Type == "git" || (src.Type == "marketplace" && src.Path == "")
}

func hasCheckout(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[1:])
		}
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return p
}

// sourceNames returns the enabled sources in name order.
func sourceNames(cfg *config.Config) []string {
	var out []string
	for n, s := range cfg.Sources {
		if s != nil && !s.Disabled {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Save writes c to path atomically with mode 0600, creating the folder if needed.
func Save(path string, c *Catalog) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, append(b, '\n'), 0o600)
}

// Load reads a catalog written by Save. A missing file is an error matching os.ErrNotExist.
func Load(path string) (*Catalog, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := &Catalog{}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return c, nil
}
