// Package doctor runs read-only health checks over skilltri's config, daemon, sources, skills,
// views, project skill folders and the harness setup. No check writes a file, runs git, touches
// the network or starts the daemon; the daemon is only asked for its status when its socket exists.
package doctor

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
)

// Severities.
const (
	Error   = "error"
	Warning = "warning"
	Info    = "info"
)

// Finding is one problem or hint: Check names the check, Subject what it is about, Fix how to
// resolve it ("" when there is nothing to run).
type Finding struct {
	Severity string `json:"severity"` // "error" | "warning" | "info"
	Check    string `json:"check"`
	Subject  string `json:"subject"`
	Message  string `json:"message"`
	Fix      string `json:"fix"`
}

// Env is where the checks look; injectable for tests. StatePath is the skill lock, the catalog is
// CacheDir/catalog.json and the views CacheDir/views. The current folder (for the project-dir and
// duplicate checks) is PWD from Environ, else the working directory.
type Env struct {
	Home, ConfigPath, Socket, CacheDir, StatePath string
	Environ                                       []string
}

// state is what the checks share, loaded once.
type state struct {
	env    Env
	vars   map[string]string
	dir    string
	cfg    *config.Config // never nil: an unloadable config is replaced by an empty one
	cfgErr error
	cat    *catalog.Catalog // nil when there is none
	catErr error
}

// checks run in this order; Run sorts the result.
var checks = []func(context.Context, *state) []Finding{
	checkConfig, checkDaemon, checkSources, checkSkills, checkLock, checkViews, checkProjectDirs,
	checkDuplicates, checkPluginServers, checkHarness, checkHarnessDuplicates, checkShellHook, checkAdapters,
	checkAgentAccess,
}

// Run runs every check and returns the findings sorted: errors, warnings, infos; then by Check,
// Subject. It stops between checks when ctx is done and then returns nil.
func Run(ctx context.Context, e Env) []Finding {
	s := &state{env: e, vars: map[string]string{}}
	for _, kv := range e.Environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			s.vars[k] = v
		}
	}
	s.dir = s.vars["PWD"]
	if s.dir == "" {
		s.dir, _ = os.Getwd()
	}
	if s.cfg, s.cfgErr = config.Load(e.ConfigPath); s.cfgErr != nil {
		s.cfg = &config.Config{Version: 1, Servers: map[string]*config.Server{}}
	}
	s.cat, s.catErr = catalog.Load(filepath.Join(e.CacheDir, "catalog.json"))

	var out []Finding
	for _, c := range checks {
		if ctx.Err() != nil {
			return nil // cancelled: a partial result would look like a clean bill of health
		}
		out = append(out, c(ctx, s)...)
	}
	rank := map[string]int{Error: 0, Warning: 1, Info: 2}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if rank[a.Severity] != rank[b.Severity] {
			return rank[a.Severity] < rank[b.Severity]
		}
		if a.Check != b.Check {
			return a.Check < b.Check
		}
		return a.Subject < b.Subject
	})
	return out
}

// HasError reports whether any finding is an error.
func HasError(fs []Finding) bool {
	for _, f := range fs {
		if f.Severity == Error {
			return true
		}
	}
	return false
}
