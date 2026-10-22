package cli

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/paths"
	"github.com/baeroe/skilltri/internal/rules"
)

// PluginServerRow is one line of `plugin-server list`. State is available, enabled, needs-review or
// gone; Name is the config server it was imported as (empty when available).
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
			rows = append(rows, PluginServerRow{Source: o.Source, Plugin: o.Plugin, Server: o.Server, State: "gone", Name: n})
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

var badNameChars = regexp.MustCompile(`[^A-Za-z0-9-]`)

// defaultServerName is "<plugin>-<server>" with invalid characters replaced by '-', cut to 32 characters.
func defaultServerName(plugin, server string) string {
	n := strings.TrimLeft(badNameChars.ReplaceAllString(plugin+"-"+server, "-"), "-")
	if len(n) > 32 {
		n = n[:32]
	}
	return n
}

// PluginServerEnable copies the plugin server ref (SOURCE/PLUGIN/SERVER) into the config as a server
// called name (default "<plugin>-<server>") with the plugin's effective rules and its Origin.
func PluginServerEnable(w io.Writer, ref, name string) int {
	p := strings.Split(ref, "/")
	if len(p) != 3 || p[0] == "" || p[1] == "" || p[2] == "" {
		fmt.Fprintf(errOut, "skilltri: invalid plugin server %q (want SOURCE/PLUGIN/SERVER)\n", ref)
		return 2
	}
	source, plugin, server := p[0], p[1], p[2]
	if name == "" {
		name = defaultServerName(plugin, server)
	}
	if !config.ValidServerName(name) || name == config.ReservedName {
		return fail(fmt.Errorf("invalid server name %q (use --name)", name))
	}
	err := editConfig(func(c *config.Config) error {
		if c.Servers[name] != nil {
			return fmt.Errorf("server %q already exists (choose another with --name)", name)
		}
		ps, ok := findPluginServer(offlineCatalog(c), source, plugin, server)
		if !ok {
			return fmt.Errorf("no plugin server %s", ref)
		}
		srv := ps.Def
		srv.Rules = effectivePluginRules(c.Sources[source], plugin)
		srv.Origin = &config.Origin{Source: source, Plugin: plugin, Server: server, DefHash: ps.DefHash}
		c.Servers[name] = &srv
		return nil
	})
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(w, "enabled %s as server %s\n", ref, name)
	return 0
}

func findPluginServer(c *catalog.Catalog, source, plugin, server string) (catalog.PluginServer, bool) {
	for _, s := range c.Servers {
		if s.Source == source && s.Plugin == plugin && s.Name == server {
			return s, true
		}
	}
	return catalog.PluginServer{}, false
}

// effectivePluginRules returns a copy of the rules of the nearest level among the plugin node and
// the source that has any.
func effectivePluginRules(src *config.Source, plugin string) []rules.Rule {
	if src == nil {
		return nil
	}
	if n := src.Plugins[plugin]; n != nil && len(n.Rules) > 0 {
		return append([]rules.Rule(nil), n.Rules...)
	}
	return append([]rules.Rule(nil), src.Rules...)
}

// PluginServerAccept replaces the connection fields of the imported server name with the plugin's
// current definition and updates its DefHash. Rules, tools and flags stay.
func PluginServerAccept(w io.Writer, name string) int {
	err := editConfig(func(c *config.Config) error {
		srv := c.Servers[name]
		if srv == nil {
			return fmt.Errorf("no such server %q", name)
		}
		o := srv.Origin
		if o == nil {
			return fmt.Errorf("server %q was not imported from a plugin", name)
		}
		ps, ok := findPluginServer(offlineCatalog(c), o.Source, o.Plugin, o.Server)
		if !ok {
			return errors.New("the plugin or its server is gone")
		}
		d := ps.Def
		srv.Type, srv.Command, srv.Args, srv.Env, srv.Cwd, srv.URL, srv.Headers = d.Type, d.Command, d.Args, d.Env, d.Cwd, d.URL, d.Headers
		o.DefHash = ps.DefHash
		return nil
	})
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(w, "accepted the current definition for server %s\n", name)
	return 0
}
