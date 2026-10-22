package downstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// defaultRequestTimeout applies when a server sets no requestTimeoutMs.
const defaultRequestTimeout = 60 * time.Second

// Conn is one live client session to a downstream server. Lists are cached until the server
// sends list_changed. Once the session ends (crash, idle stop, restart) every method returns an error;
// callers Acquire again to get a fresh Conn.
type Conn struct {
	server   string
	cs       *mcp.ClientSession
	stderr   *ring // nil for http
	timeout  time.Duration
	lastCall *atomic.Int64   // unix nanos, shared with the server's entry
	dead     chan struct{}   // closed when the session ended
	ctx      context.Context // cancelled by close, which aborts in-flight calls
	cancel   context.CancelFunc

	mu        sync.Mutex
	gen       uint64 // bumped on every invalidation, so a fetch racing a list_changed is not cached
	tools     []*mcp.Tool
	prompts   []*mcp.Prompt
	resources []*mcp.Resource
}

// dial starts or connects to the server described by cfg. onChanged runs after a list_changed
// notification invalidated the conn's caches.
func dial(ctx context.Context, name string, cfg *config.Server, expand func(string) (string, error), lastCall *atomic.Int64, onChanged func()) (*Conn, error) {
	c := &Conn{server: name, timeout: defaultRequestTimeout, lastCall: lastCall, dead: make(chan struct{})}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	if cfg.RequestTimeoutMs > 0 {
		c.timeout = time.Duration(cfg.RequestTimeoutMs) * time.Millisecond
	}
	invalidate := func() {
		c.mu.Lock()
		c.gen++
		c.tools, c.prompts, c.resources = nil, nil, nil
		c.mu.Unlock()
		onChanged()
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "skilltri", Version: "0.1.0"}, &mcp.ClientOptions{
		Capabilities:               &mcp.ClientCapabilities{},
		ToolListChangedHandler:     func(context.Context, *mcp.ToolListChangedRequest) { invalidate() },
		PromptListChangedHandler:   func(context.Context, *mcp.PromptListChangedRequest) { invalidate() },
		ResourceListChangedHandler: func(context.Context, *mcp.ResourceListChangedRequest) { invalidate() },
	})

	var t mcp.Transport
	switch cfg.Type {
	case "stdio":
		cmd := exec.Command(cfg.Command, cfg.Args...)
		cmd.Dir = cfg.Cwd
		cmd.Env = os.Environ()
		for k, v := range cfg.Env {
			ev, err := expand(v)
			if err != nil {
				return nil, fmt.Errorf("env %s: %w", k, err)
			}
			cmd.Env = append(cmd.Env, k+"="+ev)
		}
		c.stderr = &ring{}
		cmd.Stderr = c.stderr
		t = &mcp.CommandTransport{Command: cmd}
	case "http":
		hdr := http.Header{}
		for k, v := range cfg.Headers {
			ev, err := expand(v)
			if err != nil {
				return nil, fmt.Errorf("header %s: %w", k, err)
			}
			hdr.Set(k, ev)
		}
		t = &mcp.StreamableClientTransport{
			Endpoint:   cfg.URL,
			HTTPClient: &http.Client{Transport: headerTransport{base: http.DefaultTransport, header: hdr}},
		}
	default:
		return nil, fmt.Errorf("unknown server type %q", cfg.Type)
	}

	cs, err := client.Connect(ctx, t, nil)
	if err != nil {
		return c, err // c carries the stderr tail
	}
	c.cs = cs
	return c, nil
}

// headerTransport adds fixed headers (already secret-expanded) to every request.
type headerTransport struct {
	base   http.RoundTripper
	header http.Header
}

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h.header {
		r.Header[k] = v
	}
	return h.base.RoundTrip(r)
}

// stderrTail returns the last stderr lines of a stdio server.
func (c *Conn) stderrTail() []string {
	if c == nil || c.stderr == nil {
		return nil
	}
	return c.stderr.Lines()
}

// toolCount is the number of cached tools (0 if not listed yet).
func (c *Conn) toolCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.tools)
}

func (c *Conn) isDead() bool {
	select {
	case <-c.dead:
		return true
	default:
		return false
	}
}

// close cancels in-flight calls (the SDK only shuts the transport down once no call is pending),
// then ends the session (for stdio: closes stdin, then terminates the process) and waits.
func (c *Conn) close() {
	c.cancel()
	_ = c.cs.Close()
}

// do runs fn with the request timeout and turns failures into errors that name the server.
func (c *Conn) do(ctx context.Context, what string, fn func(ctx context.Context) error) error {
	if c.isDead() || c.ctx.Err() != nil {
		return fmt.Errorf("%s: %s: connection closed", c.server, what)
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	defer context.AfterFunc(c.ctx, cancel)()
	err := fn(ctx)
	switch {
	case err == nil:
		return nil
	case c.ctx.Err() != nil:
		return fmt.Errorf("%s: %s: connection closed", c.server, what)
	case errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%s: %s: request timeout after %v", c.server, what, c.timeout)
	default:
		return fmt.Errorf("%s: %s: %w", c.server, what, err)
	}
}

// cachedList returns *slot if cached, else fetches the list it and stores it unless a list_changed arrived meanwhile.
func cachedList[T any](c *Conn, ctx context.Context, what string, slot *[]*T, list func(ctx context.Context) ([]*T, error)) ([]*T, error) {
	c.mu.Lock()
	if *slot != nil {
		out := *slot
		c.mu.Unlock()
		return out, nil
	}
	gen := c.gen
	c.mu.Unlock()
	var items []*T
	err := c.do(ctx, what, func(ctx context.Context) error {
		var err error
		items, err = list(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []*T{}
	}
	c.mu.Lock()
	if c.gen == gen {
		*slot = items
	}
	c.mu.Unlock()
	return items, nil
}

// collect drains a paginating SDK iterator.
func collect[T any](seq func(yield func(*T, error) bool)) ([]*T, error) {
	var out []*T
	for item, err := range seq {
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

// Tools lists the server's tools (all pages), cached until list_changed.
func (c *Conn) Tools(ctx context.Context) ([]*mcp.Tool, error) {
	return cachedList(c, ctx, "tools/list", &c.tools, func(ctx context.Context) ([]*mcp.Tool, error) {
		return collect(c.cs.Tools(ctx, nil))
	})
}

// Prompts lists the server's prompts (all pages), cached until list_changed.
func (c *Conn) Prompts(ctx context.Context) ([]*mcp.Prompt, error) {
	return cachedList(c, ctx, "prompts/list", &c.prompts, func(ctx context.Context) ([]*mcp.Prompt, error) {
		return collect(c.cs.Prompts(ctx, nil))
	})
}

// Resources lists the server's resources (all pages), cached until list_changed.
func (c *Conn) Resources(ctx context.Context) ([]*mcp.Resource, error) {
	return cachedList(c, ctx, "resources/list", &c.resources, func(ctx context.Context) ([]*mcp.Resource, error) {
		return collect(c.cs.Resources(ctx, nil))
	})
}

// CallTool calls a tool with raw JSON arguments, bounded by the server's request timeout.
func (c *Conn) CallTool(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	c.lastCall.Store(time.Now().UnixNano())
	var res *mcp.CallToolResult
	err := c.do(ctx, "tools/call "+name, func(ctx context.Context) error {
		var err error
		p := &mcp.CallToolParams{Name: name}
		if len(args) > 0 {
			p.Arguments = args
		}
		res, err = c.cs.CallTool(ctx, p)
		return err
	})
	return res, err
}

// GetPrompt fetches a prompt.
func (c *Conn) GetPrompt(ctx context.Context, p *mcp.GetPromptParams) (*mcp.GetPromptResult, error) {
	var res *mcp.GetPromptResult
	err := c.do(ctx, "prompts/get "+p.Name, func(ctx context.Context) error {
		var err error
		res, err = c.cs.GetPrompt(ctx, p)
		return err
	})
	return res, err
}

// ReadResource reads a resource by URI.
func (c *Conn) ReadResource(ctx context.Context, uri string) (*mcp.ReadResourceResult, error) {
	var res *mcp.ReadResourceResult
	err := c.do(ctx, "resources/read "+uri, func(ctx context.Context) error {
		var err error
		res, err = c.cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
		return err
	})
	return res, err
}
