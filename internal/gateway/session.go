package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/downstream"
	"github.com/baeroe/skilltri/internal/rules"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ErrSessionClosed is returned by Refresh after Close.
var ErrSessionClosed = errors.New("gateway session closed")

// protocolVersions are the versions sessions negotiate. 2026-07-28 is left out: it is sessionless
// and forbids server-initiated roots/list, so server/discover is rejected and clients fall back to
// initialize (CONCEPT.md "Spike results").
var protocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// SessionInfo identifies a harness session. ID is also the downstream session ID.
type SessionInfo struct {
	ID         string
	Dir        string
	ClientName string
	// SkillsNative lists the adapter ids whose harnesses got the session's skills natively
	// (SKILLTRI_SKILLS_NATIVE); their clients get no skill fallback.
	SkillsNative []string
}

// Session is one harness connection: an mcp.Server whose tools, prompts and resources are the
// active downstream ones, proxied by name. It is safe for concurrent use.
type Session struct {
	id     string // info.ID, immutable
	cfg    func() *config.Config
	mgr    *downstream.Manager
	skills func(dir string) []catalog.Entry // active skills for a folder; nil: no skill fallback
	server *mcp.Server
	ctx    context.Context // cancelled by Close; aborts a running Refresh
	cancel context.CancelFunc

	refreshMu sync.Mutex // serializes Refresh; the registered maps below are owned by it
	tools     map[string]registered
	prompts   map[string]registered
	resources map[string]registered // by URI
	last      *wanted               // what the last Refresh applied

	mu     sync.Mutex
	info   SessionInfo
	active map[string]bool // servers this session holds a claim on
	closed bool
}

// registered is one item added to the mcp.Server; key detects a changed definition.
type registered struct {
	server string
	key    string
}

// NewSession returns a session for info; nothing is listed until Refresh. info.Dir is normalized.
// skills returns the skills active in a folder (nil: no skill fallback); it is called for the
// instructions and on every skill tool call (with the current folder), so calls see the current
// catalog and config. The instructions list the skills
// active in info.Dir only when info.SkillsNative is empty, since they are fixed before the client
// name is known.
func NewSession(info SessionInfo, cfg func() *config.Config, mgr *downstream.Manager, skills func(dir string) []catalog.Entry) *Session {
	info.Dir = rules.Normalize(info.Dir)
	var names []string
	for _, d := range Servers(cfg(), info.Dir) {
		if d.Decision.Active {
			names = append(names, d.Name)
		}
	}
	list := "none"
	if len(names) > 0 {
		list = strings.Join(names, ", ")
	}
	instructions := fmt.Sprintf("skilltri gateway. Tools and prompts are named <server>_<name>. Servers active for %s: %s.", info.Dir, list)
	if skills != nil && len(info.SkillsNative) == 0 {
		if l := skillInstructions(skills(info.Dir)); l != "" {
			instructions += "\n\n" + l
		}
	}
	s := &Session{
		id: info.ID, cfg: cfg, mgr: mgr, skills: skills, info: info,
		tools: map[string]registered{}, prompts: map[string]registered{}, resources: map[string]registered{},
		active: map[string]bool{}, last: newWanted(),
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.server = mcp.NewServer(&mcp.Implementation{Name: "skilltri", Version: Version}, &mcp.ServerOptions{
		Instructions: instructions,
		HasTools:     true, HasPrompts: true, HasResources: true,
		SupportedProtocolVersions: protocolVersions,
	})
	return s
}

// MCPServer is the server to Connect on the harness transport.
func (s *Session) MCPServer() *mcp.Server { return s.server }

// Info returns the session's identity and current folder.
func (s *Session) Info() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

// UsesServer reports whether server is active for this session.
func (s *Session) UsesServer(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[name]
}

// SetDir moves the session to dir (from the client's roots) and refreshes if it changed.
func (s *Session) SetDir(ctx context.Context, dir string) {
	dir = rules.Normalize(dir)
	s.mu.Lock()
	changed := s.info.Dir != dir
	s.info.Dir = dir
	s.mu.Unlock()
	if !changed {
		return
	}
	if err := s.Refresh(ctx); err != nil {
		slog.Warn("gateway: refresh after folder change", "session", s.id, "err", err)
	}
}

// Close releases every server the session acquired and stops its perSession instances at once.
// The mcp.Server keeps its last lists, but calls fail from now on.
func (s *Session) Close() {
	s.cancel()
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.mgr.ReleaseSession(s.id)
	s.active = map[string]bool{}
}

// wanted is the desired content of the mcp.Server after a Refresh.
type wanted struct {
	tools     map[string]*mcp.Tool
	toolSrc   map[string]route
	local     map[string]mcp.ToolHandler // tools served by the session itself (skill fallback)
	prompts   map[string]*mcp.Prompt
	promptSrc map[string]route
	resources map[string]*mcp.Resource // by URI
	resSrc    map[string]string
}

// route is the downstream server and original name behind an exposed name.
type route struct{ server, name string }

// Refresh recomputes the active servers from the current config and folder, lists them through
// the manager (in parallel) and adds or removes tools, prompts and resources (the SDK sends
// list_changed). Servers that fail to start or list are skipped and logged. When ctx ends first,
// the servers that finished are applied, the unfinished ones keep their current items, and
// Refresh returns ctx's error; after Close it returns ErrSessionClosed.
func (s *Session) Refresh(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(s.ctx, cancel)()

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	cfg := s.cfg()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrSessionClosed
	}
	info := s.info
	id, dir := info.ID, info.Dir
	next := map[string]bool{}
	var names []string
	for _, d := range Servers(cfg, dir) {
		if d.Decision.Active {
			next[d.Name] = true
			names = append(names, d.Name)
		}
	}
	var gone []string
	for name := range s.active {
		if !next[name] {
			gone = append(gone, name)
		}
	}
	s.active = next // before Acquire, so a proxied call racing this Refresh keeps its claim
	s.mu.Unlock()
	for _, name := range gone {
		s.mgr.Release(name, id)
	}

	lists := make([]serverLists, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lists[i] = s.fetch(ctx, name, id)
		}()
	}
	wg.Wait() // every fetch honours ctx, so this returns soon after ctx ends

	if s.ctx.Err() != nil {
		return ErrSessionClosed
	}
	w := newWanted()
	s.addSkillTools(info, w)
	for i, name := range names { // sorted, so the first server by name wins a collision
		l := lists[i]
		switch {
		case l.err == nil:
			s.merge(cfg, name, dir, l, w)
		case ctx.Err() != nil:
			s.last.keep(name, w)
		default:
			slog.Warn("gateway: skipping server", "session", id, "server", name, "err", l.err)
		}
	}
	s.apply(w)
	s.last = w
	return ctx.Err()
}

func newWanted() *wanted {
	return &wanted{
		tools: map[string]*mcp.Tool{}, toolSrc: map[string]route{}, local: map[string]mcp.ToolHandler{},
		prompts: map[string]*mcp.Prompt{}, promptSrc: map[string]route{},
		resources: map[string]*mcp.Resource{}, resSrc: map[string]string{},
	}
}

// keep copies server's items from w (the last applied set) into next.
func (w *wanted) keep(server string, next *wanted) {
	for name, rt := range w.toolSrc {
		if _, dup := next.toolSrc[name]; rt.server == server && !dup {
			next.tools[name], next.toolSrc[name] = w.tools[name], rt
		}
	}
	for name, rt := range w.promptSrc {
		if _, dup := next.promptSrc[name]; rt.server == server && !dup {
			next.prompts[name], next.promptSrc[name] = w.prompts[name], rt
		}
	}
	for uri, srv := range w.resSrc {
		if _, dup := next.resSrc[uri]; srv == server && !dup {
			next.resources[uri], next.resSrc[uri] = w.resources[uri], srv
		}
	}
}

// serverLists is what one server listed during a Refresh.
type serverLists struct {
	tools     []*mcp.Tool
	prompts   []*mcp.Prompt
	resources []*mcp.Resource
	err       error
}

// fetch acquires server and lists its tools, prompts and resources.
func (s *Session) fetch(ctx context.Context, server, id string) serverLists {
	conn, err := s.mgr.Acquire(ctx, server, id)
	if err != nil {
		return serverLists{err: err}
	}
	tools, err := conn.Tools(ctx)
	if err != nil {
		return serverLists{err: err}
	}
	prompts, err := conn.Prompts(ctx)
	if err != nil {
		prompts = nil // servers without prompt support are common
	}
	resources, err := conn.Resources(ctx)
	if err != nil {
		resources = nil
	}
	return serverLists{tools: tools, prompts: prompts, resources: resources}
}

// merge adds the active items of one server's lists to w.
func (s *Session) merge(cfg *config.Config, server, dir string, l serverLists, w *wanted) {
	tools, prompts, resources := l.tools, l.prompts, l.resources
	for _, t := range tools {
		if !ToolDecision(cfg, server, t.Name, dir).Active {
			continue
		}
		name := ExposedName(server, t.Name)
		if prev, dup := w.toolSrc[name]; dup {
			slog.Warn("gateway: tool name collision", "name", name, "kept", prev.server+"/"+prev.name, "dropped", server+"/"+t.Name)
			continue
		}
		cp := *t
		cp.Name = name
		w.tools[name], w.toolSrc[name] = &cp, route{server, t.Name}
	}
	for _, p := range prompts {
		name := ExposedName(server, p.Name)
		if prev, dup := w.promptSrc[name]; dup {
			slog.Warn("gateway: prompt name collision", "name", name, "kept", prev.server+"/"+prev.name, "dropped", server+"/"+p.Name)
			continue
		}
		cp := *p
		cp.Name = name
		w.prompts[name], w.promptSrc[name] = &cp, route{server, p.Name}
	}
	for _, r := range resources {
		if prev, dup := w.resSrc[r.URI]; dup {
			slog.Warn("gateway: resource URI collision", "uri", r.URI, "kept", prev, "dropped", server)
			continue
		}
		w.resources[r.URI], w.resSrc[r.URI] = r, server
	}
}

// apply diffs w against the registered items and updates the mcp.Server. Called with refreshMu held.
func (s *Session) apply(w *wanted) {
	var drop []string
	for name := range s.tools {
		if _, ok := w.tools[name]; !ok {
			drop = append(drop, name)
			delete(s.tools, name)
		}
	}
	if len(drop) > 0 {
		s.server.RemoveTools(drop...)
	}
	for name, t := range w.tools {
		rt := w.toolSrc[name]
		reg := registered{server: rt.server, key: defKey(t)}
		if s.tools[name] == reg {
			continue
		}
		handler := w.local[name]
		if handler == nil {
			handler = s.toolHandler(rt)
		}
		if err := addSafely(func() { s.server.AddTool(t, handler) }); err != nil {
			slog.Warn("gateway: skipping tool", "tool", rt.server+"/"+rt.name, "err", err)
			if _, had := s.tools[name]; had {
				s.server.RemoveTools(name)
				delete(s.tools, name)
			}
			continue
		}
		s.tools[name] = reg
	}

	drop = drop[:0]
	for name := range s.prompts {
		if _, ok := w.prompts[name]; !ok {
			drop = append(drop, name)
			delete(s.prompts, name)
		}
	}
	if len(drop) > 0 {
		s.server.RemovePrompts(drop...)
	}
	for name, p := range w.prompts {
		rt := w.promptSrc[name]
		reg := registered{server: rt.server, key: defKey(p)}
		if s.prompts[name] == reg {
			continue
		}
		s.server.AddPrompt(p, s.promptHandler(rt))
		s.prompts[name] = reg
	}

	drop = drop[:0]
	for uri := range s.resources {
		if _, ok := w.resources[uri]; !ok {
			drop = append(drop, uri)
			delete(s.resources, uri)
		}
	}
	if len(drop) > 0 {
		s.server.RemoveResources(drop...)
	}
	for uri, r := range w.resources {
		server := w.resSrc[uri]
		reg := registered{server: server, key: defKey(r)}
		if s.resources[uri] == reg {
			continue
		}
		if err := addSafely(func() { s.server.AddResource(r, s.resourceHandler(server)) }); err != nil {
			slog.Warn("gateway: skipping resource", "server", server, "uri", uri, "err", err)
			continue
		}
		s.resources[uri] = reg
	}
}

// addSafely turns the SDK's panics on invalid definitions (e.g. a non-object input schema) into errors.
func addSafely(add func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	add()
	return nil
}

// defKey is a comparable fingerprint of a tool, prompt or resource definition.
func defKey(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// conn acquires server for a proxied request. It fails when the server is no longer active for
// the session; a claim taken while a Refresh dropped the server is released again.
func (s *Session) conn(ctx context.Context, server string, opts ...downstream.AcquireOption) (*downstream.Conn, error) {
	if !s.UsesServer(server) {
		return nil, fmt.Errorf("%s: server is not active for this session", server)
	}
	c, err := s.mgr.Acquire(ctx, server, s.id, opts...)
	s.mu.Lock()
	if !s.active[server] {
		s.mgr.Release(server, s.id)
		if err == nil {
			err = fmt.Errorf("%s: server is not active for this session", server)
		}
	}
	s.mu.Unlock()
	return c, err
}

// toolHandler forwards a call with its raw arguments. A tool call is explicit, so it starts a
// failed server even during its start backoff. Failures become error results naming the server,
// so a dead downstream never breaks the harness session.
func (s *Session) toolHandler(rt route) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args json.RawMessage
		if req.Params != nil {
			args = req.Params.Arguments
		}
		c, err := s.conn(ctx, rt.server, downstream.BypassBackoff())
		if err == nil {
			var res *mcp.CallToolResult
			if res, err = c.CallTool(ctx, rt.name, args); err == nil {
				return res, nil
			}
		}
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{
			Text: fmt.Sprintf("skilltri: server %q: %v", rt.server, err),
		}}}, nil
	}
}

func (s *Session) promptHandler(rt route) mcp.PromptHandler {
	return func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		c, err := s.conn(ctx, rt.server)
		if err != nil {
			return nil, err
		}
		p := &mcp.GetPromptParams{Name: rt.name}
		if req.Params != nil {
			p.Arguments = req.Params.Arguments
		}
		return c.GetPrompt(ctx, p)
	}
}

func (s *Session) resourceHandler(server string) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		c, err := s.conn(ctx, server)
		if err != nil {
			return nil, err
		}
		return c.ReadResource(ctx, req.Params.URI)
	}
}
