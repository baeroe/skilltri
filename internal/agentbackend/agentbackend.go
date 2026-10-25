// Package agentbackend connects skilltri's agent tools (package agenttools) to the running daemon:
// it implements agenttools.Backend on a *daemon.Daemon and turns the tools into gateway local
// tools, offered per session according to settings.agentAccess at every Refresh. It lives apart
// from the daemon because agenttools uses actions and doctor, which import the daemon.
package agentbackend

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/baeroe/skilltri/internal/actions"
	"github.com/baeroe/skilltri/internal/agenttools"
	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/daemon"
	"github.com/baeroe/skilltri/internal/doctor"
	"github.com/baeroe/skilltri/internal/gateway"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Options say where the daemon's files are, for the doctor and the actions.
type Options struct {
	ConfigPath, Socket string
	Home               string   // the user's home, for the doctor's harness checks
	CacheDir           string   // holds catalog.json and the views
	StatePath          string   // the skill lock
	Environ            []string // the daemon's environment, for the doctor
}

// Tools is the daemon.Options.Tools hook: the agent tools on d.
func Tools(o Options) func(*daemon.Daemon) gateway.ToolProvider {
	return func(d *daemon.Daemon) gateway.ToolProvider { return Provider(New(d, o)) }
}

// Provider offers the agent tools that cfg's agentAccess allows; each call runs against b with
// the session's client name and current folder.
func Provider(b agenttools.Backend) gateway.ToolProvider {
	return func(cfg *config.Config, _ gateway.SessionInfo) []gateway.LocalTool {
		tools := agenttools.Tools(cfg.AgentAccess())
		out := make([]gateway.LocalTool, 0, len(tools))
		for _, t := range tools {
			out = append(out, gateway.LocalTool{Tool: t.Def, Handle: func(ctx context.Context, info gateway.SessionInfo, args json.RawMessage) (*mcp.CallToolResult, error) {
				return t.Handle(ctx, b, agenttools.Call{Client: info.ClientName, Dir: info.Dir}, args)
			}})
		}
		return out
	}
}

// New returns the backend of d.
func New(d *daemon.Daemon, o Options) agenttools.Backend { return &backend{d: d, o: o} }

type backend struct {
	d *daemon.Daemon
	o Options
}

func (b *backend) Config() *config.Config    { return b.d.Config() }
func (b *backend) Catalog() *catalog.Catalog { return b.d.Catalog() }

func (b *backend) Status() agenttools.Status {
	st := b.d.Status()
	out := agenttools.Status{PID: st.PID, Version: st.Version, ConfigError: st.ConfigError, Sessions: len(st.Sessions),
		Servers: make([]agenttools.ServerStatus, 0, len(st.Servers))}
	for _, s := range st.Servers {
		out.Servers = append(out.Servers, agenttools.ServerStatus{Name: s.Name, State: s.State, Error: s.Error,
			Tools: s.Tools, Sessions: s.Sessions, LastCall: s.LastCall})
	}
	return out
}

func (b *backend) Which(dir string) (agenttools.Which, error) {
	w, err := b.d.Which(dir)
	if err != nil {
		return agenttools.Which{}, err
	}
	return ConvertWhich(w), nil
}

// ConvertWhich is the agent view of a which reply.
func ConvertWhich(w daemon.WhichReply) agenttools.Which {
	out := agenttools.Which{Dir: w.Dir, Servers: []agenttools.WhichServer{}, Skills: []agenttools.WhichSkill{}}
	for _, s := range w.Servers {
		ws := agenttools.WhichServer{Name: s.Name, Active: s.Decision.Active, Reason: s.Decision.Reason, State: s.State}
		for _, t := range s.Tools {
			ws.Tools = append(ws.Tools, agenttools.WhichTool{Name: t.Name, Exposed: t.Exposed, Active: t.Decision.Active, Reason: t.Decision.Reason})
		}
		out.Servers = append(out.Servers, ws)
	}
	for _, s := range w.Skills {
		out.Skills = append(out.Skills, agenttools.WhichSkill{ID: s.ID, Source: s.Source, Plugin: s.Plugin, Active: s.Decision.Active, Reason: s.Decision.Reason})
	}
	return out
}

// Doctor runs the checks with PWD set to dir, so the project folder checks look at the session's folder.
func (b *backend) Doctor(ctx context.Context, dir string) []doctor.Finding {
	environ := append(append([]string(nil), b.o.Environ...), "PWD="+dir) // later entries win
	return doctor.Run(ctx, doctor.Env{Home: b.o.Home, ConfigPath: b.o.ConfigPath, Socket: b.o.Socket,
		CacheDir: b.o.CacheDir, StatePath: b.o.StatePath, Environ: environ})
}

func (b *backend) Update(ctx context.Context, sources []string) ([]catalog.SourceState, error) {
	r, err := b.d.UpdateSources(ctx, sources)
	return r.Sources, err
}

func (b *backend) Env() actions.Env {
	e := actions.Env{ConfigPath: b.o.ConfigPath, Socket: b.o.Socket}
	if b.o.CacheDir != "" {
		e.CatalogPath = filepath.Join(b.o.CacheDir, "catalog.json")
	}
	return e
}
