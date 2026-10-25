package daemon

import (
	"context"

	"github.com/baeroe/skilltri/internal/config"
)

// The exported methods below are the daemon's in-process view for the agent tools (package
// agenttools, adapted in package agentbackend); they answer like the control commands of the same
// name without a socket round trip. They are valid while Run serves.

// Config is the config the daemon currently serves (an empty one while none was ever loaded).
func (d *Daemon) Config() *config.Config {
	if c := d.cfg.Load(); c != nil {
		return c
	}
	return &config.Config{Version: 1, Servers: map[string]*config.Server{}}
}

// Status answers like the "status" control command.
func (d *Daemon) Status() StatusReply { return d.status() }

// Which answers like the "which" control command for dir; an empty dir is an error.
func (d *Daemon) Which(dir string) (WhichReply, error) { return d.which(Hello{Args: []string{dir}}) }

// UpdateSources answers like the "update" control command: it syncs the named sources (every
// enabled one when names is empty) and rebuilds the catalog; ctx (and shutdown) aborts it.
func (d *Daemon) UpdateSources(ctx context.Context, names []string) (UpdateReply, error) {
	return d.update(ctx, names)
}
