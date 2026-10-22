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

	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/downstream"
	"github.com/baeroe/skilltri/internal/gateway"
	"github.com/baeroe/skilltri/internal/rules"
)

// controlTimeout bounds one control request, client and daemon side.
const controlTimeout = 30 * time.Second

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
	if h.V == 0 {
		h.V = helloVersion
	}
	if h.Kind == "" {
		h.Kind = "control"
	}
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(controlTimeout))
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
	_ = conn.SetDeadline(time.Now().Add(controlTimeout))
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
	reply := WhichReply{Dir: dir}
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
