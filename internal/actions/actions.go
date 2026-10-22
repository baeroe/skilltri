// Package actions holds every operation that changes the skilltri config. The CLI and the TUI both
// call these functions: they return results and typed errors and never print.
package actions

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/baeroe/skilltri/internal/adapters"
	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/daemon"
	"github.com/baeroe/skilltri/internal/paths"
)

var (
	// ErrNotFound is wrapped by errors about a server, source, rule or target that does not exist.
	ErrNotFound = errors.New("not found")
	// ErrExists is wrapped by errors about a name that is already taken.
	ErrExists = errors.New("already exists")
	// ErrInvalid is wrapped by errors about a malformed reference or an unknown identifier (a bad
	// plugin server reference, an unknown adapter id), as opposed to a failed validation of values.
	ErrInvalid = errors.New("invalid")
	// ErrChanged is wrapped by errors about a value that changed since the caller read it.
	ErrChanged = errors.New("changed")
)

// kindError carries a human message and the sentinel callers match with errors.Is.
type kindError struct {
	msg   string
	kind  error
	cause error // optional original error, kept in the chain
}

func (e *kindError) Error() string { return e.msg }
func (e *kindError) Unwrap() []error {
	if e.cause != nil {
		return []error{e.kind, e.cause}
	}
	return []error{e.kind}
}

func wrap(kind error, format string, args ...any) error {
	return &kindError{msg: fmt.Sprintf(format, args...), kind: kind}
}

// Env says where the config, the catalog cache and the daemon socket are, and how the catalog is
// built offline. The zero value uses the paths defaults and daemon.DefaultBuilder.
type Env struct {
	ConfigPath  string
	CatalogPath string
	Builder     *catalog.Builder
	Socket      string
	// ControlTimeout bounds each daemon control request an action makes (0: 2 s), so an action
	// never waits long on a daemon that accepts but does not answer.
	ControlTimeout time.Duration
	// Dir is the folder unknown-tool checks ask the daemon about ("": the working directory).
	Dir string
}

func (e Env) configPath() string {
	if e.ConfigPath != "" {
		return e.ConfigPath
	}
	return paths.ConfigFile()
}

func (e Env) catalogPath() string {
	if e.CatalogPath != "" {
		return e.CatalogPath
	}
	return catalog.Path()
}

// control sends one control request to the daemon, bounded by ControlTimeout.
func (e Env) control(h daemon.Hello, reply any) error {
	t := e.ControlTimeout
	if t <= 0 {
		t = 2 * time.Second
	}
	return daemon.ControlTimeout(e.socket(), h, reply, t)
}

// dir is the folder for unknown-tool checks: Dir, else the working directory.
func (e Env) dir() (string, error) {
	if e.Dir != "" {
		return e.Dir, nil
	}
	return os.Getwd()
}

func (e Env) socket() string {
	if e.Socket != "" {
		return e.Socket
	}
	return paths.SocketPath()
}

// configDir is where the "skills" folder for `skill new` lives: next to an explicit config file,
// else the default config dir.
func (e Env) configDir() string {
	if e.ConfigPath != "" {
		return filepath.Dir(e.ConfigPath)
	}
	return paths.ConfigDir()
}

func (e Env) builder() *catalog.Builder {
	if e.Builder != nil {
		return e.Builder
	}
	return daemon.DefaultBuilder()
}

// OfflineCatalog builds the catalog of cfg from the existing checkouts without running git.
func (e Env) OfflineCatalog(cfg *config.Config) *catalog.Catalog {
	c, _ := e.builder().Build(context.Background(), cfg, false)
	return c
}

// edit applies fn to the config under the config lock and then refreshes the catalog. A failing
// refresh is not an error of the edit: the config is saved and the next daemon start rebuilds.
func (e Env) edit(fn func(*config.Config) error) error {
	if err := config.Update(e.configPath(), fn); err != nil {
		return err
	}
	_ = e.RefreshCatalog()
	return nil
}

// RefreshCatalog rebuilds and saves the catalog offline (no git) when no daemon runs; a running
// daemon watches the config and rebuilds on its own. Any failure to connect counts as no daemon;
// a daemon that accepted the connection is left to rebuild, even if it did not answer.
func (e Env) RefreshCatalog() error {
	if err := e.control(daemon.Hello{Cmd: "status"}, nil); !dialFailed(err) {
		return nil
	}
	cfg, err := config.Load(e.configPath())
	if err != nil {
		return nil
	}
	if err := catalog.Save(e.catalogPath(), e.OfflineCatalog(cfg)); err != nil {
		return fmt.Errorf("save catalog: %w", err)
	}
	return nil
}

// dialFailed reports whether err is a failure to connect to the daemon socket at all.
func dialFailed(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

// SetAdapter enables or disables the adapter id in settings.adapters. An unknown id wraps
// ErrInvalid; the last enabled adapter cannot be disabled (an empty list would mean the default
// again).
func SetAdapter(e Env, id string, enabled bool) error {
	if _, ok := adapters.ByID(id); !ok {
		return wrap(ErrInvalid, "unknown adapter %q", id)
	}
	return e.edit(func(c *config.Config) error {
		list := slices.Clone(c.EnabledAdapters())
		switch has := slices.Contains(list, id); {
		case enabled && !has:
			list = append(list, id)
		case !enabled && has:
			list = slices.DeleteFunc(list, func(s string) bool { return s == id })
		}
		if len(list) == 0 {
			return fmt.Errorf("cannot disable %s: at least one adapter must stay enabled", id)
		}
		slices.SortStableFunc(list, func(a, b string) int { return registryIndex(a) - registryIndex(b) })
		c.Settings.Adapters = list
		return nil
	})
}

func registryIndex(id string) int {
	for i, a := range adapters.Registry {
		if a.ID == id {
			return i
		}
	}
	return len(adapters.Registry)
}
