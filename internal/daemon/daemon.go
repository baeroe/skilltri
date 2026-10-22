// Package daemon is skilltri's background process: it owns the downstream connections and serves
// harness sessions and control requests on a Unix socket. Every connection starts with a one-line
// JSON hello that says which of the two it is; a flock on a lock file guarantees one daemon.
package daemon

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/downstream"
	"github.com/baeroe/skilltri/internal/gateway"
	"github.com/baeroe/skilltri/internal/secrets"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ErrAlreadyRunning is returned by Run when another daemon holds the lock.
var ErrAlreadyRunning = errors.New("skilltri daemon already running")

// Defaults for zero Options and settings.
const (
	defaultPollInterval  = time.Second
	defaultRetryInterval = 30 * time.Second
	defaultIdleTimeout   = 600 * time.Second
	helloTimeout         = 10 * time.Second // a client must send its hello within this time
	rootsTimeout         = 10 * time.Second
)

// Options configure a Daemon.
type Options struct {
	ConfigPath, SocketPath, LockPath string
	Version                          string
	Logger                           *log.Logger   // nil: discard
	PollInterval                     time.Duration // config file check; 0 → 1s
	RetryInterval                    time.Duration // re-Refresh of all sessions so failed servers reappear; 0 → 30s
}

// Daemon serves sessions and control requests. Create it with New and start it with Run.
type Daemon struct {
	o   Options
	log *log.Logger
	cfg atomic.Pointer[config.Config] // never nil once Run started
	mgr *downstream.Manager
	ctx context.Context // Run's context; cancelled on shutdown

	mu        sync.Mutex
	configErr string
	sessions  map[string]*session
	closing   bool
	wg        sync.WaitGroup // connection handlers and their helper goroutines
}

// session is one harness connection.
type session struct {
	gw     *gateway.Session
	ss     *mcp.ServerSession // set after Connect, guarded by Daemon.mu
	client string             // clientInfo.name from initialize, guarded by Daemon.mu
	kick   chan struct{}      // buffered 1: a Refresh is wanted
}

// New returns a daemon for o; nothing happens until Run.
func New(o Options) *Daemon {
	if o.PollInterval <= 0 {
		o.PollInterval = defaultPollInterval
	}
	if o.RetryInterval <= 0 {
		o.RetryInterval = defaultRetryInterval
	}
	l := o.Logger
	if l == nil {
		l = log.New(io.Discard, "", 0)
	}
	return &Daemon{o: o, log: l, sessions: map[string]*session{}}
}

// Run takes the lock, listens on the socket and serves until ctx is cancelled. It returns
// ErrAlreadyRunning if another daemon holds the lock.
func (d *Daemon) Run(ctx context.Context) error {
	for _, p := range []string{d.o.LockPath, d.o.SocketPath} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
	}
	unlock, err := tryLock(d.o.LockPath)
	if err != nil {
		return err
	}
	defer unlock()

	sig, _ := fileSig(d.o.ConfigPath)
	c, err := config.Load(d.o.ConfigPath)
	if err != nil {
		d.log.Printf("config: %v (starting with no servers)", err)
		d.configErr = err.Error()
		c = &config.Config{Version: 1, Servers: map[string]*config.Server{}}
	}
	d.cfg.Store(c)
	idle := time.Duration(c.Settings.IdleTimeoutSec) * time.Second
	if idle <= 0 {
		idle = defaultIdleTimeout
	}
	d.mgr = downstream.NewManager(downstream.Options{
		Lookup: func(name string) (*config.Server, bool) {
			s, ok := d.cfg.Load().Servers[name]
			return s, ok && s != nil
		},
		Expand:        secrets.Expand,
		IdleTimeout:   idle,
		OnListChanged: d.listChanged,
	})
	defer d.mgr.Close()

	// We hold the lock, so a socket file at this path was left by a dead daemon.
	if err := os.Remove(d.o.SocketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ln, err := net.Listen("unix", d.o.SocketPath)
	if err != nil {
		return err
	}
	defer os.Remove(d.o.SocketPath)
	if err := os.Chmod(d.o.SocketPath, 0o600); err != nil {
		ln.Close()
		return err
	}
	d.log.Printf("listening on %s (pid %d)", d.o.SocketPath, os.Getpid())

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.ctx = ctx
	var loops sync.WaitGroup
	loops.Add(3)
	go func() { defer loops.Done(); d.watch(ctx, sig) }()
	go func() { defer loops.Done(); d.retry(ctx) }()
	go func() { defer loops.Done(); d.accept(ln) }()

	<-ctx.Done()
	ln.Close()
	loops.Wait()
	d.mu.Lock()
	d.closing = true
	var open []*session
	for _, s := range d.sessions {
		open = append(open, s)
	}
	d.mu.Unlock()
	for _, s := range open { // outside mu: ServerSession.Close waits for handlers that may take it
		s.gw.Close()
		d.mu.Lock()
		ss := s.ss
		d.mu.Unlock()
		if ss != nil {
			ss.Close()
		}
	}
	d.wg.Wait()
	d.log.Printf("stopped")
	return nil
}

func (d *Daemon) accept(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			d.log.Printf("accept: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		d.mu.Lock()
		if d.closing {
			d.mu.Unlock()
			conn.Close()
			continue
		}
		d.wg.Add(1)
		d.mu.Unlock()
		go func() {
			defer d.wg.Done()
			d.handle(conn)
		}()
	}
}

// handle reads the hello and dispatches the connection.
func (d *Daemon) handle(conn net.Conn) {
	br := bufio.NewReaderSize(conn, maxHello)
	_ = conn.SetReadDeadline(time.Now().Add(helloTimeout))
	h, err := readHello(br)
	_ = conn.SetReadDeadline(time.Time{})
	if h.Kind == "control" || (err != nil && h.Kind != "session") {
		d.control(conn, h, err)
		return
	}
	if err != nil {
		d.log.Printf("session hello: %v", err)
		conn.Close()
		return
	}
	d.serveSession(conn, br, h)
}

// serveSession runs one harness MCP session until the client disconnects or the daemon stops.
func (d *Daemon) serveSession(conn net.Conn, br *bufio.Reader, h Hello) {
	id := newID()
	gw := gateway.NewSession(gateway.SessionInfo{ID: id, Dir: h.Dir}, d.cfg.Load, d.mgr)
	s := &session{gw: gw, kick: make(chan struct{}, 1)}
	gw.MCPServer().AddReceivingMiddleware(d.initMiddleware(s, h.DirExplicit))
	if !d.register(id, s) {
		gw.Close()
		conn.Close()
		return
	}
	defer d.unregister(id)
	defer gw.Close()

	if err := gw.Refresh(d.ctx); err != nil { // before Connect, so the first tools/list is complete
		conn.Close()
		return
	}
	ss, err := gw.MCPServer().Connect(d.ctx, &mcp.IOTransport{Reader: bufferedConn{br, conn}, Writer: conn}, nil)
	if err != nil {
		d.log.Printf("session %s: connect: %v", id, err)
		conn.Close()
		return
	}
	d.mu.Lock()
	s.ss = ss
	closing := d.closing
	d.mu.Unlock()
	if closing {
		ss.Close()
	}
	d.log.Printf("session %s: connected in %s", id, gw.Info().Dir)

	done := make(chan struct{})
	d.wg.Add(1)
	go func() { // serialized refreshes requested by reloads, list changes and retries
		defer d.wg.Done()
		for {
			select {
			case <-s.kick:
				if err := gw.Refresh(d.ctx); err != nil && !errors.Is(err, gateway.ErrSessionClosed) && d.ctx.Err() == nil {
					d.log.Printf("session %s: refresh: %v", id, err)
				}
			case <-done:
				return
			}
		}
	}()
	_ = ss.Wait()
	close(done)
	d.log.Printf("session %s: closed", id)
}

func (d *Daemon) register(id string, s *session) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closing {
		return false
	}
	d.sessions[id] = s
	return true
}

func (d *Daemon) unregister(id string) {
	d.mu.Lock()
	delete(d.sessions, id)
	d.mu.Unlock()
}

// newProtocol is the first protocol version without initialize; it forbids server-initiated
// roots/list (SEP-2322), so sessions on it keep the hello folder.
const newProtocol = "2026-07-28"

// initMiddleware records the client name (from initialize, or from the request _meta on the
// sessionless protocol) and, unless the folder is explicit, follows the client's roots on
// initialized and on roots/list_changed.
func (d *Daemon) initMiddleware(s *session, explicit bool) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			ss, ok := req.GetSession().(*mcp.ServerSession)
			if !ok {
				return res, err
			}
			p := ss.InitializeParams()
			if p == nil {
				return res, err
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			if s.client == "" && p.ClientInfo != nil {
				s.client = p.ClientInfo.Name
			}
			roots := method == "notifications/initialized" || method == "notifications/roots/list_changed"
			if err == nil && roots && !explicit && !d.closing && p.ProtocolVersion < newProtocol &&
				p.Capabilities != nil && p.Capabilities.RootsV2 != nil {
				d.wg.Add(1)
				go func() {
					defer d.wg.Done()
					d.followRoots(s, ss)
				}()
			}
			return res, err
		}
	}
}

// followRoots asks the client for its roots and moves the session to the root if there is exactly
// one file:// root (CONCEPT.md "Which folder is the session in?").
func (d *Daemon) followRoots(s *session, ss *mcp.ServerSession) {
	ctx, cancel := context.WithTimeout(d.ctx, rootsTimeout)
	defer cancel()
	res, err := ss.ListRoots(ctx, nil)
	if err != nil {
		d.log.Printf("session %s: roots/list: %v", s.gw.Info().ID, err)
		return
	}
	if len(res.Roots) != 1 {
		return
	}
	u, err := url.Parse(res.Roots[0].URI)
	if err != nil || u.Scheme != "file" || u.Path == "" {
		return
	}
	s.gw.SetDir(d.ctx, u.Path)
}

// refreshAll asks every session for a Refresh; unchanged sessions send no notifications.
func (d *Daemon) refreshAll() {
	d.refreshIf(func(*session) bool { return true })
}

func (d *Daemon) refreshIf(want func(*session) bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.sessions {
		if want(s) {
			select {
			case s.kick <- struct{}{}:
			default: // a Refresh is already pending
			}
		}
	}
}

// listChanged refreshes the sessions that use server after its lists changed.
func (d *Daemon) listChanged(server string) {
	d.refreshIf(func(s *session) bool { return s.gw.UsesServer(server) })
}

// retry refreshes all sessions periodically, so a server that failed during a Refresh reappears.
func (d *Daemon) retry(ctx context.Context) {
	t := time.NewTicker(d.o.RetryInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.refreshAll()
		}
	}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
