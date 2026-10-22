package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"time"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/downstream"
	"github.com/baeroe/skilltri/internal/gateway"
	"github.com/baeroe/skilltri/internal/rules"
)

// controlTimeout bounds one control request, client and daemon side; "update" gets updateTimeout.
const controlTimeout = 30 * time.Second

func timeoutFor(cmd string) time.Duration {
	if cmd == "update" {
		return updateTimeout
	}
	return controlTimeout
}

// StatusReply answers the "status" control command.
type StatusReply struct {
	PID         int
	Version     string
	Path        string // the daemon's PATH, which stdio servers inherit
	ConfigError string
	Servers     []downstream.ServerStatus // every configured server; never started ones are "stopped"
	Sessions    []gateway.SessionInfo
}

// WhichReply answers the "which" control command: what a session in Dir would get.
type WhichReply struct {
	Dir     string
	Servers []WhichServer
	Skills  []WhichSkill
}

// WhichSkill is one catalog skill's decision for the folder. Plugin is the marketplace entry name.
type WhichSkill struct {
	ID, Source, Plugin string
	Decision           rules.Decision
}

// WhichSkills decides every skill of c for dir (already normalized), sorted by ID.
func WhichSkills(c *catalog.Catalog, cfg *config.Config, dir string) []WhichSkill {
	var out []WhichSkill
	for _, d := range catalog.Evaluate(c, cfg, dir) {
		out = append(out, WhichSkill{ID: d.Entry.ID, Source: d.Entry.Source, Plugin: d.Entry.Plugin, Decision: d.Decision})
	}
	return out
}

// WhichOffline decides every server and skill for dir (already normalized) from the config and
// catalog alone, as `skilltri which` does without a daemon: servers have the state "unknown" and
// no tools. cat may be nil (no skills).
func WhichOffline(cfg *config.Config, cat *catalog.Catalog, dir string) WhichReply {
	reply := WhichReply{Dir: dir}
	for _, sd := range gateway.Servers(cfg, dir) {
		reply.Servers = append(reply.Servers, WhichServer{Name: sd.Name, Decision: sd.Decision, State: "unknown"})
	}
	if cat != nil {
		reply.Skills = WhichSkills(cat, cfg, dir)
	}
	return reply
}

// WhichServer is one configured server's decision for the folder.
type WhichServer struct {
	Name     string
	Decision rules.Decision
	State    string
	Tools    []WhichTool // Tools only for servers that are ready
}

// WhichTool is one tool of a ready server.
type WhichTool struct {
	Name     string
	Exposed  string
	Decision rules.Decision
}

// controlResponse is the single JSON line the daemon writes back to a control request.
type controlResponse struct {
	Error  string          `json:"error,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

// Control sends a control hello to the daemon at socket and decodes the result into reply.
// V and Kind default to 1 and "control".
func Control(socket string, h Hello, reply any) error {
	return control(socket, h, reply, 2*time.Second, timeoutFor(h.Cmd))
}

// ControlTimeout is Control with timeout bounding both the dial and the whole request.
func ControlTimeout(socket string, h Hello, reply any, timeout time.Duration) error {
	return control(socket, h, reply, timeout, timeout)
}

// ControlContext is Control (with its timeouts) that gives up when ctx is done: the connection is
// closed and ctx.Err() returned. The daemon is not told; it finishes the request on its own.
func ControlContext(ctx context.Context, socket string, h Hello, reply any) error {
	dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(dctx, "unix", socket)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	err = exchange(conn, h, reply, timeoutFor(h.Cmd))
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func control(socket string, h Hello, reply any, dial, total time.Duration) error {
	conn, err := net.DialTimeout("unix", socket, dial)
	if err != nil {
		return err
	}
	defer conn.Close()
	return exchange(conn, h, reply, total)
}

// exchange sends h on conn and decodes the reply, all within total.
func exchange(conn net.Conn, h Hello, reply any, total time.Duration) error {
	if h.V == 0 {
		h.V = helloVersion
	}
	if h.Kind == "" {
		h.Kind = "control"
	}
	_ = conn.SetDeadline(time.Now().Add(total))
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return err
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("read control reply: %w", err)
	}
	var resp controlResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return fmt.Errorf("parse control reply: %w", err)
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	if reply == nil {
		return nil
	}
	return json.Unmarshal(resp.Result, reply)
}

// control answers one control request (or reports a bad hello) and closes the connection.
func (d *Daemon) control(conn net.Conn, h Hello, herr error) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeoutFor(h.Cmd)))
	var (
		result any
		err    = herr
	)
	if err == nil {
		switch h.Cmd {
		case "status":
			result = d.status()
		case "which":
			result, err = d.which(h)
		case "update":
			result, err = d.update(h.Args)
		case "restart":
			err = d.restart(h.Args)
		case "shutdown":
			defer d.cancel() // after the reply is written; Run then stops like on SIGTERM
			d.log.Printf("shutdown requested")
		default:
			err = fmt.Errorf("unknown control command %q", h.Cmd)
		}
	}
	var resp controlResponse
	if err != nil {
		resp.Error = err.Error()
	} else if resp.Result, err = json.Marshal(result); err != nil {
		resp = controlResponse{Error: err.Error()}
	}
	b, _ := json.Marshal(resp)
	if _, err := conn.Write(append(b, '\n')); err != nil {
		d.log.Printf("control %s: %v", h.Cmd, err)
	}
}

// restart stops every instance of the configured server Args[0]; the next session (or which)
// starts a fresh process.
func (d *Daemon) restart(args []string) error {
	if len(args) != 1 || args[0] == "" {
		return errors.New("restart needs a server name")
	}
	if d.cfg.Load().Servers[args[0]] == nil {
		return fmt.Errorf("no such server %q", args[0])
	}
	d.log.Printf("restart %s requested", args[0])
	d.mgr.Restart(args[0])
	return nil
}

func (d *Daemon) status() StatusReply {
	d.mu.Lock()
	st := StatusReply{PID: os.Getpid(), Version: d.o.Version, Path: os.Getenv("PATH"), ConfigError: d.configErr}
	for _, s := range d.sessions {
		info := s.gw.Info()
		info.ClientName = s.client
		st.Sessions = append(st.Sessions, info)
	}
	d.mu.Unlock()
	sort.Slice(st.Sessions, func(i, j int) bool { return st.Sessions[i].ID < st.Sessions[j].ID })
	st.Servers = d.servers()
	return st
}

// servers is the manager's status plus every configured server it has not seen yet, by name.
func (d *Daemon) servers() []downstream.ServerStatus {
	out := d.mgr.Status()
	seen := map[string]bool{}
	for _, s := range out {
		seen[s.Name] = true
	}
	for name := range d.cfg.Load().Servers {
		if !seen[name] {
			out = append(out, downstream.ServerStatus{Name: name, State: "stopped"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// which decides every server and, for ready shared servers, every tool for the folder in Args[0]
// (or the hello's Dir).
func (d *Daemon) which(h Hello) (WhichReply, error) {
	dir := h.Dir
	if len(h.Args) > 0 {
		dir = h.Args[0]
	}
	if dir == "" {
		return WhichReply{}, errors.New("which needs a folder")
	}
	dir = rules.Normalize(dir)
	cfg := d.cfg.Load()
	states := map[string]string{}
	for _, s := range d.servers() {
		states[s.Name] = s.State
	}
	reply := WhichReply{Dir: dir, Skills: WhichSkills(d.Catalog(), cfg, dir)}
	for _, sd := range gateway.Servers(cfg, dir) {
		ws := WhichServer{Name: sd.Name, Decision: sd.Decision, State: states[sd.Name]}
		// A perSession server would start a new instance for this request, so it lists no tools.
		if srv := cfg.Servers[sd.Name]; ws.State == "ready" && srv != nil && !srv.PerSession {
			ws.Tools = d.whichTools(cfg, sd.Name, dir)
		}
		reply.Servers = append(reply.Servers, ws)
	}
	return reply, nil
}

func (d *Daemon) whichTools(cfg *config.Config, server, dir string) []WhichTool {
	ctx, cancel := context.WithTimeout(d.ctx, 10*time.Second)
	defer cancel()
	id := "which-" + newID()
	conn, err := d.mgr.Acquire(ctx, server, id)
	if err != nil {
		return nil
	}
	defer d.mgr.Release(server, id)
	tools, err := conn.Tools(ctx)
	if err != nil {
		return nil
	}
	out := make([]WhichTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, WhichTool{Name: t.Name, Exposed: gateway.ExposedName(server, t.Name), Decision: gateway.ToolDecision(cfg, server, t.Name, dir)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
