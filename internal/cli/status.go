// Package cli implements the commands that talk to the daemon: `status`, `which` and `daemon stop`.
// Both ask the running daemon over the control socket; `which` falls back to the config file
// when none runs, so folder rules can be checked before the daemon is up.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"text/tabwriter"

	"github.com/baeroe/skilltri/internal/daemon"
	"github.com/baeroe/skilltri/internal/paths"
)

// errOut receives error messages; tests swap it.
var errOut io.Writer = os.Stderr

// notRunning reports whether err means that no daemon listens on the socket.
func notRunning(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

func printJSON(w io.Writer, v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(w, string(b))
}

// Status prints the daemon state to w and returns the exit code: 1 when no daemon runs.
func Status(w io.Writer, jsonOut bool) int {
	var reply daemon.StatusReply
	err := daemon.Control(paths.SocketPath(), daemon.Hello{Cmd: "status"}, &reply)
	switch {
	case notRunning(err):
		if jsonOut {
			fmt.Fprintln(w, `{"running":false}`)
		} else {
			fmt.Fprintln(errOut, "daemon not running")
		}
		return 1
	case err != nil:
		fmt.Fprintf(errOut, "skilltri: status: %v\n", err)
		return 1
	}
	if jsonOut {
		printJSON(w, reply)
	} else {
		renderStatus(w, reply)
	}
	return 0
}

func renderStatus(w io.Writer, r daemon.StatusReply) {
	fmt.Fprintf(w, "daemon: running (pid %d, version %s)\n", r.PID, r.Version)
	if r.Path != "" {
		fmt.Fprintf(w, "path: %s\n", r.Path)
	}
	if r.ConfigError != "" {
		fmt.Fprintf(w, "config error: %s\n", r.ConfigError)
	}
	if len(r.Servers) > 0 {
		fmt.Fprintln(w, "\nservers:")
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, s := range r.Servers {
			fmt.Fprintf(tw, "  %s\t%s\t%d tools\t%d sessions\t%s\n", s.Name, s.State, s.Tools, s.Sessions, s.Error)
		}
		tw.Flush()
	}
	fmt.Fprintf(w, "\nsessions: %d\n", len(r.Sessions))
	for _, s := range r.Sessions {
		client := s.ClientName
		if client == "" {
			client = "unknown client"
		}
		fmt.Fprintf(w, "  %s  %s\n", s.Dir, client)
	}
}
