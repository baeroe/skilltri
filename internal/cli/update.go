package cli

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/daemon"
	"github.com/baeroe/skilltri/internal/paths"
)

// Update syncs the named skill sources (all when names is empty) and prints one line per synced
// source, or the reply as JSON. The running daemon does the work, so its sessions see the new
// catalog; without a daemon it runs inline and saves the catalog. It returns 1 when a listed source
// failed (its last good checkout stays in use) or the update could not run.
func Update(w io.Writer, names []string, jsonOut bool) int {
	var reply daemon.UpdateReply
	err := daemon.Control(paths.SocketPath(), daemon.Hello{Cmd: "update", Args: names}, &reply)
	if notRunning(err) {
		reply, err = updateInline(names)
	}
	if err != nil {
		fmt.Fprintf(errOut, "skilltri: update: %v\n", err)
		return 1
	}
	if jsonOut {
		printJSON(w, reply)
	} else {
		renderUpdate(w, reply)
	}
	for _, s := range reply.Sources {
		if s.Error != "" {
			return 1
		}
	}
	return 0
}

// updateInline updates without a daemon and saves the rebuilt catalog.
func updateInline(names []string) (daemon.UpdateReply, error) {
	cfg, err := config.Load(paths.ConfigFile())
	if err != nil {
		return daemon.UpdateReply{}, err
	}
	reply, c := daemon.Update(context.Background(), daemon.DefaultBuilder(), cfg, names)
	if err := catalog.Save(catalog.Path(), c); err != nil {
		return reply, fmt.Errorf("save catalog: %w", err)
	}
	return reply, nil
}

func renderUpdate(w io.Writer, r daemon.UpdateReply) {
	if len(r.Sources) == 0 {
		fmt.Fprintln(w, "no sources to update")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, s := range r.Sources {
		commit, at := s.Commit, "-"
		if len(commit) > 7 {
			commit = commit[:7]
		}
		if commit == "" {
			commit = "-"
		}
		if !s.FetchedAt.IsZero() {
			at = s.FetchedAt.Local().Format(time.DateTime)
		}
		line := s.Name + "\t" + commit + "\t" + at
		if s.Error != "" {
			line += "\t" + s.Error
		}
		fmt.Fprintln(tw, line)
	}
	tw.Flush()
}
