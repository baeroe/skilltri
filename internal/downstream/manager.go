// Package downstream manages skilltri's client connections to the real ("downstream") MCP servers.
// Servers start on the first Acquire and stop after an idle timeout; one process is shared by all
// sessions unless the server is perSession. A crashed server is replaced by the next Acquire.
package downstream

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/baeroe/skilltri/internal/config"
)

// startTimeout bounds process start plus the MCP handshake.
const startTimeout = 60 * time.Second

// ErrClosed is returned by Acquire after Close.
var ErrClosed = errors.New("downstream manager closed")

// Options configure a Manager.
type Options struct {
	Lookup        func(name string) (*config.Server, bool) // current config
	Expand        func(string) (string, error)             // secrets.Expand
	IdleTimeout   time.Duration                            // 0: never stop idle servers
	OnListChanged func(server string)                      // downstream tools/prompts/resources changed
}

// ServerStatus is one server's state for `skilltri status` and the TUI.
type ServerStatus struct {
	Name       string
	State      string // "stopped" | "starting" | "ready" | "error"
	Error      string
	StderrTail []string
	Tools      int
	Sessions   int
	LastCall   time.Time
}

// Manager owns all downstream connections. It is safe for concurrent use.
type Manager struct {
	o      Options
	ctx    context.Context // cancelled by Close; parent of every start
	cancel context.CancelFunc

	mu      sync.Mutex
	closed  bool
	servers map[string]*entry
	wg      sync.WaitGroup // start, watch and stop goroutines; Add only under mu while !closed
}

// entry is the state of one server name.
type entry struct {
	name     string
	users    map[string]struct{} // sessions that acquired the server and did not release it
	insts    map[string]*inst    // "" for the shared instance, else the session ID (perSession)
	err      string              // last start failure or crash, cleared by a successful start
	tail     []string            // stderr tail captured with err
	lastCall atomic.Int64
}

// inst is one started (or starting) connection. done is closed when the start finished;
// conn and err are written before that and read only after.
type inst struct {
	key  string // config ConnKey the instance was started with
	done chan struct{}
	conn *Conn
	err  error
	idle *time.Timer
}

func (in *inst) started() bool {
	select {
	case <-in.done:
		return true
	default:
		return false
	}
}

// NewManager returns a Manager; nothing starts until Acquire.
func NewManager(o Options) *Manager {
	if o.Expand == nil {
		o.Expand = func(s string) (string, error) { return s, nil }
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{o: o, ctx: ctx, cancel: cancel, servers: map[string]*entry{}}
}

// Acquire returns a ready connection to server for sessionID, starting it on demand. Shared servers
// return the same Conn to every session; perSession servers one Conn per session. Concurrent Acquires
// start a single process. A dead conn or one started with an outdated config is replaced.
func (m *Manager) Acquire(ctx context.Context, server, sessionID string) (*Conn, error) {
	cfg, ok := m.o.Lookup(server)
	if !ok {
		return nil, fmt.Errorf("unknown server %q", server)
	}
	if cfg.Disabled {
		return nil, fmt.Errorf("server %q is disabled", server)
	}
	key := cfg.ConnKey()
	ikey := ""
	if cfg.PerSession {
		ikey = sessionID
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	e := m.servers[server]
	if e == nil {
		e = &entry{name: server, users: map[string]struct{}{}, insts: map[string]*inst{}}
		m.servers[server] = e
	}
	e.users[sessionID] = struct{}{}
	var stale *Conn
	in := e.insts[ikey]
	if in != nil && (in.key != key || (in.started() && in.conn != nil && in.conn.isDead())) {
		delete(e.insts, ikey) // a still-starting stale instance is closed by its start goroutine
		if in.started() {
			stale = in.conn
		}
		in = nil
	}
	if in == nil {
		in = &inst{key: key, done: make(chan struct{})}
		e.insts[ikey] = in
		m.wg.Add(1)
		go m.start(e, ikey, in, cfg)
	} else if in.idle != nil {
		in.idle.Stop()
		in.idle = nil
	}
	m.mu.Unlock()
	if stale != nil {
		stale.close()
	}

	select {
	case <-in.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if in.err != nil {
		return nil, in.err
	}
	return in.conn, nil
}

// start dials the instance and publishes the result; it runs in its own goroutine.
func (m *Manager) start(e *entry, ikey string, in *inst, cfg *config.Server) {
	defer m.wg.Done()
	defer close(in.done)
	ctx, cancel := context.WithTimeout(m.ctx, startTimeout)
	conn, err := dial(ctx, e.name, cfg, m.o.Expand, &e.lastCall, func() { m.listChanged(e.name) })
	cancel()

	m.mu.Lock()
	current := !m.closed && e.insts[ikey] == in
	switch {
	case err != nil:
		in.err = fmt.Errorf("start %s: %w", e.name, err)
		if current {
			delete(e.insts, ikey)
			e.err, e.tail = in.err.Error(), conn.stderrTail()
		}
	case !current:
		in.err = fmt.Errorf("%s: connection replaced while starting", e.name)
	default:
		in.conn = conn
		e.err, e.tail = "", nil
		m.wg.Add(1)
		go m.watch(e, ikey, in)
	}
	m.mu.Unlock()
	if err == nil && !current {
		conn.close()
	}
}

// watch waits for the session to end. An end the manager did not cause (the instance is still
// registered) is a crash and is recorded as the server's error.
func (m *Manager) watch(e *entry, ikey string, in *inst) {
	defer m.wg.Done()
	err := in.conn.cs.Wait()
	close(in.conn.dead)
	m.mu.Lock()
	if e.insts[ikey] == in {
		delete(e.insts, ikey)
		msg := "server exited"
		if err != nil {
			msg += ": " + err.Error()
		}
		e.err, e.tail = fmt.Sprintf("%s: %s", e.name, msg), in.conn.stderrTail()
	}
	m.mu.Unlock()
	in.conn.close() // reap the process
}

// listChanged forwards a downstream list_changed to Options.OnListChanged without blocking the
// SDK's read loop (the callback may call back into the Conn).
func (m *Manager) listChanged(server string) {
	if m.o.OnListChanged == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.o.OnListChanged(server)
	}()
}

// Release drops sessionID's use of server. Instances nobody uses stop after the idle timeout.
func (m *Manager) Release(server, sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.servers[server]
	if e == nil {
		return
	}
	delete(e.users, sessionID)
	if in := e.insts[sessionID]; in != nil && sessionID != "" {
		m.scheduleIdle(e, sessionID, in)
	}
	if in := e.insts[""]; in != nil && len(e.users) == 0 {
		m.scheduleIdle(e, "", in)
	}
}

// scheduleIdle arms the idle timer of an unused instance. Called with mu held.
func (m *Manager) scheduleIdle(e *entry, ikey string, in *inst) {
	if m.o.IdleTimeout <= 0 || in.idle != nil {
		return
	}
	in.idle = time.AfterFunc(m.o.IdleTimeout, func() {
		m.mu.Lock()
		_, used := e.users[ikey]
		if m.closed || e.insts[ikey] != in || (ikey == "" && len(e.users) > 0) || (ikey != "" && used) {
			m.mu.Unlock()
			return
		}
		delete(e.insts, ikey)
		m.wg.Add(1)
		m.mu.Unlock()
		defer m.wg.Done()
		stopInst(in)
	})
}

// stopInst closes an instance removed from its entry, waiting for a pending start first.
func stopInst(in *inst) {
	<-in.done
	if in.conn != nil {
		in.conn.close()
	}
}

// Restart stops every instance of server (for example after its ConnKey changed). Sessions keep
// their claim; their old Conn returns errors and the next Acquire starts a fresh process.
func (m *Manager) Restart(server string) {
	m.mu.Lock()
	e := m.servers[server]
	if e == nil {
		m.mu.Unlock()
		return
	}
	var stop []*inst
	for k, in := range e.insts {
		if in.idle != nil {
			in.idle.Stop()
		}
		if in.started() {
			stop = append(stop, in) // starting ones see they were replaced and close themselves
		}
		delete(e.insts, k)
	}
	e.err, e.tail = "", nil
	m.mu.Unlock()
	for _, in := range stop {
		stopInst(in)
	}
}

// Status reports every server the manager has seen, sorted by name.
func (m *Manager) Status() []ServerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ServerStatus, 0, len(m.servers))
	for _, e := range m.servers {
		s := ServerStatus{Name: e.name, State: "stopped", Sessions: len(e.users)}
		if n := e.lastCall.Load(); n != 0 {
			s.LastCall = time.Unix(0, n)
		}
		starting, ready := false, false
		for _, in := range e.insts {
			if !in.started() {
				starting = true
				continue
			}
			if in.conn != nil {
				ready = true
				s.Tools = max(s.Tools, in.conn.toolCount())
				if s.StderrTail == nil {
					s.StderrTail = in.conn.stderrTail()
				}
			}
		}
		switch {
		case ready:
			s.State = "ready"
		case starting:
			s.State = "starting"
		case e.err != "":
			s.State, s.Error, s.StderrTail = "error", e.err, slices.Clone(e.tail)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Close stops every server and waits for all goroutines. Acquire fails afterwards.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.cancel()
	var stop []*inst
	for _, e := range m.servers {
		for k, in := range e.insts {
			if in.idle != nil {
				in.idle.Stop()
			}
			stop = append(stop, in)
			delete(e.insts, k)
		}
	}
	m.mu.Unlock()
	for _, in := range stop {
		stopInst(in)
	}
	m.wg.Wait()
}
