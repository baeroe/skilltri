package tui

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/daemon"
	"github.com/baeroe/skilltri/internal/paths"
)

// controlTimeout bounds every daemon control call of the TUI.
const controlTimeout = 2 * time.Second

// tickInterval is how often the daemon status (and the config/catalog mtimes) are checked.
const tickInterval = 2 * time.Second

// Snapshot is everything the tabs render, loaded off the UI goroutine. Config is nil only when
// ConfigErr is set; Catalog is never nil after a load (the cached catalog.json, else an offline
// build from the existing checkouts, else empty).
type Snapshot struct {
	Config    *config.Config
	ConfigErr error
	Catalog   *catalog.Catalog
	Status    *daemon.StatusReply
	DaemonErr error             // nil Status = not running (or not answering, see DaemonErr)
	Review    map[string]string // catalog.ReviewState of plugin servers
	Loaded    time.Time
}

// fileStamps identify the config and catalog file versions (mtime and size; "" = missing).
type fileStamps struct{ config, catalog string }

// snapshotMsg carries a full snapshot; seq orders it against status refreshes.
type snapshotMsg struct {
	seq    int
	snap   Snapshot
	stamps fileStamps
}

// statusMsg carries a status-only refresh plus the current file stamps.
type statusMsg struct {
	seq    int
	status *daemon.StatusReply
	err    error
	stamps fileStamps
}

// tickMsg drives the periodic refresh.
type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (o Options) configPath() string {
	if o.Env.ConfigPath != "" {
		return o.Env.ConfigPath
	}
	return paths.ConfigFile()
}

func (o Options) catalogPath() string {
	if o.Env.CatalogPath != "" {
		return o.Env.CatalogPath
	}
	return catalog.Path()
}

func (o Options) socket() string {
	if o.Env.Socket != "" {
		return o.Env.Socket
	}
	return paths.SocketPath()
}

func stamp(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d/%d", fi.ModTime().UnixNano(), fi.Size())
}

func (o Options) stamps() fileStamps {
	return fileStamps{config: stamp(o.configPath()), catalog: stamp(o.catalogPath())}
}

// queryStatus asks the daemon for its status; nil with the error when it does not answer.
func queryStatus(o Options) (*daemon.StatusReply, error) {
	var r daemon.StatusReply
	if err := o.Control(daemon.Hello{Cmd: "status"}, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// loadSnapshot loads config, catalog and daemon status in a command (never in Update).
func loadSnapshot(o Options) tea.Cmd {
	return func() tea.Msg {
		st := o.stamps() // before reading: a change while loading triggers another reload
		s := Snapshot{}
		s.Config, s.ConfigErr = config.Load(o.configPath())
		if s.ConfigErr != nil {
			s.Config = nil
		}
		if c, err := catalog.Load(o.catalogPath()); err == nil {
			s.Catalog = c
		} else if s.Config != nil {
			s.Catalog = o.Env.OfflineCatalog(s.Config)
		}
		if s.Catalog == nil {
			s.Catalog = &catalog.Catalog{}
		}
		if s.Config != nil {
			s.Review = catalog.ReviewState(s.Config, s.Catalog)
		}
		s.Status, s.DaemonErr = queryStatus(o)
		s.Loaded = o.Now()
		return snapshotMsg{snap: s, stamps: st}
	}
}

// loadStatus refreshes only the daemon status and reads the file stamps.
func loadStatus(o Options) tea.Cmd {
	return func() tea.Msg {
		st := o.stamps()
		r, err := queryStatus(o)
		return statusMsg{status: r, err: err, stamps: st}
	}
}

// daemonStopped reports whether err means no daemon listens (as opposed to one not answering).
func daemonStopped(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

// warnings are the doctor-relevant problems visible in the snapshot: a config error, failing
// sources and plugin servers whose definition changed.
func (s Snapshot) warnings() []string {
	var out []string
	switch {
	case s.ConfigErr != nil:
		out = append(out, "config: "+s.ConfigErr.Error())
	case s.Status != nil && s.Status.ConfigError != "":
		out = append(out, "config: "+s.Status.ConfigError)
	}
	if s.Catalog != nil {
		for _, st := range s.Catalog.Sources {
			if st.Error != "" {
				out = append(out, "source "+st.Name+": "+st.Error)
			}
		}
	}
	var review []string
	for name, st := range s.Review {
		if st == "needs-review" {
			review = append(review, name)
		}
	}
	sort.Strings(review)
	for _, name := range review {
		out = append(out, "server "+name+": plugin definition changed, needs review")
	}
	return out
}

// serverNames are the configured server names, sorted.
func (s Snapshot) serverNames() []string {
	if s.Config == nil {
		return nil
	}
	names := make([]string, 0, len(s.Config.Servers))
	for n := range s.Config.Servers {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// serverState is the daemon's state of a server ("" when the daemon does not run).
func (s Snapshot) serverState(name string) string {
	if s.Status == nil {
		return ""
	}
	for _, st := range s.Status.Servers {
		if st.Name == name {
			return st.State
		}
	}
	return ""
}
