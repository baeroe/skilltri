package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/daemon"
	"github.com/baeroe/skilltri/internal/paths"
	"github.com/baeroe/skilltri/internal/rules"
)

// Which prints which servers and tools a session in dir would get. An empty dir means the
// working directory. Without a daemon, it decides from the config file and says that tool lists
// are unavailable. The exit code is 1 on errors.
func Which(w io.Writer, dir string, jsonOut bool) int {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(errOut, "skilltri: which: %v\n", err)
			return 1
		}
		dir = wd
	}
	dir = rules.Normalize(dir)

	var reply daemon.WhichReply
	offline := false
	err := daemon.Control(paths.SocketPath(), daemon.Hello{Cmd: "which", Args: []string{dir}}, &reply)
	switch {
	case notRunning(err):
		offline = true
		cfg, err := config.Load(paths.ConfigFile())
		if err != nil {
			fmt.Fprintf(errOut, "skilltri: which: %v\n", err)
			return 1
		}
		c, err := catalog.Load(catalog.Path())
		if err != nil {
			c = nil
		}
		reply = daemon.WhichOffline(cfg, c, dir)
	case err != nil:
		fmt.Fprintf(errOut, "skilltri: which: %v\n", err)
		return 1
	}
	if jsonOut {
		printJSON(w, reply)
		return 0
	}
	renderWhich(w, reply)
	if offline {
		fmt.Fprintln(w, "\ndaemon not running: tool lists unavailable")
	}
	return 0
}

func mark(active bool) string {
	if active {
		return "✓"
	}
	return "✗"
}

func renderWhich(w io.Writer, r daemon.WhichReply) {
	fmt.Fprintf(w, "folder: %s\n\n", r.Dir)
	if len(r.Servers) == 0 {
		fmt.Fprintln(w, "no servers configured")
	}
	for _, s := range r.Servers {
		fmt.Fprintf(w, "%s %s  (%s)\n", mark(s.Decision.Active), s.Name, s.Decision.Reason)
		if !s.Decision.Active {
			continue
		}
		for _, t := range s.Tools {
			line := fmt.Sprintf("    %s %s", mark(t.Decision.Active), t.Exposed)
			if t.Decision.Reason != "" && !t.Decision.Active {
				line += "  (" + t.Decision.Reason + ")"
			}
			fmt.Fprintln(w, line)
		}
	}
	if len(r.Skills) > 0 {
		fmt.Fprintln(w, "\nskills:")
	}
	for _, s := range r.Skills {
		from := s.Source
		if s.Plugin != "" { // the entry name, which plugin targets use
			from += ", plugin " + s.Plugin
		}
		line := fmt.Sprintf("  %s %s  (%s)", mark(s.Decision.Active), s.ID, from)
		if !s.Decision.Active {
			line += "  " + s.Decision.Reason
		}
		fmt.Fprintln(w, line)
	}
}
