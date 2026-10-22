package cli

import (
	"errors"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/baeroe/skilltri/internal/actions"
	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/paths"
)

// PluginServerRow is one line of `plugin-server list`. State is available, enabled, needs-review,
// or (see catalog.ReviewState) gone, source disabled or not fetched yet; Name is the config server
// it was imported as (empty when available).
type PluginServerRow struct {
	Source string `json:"source"`
	Plugin string `json:"plugin"`
	Server string `json:"server"`
	State  string `json:"state"`
	Name   string `json:"name,omitempty"`
}

type psKey struct{ source, plugin, server string }

// PluginServerList prints the MCP servers bundled with marketplace plugins and what became of them.
func PluginServerList(w io.Writer, jsonOut bool) int {
	cfg, err := config.Load(paths.ConfigFile())
	if err != nil {
		return fail(err)
	}
	c := offlineCatalog(cfg)
	review := catalog.ReviewState(cfg, c)
	imported := map[psKey]string{}
	for _, n := range sortedNames(cfg.Servers) {
		if o := cfg.Servers[n].Origin; o != nil {
			imported[psKey{o.Source, o.Plugin, o.Server}] = n
		}
	}
	rows := []PluginServerRow{}
	seen := map[psKey]bool{}
	for _, s := range c.Servers {
		k := psKey{s.Source, s.Plugin, s.Name}
		seen[k] = true
		row := PluginServerRow{Source: s.Source, Plugin: s.Plugin, Server: s.Name, State: "available"}
		if n, ok := imported[k]; ok {
			row.Name, row.State = n, "enabled"
			if review[n] == "needs-review" {
				row.State = "needs-review"
			}
		}
		rows = append(rows, row)
	}
	for _, n := range sortedNames(cfg.Servers) {
		if o := cfg.Servers[n].Origin; o != nil && !seen[psKey{o.Source, o.Plugin, o.Server}] {
			rows = append(rows, PluginServerRow{Source: o.Source, Plugin: o.Plugin, Server: o.Server, State: review[n], Name: n})
		}
	}
	if jsonOut {
		printJSON(w, rows)
		return 0
	}
	if len(rows) == 0 {
		fmt.Fprintln(w, "no plugin servers")
		return 0
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, r := range rows {
		state := r.State
		switch {
		case r.State == "enabled":
			state = "enabled as " + r.Name
		case r.Name != "":
			state += " (" + r.Name + ")"
		}
		fmt.Fprintf(tw, "%s/%s/%s\t%s\n", r.Source, r.Plugin, r.Server, state)
	}
	tw.Flush()
	return 0
}

// PluginServerEnable copies the plugin server ref (SOURCE/PLUGIN/SERVER) into the config as a server
// called name (default "<plugin>-<server>") with the plugin's effective rules and its Origin.
func PluginServerEnable(w io.Writer, ref, name string) int {
	name, err := actions.PluginServerEnable(actEnv, ref, name)
	if errors.Is(err, actions.ErrInvalid) {
		fmt.Fprintf(errOut, "skilltri: %v\n", err)
		return 2
	}
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(w, "enabled %s as server %s\n", ref, name)
	return 0
}

// PluginServerAccept replaces the connection fields of the imported server name with the plugin's
// current definition and updates its DefHash. Rules, tools and flags stay.
func PluginServerAccept(w io.Writer, name string) int {
	if err := actions.PluginServerAccept(actEnv, name); err != nil {
		return fail(err)
	}
	fmt.Fprintf(w, "accepted the current definition for server %s\n", name)
	return 0
}
