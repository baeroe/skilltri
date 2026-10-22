package actions

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/rules"
)

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
// called name (default "<plugin>-<server>") with the plugin's effective rules and its Origin, and
// returns that name. A malformed ref wraps ErrInvalid, an unknown plugin server ErrNotFound and a
// taken name ErrExists.
func PluginServerEnable(e Env, ref, name string) (string, error) {
	p := strings.Split(ref, "/")
	if len(p) != 3 || p[0] == "" || p[1] == "" || p[2] == "" {
		return "", wrap(ErrInvalid, "invalid plugin server %q (want SOURCE/PLUGIN/SERVER)", ref)
	}
	source, plugin, server := p[0], p[1], p[2]
	if name == "" {
		name = defaultServerName(plugin, server)
	}
	if !config.ValidServerName(name) || name == config.ReservedName {
		return "", fmt.Errorf("invalid server name %q (use --name)", name)
	}
	err := e.edit(func(c *config.Config) error {
		if c.Servers[name] != nil {
			return wrap(ErrExists, "server %q already exists (choose another with --name)", name)
		}
		ps, ok := findPluginServer(e.OfflineCatalog(c), source, plugin, server)
		if !ok {
			return wrap(ErrNotFound, "no plugin server %s", ref)
		}
		srv := ps.Def
		srv.Rules = effectivePluginRules(c.Sources[source], plugin)
		srv.Origin = &config.Origin{Source: source, Plugin: plugin, Server: server, DefHash: ps.DefHash}
		if c.Servers == nil {
			c.Servers = map[string]*config.Server{}
		}
		c.Servers[name] = &srv
		return nil
	})
	if err != nil {
		return "", err
	}
	return name, nil
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
// current definition and updates its DefHash. Rules, tools and flags stay. A missing server wraps
// ErrNotFound.
func PluginServerAccept(e Env, name string) error {
	return e.edit(func(c *config.Config) error {
		srv := c.Servers[name]
		if srv == nil {
			return wrap(ErrNotFound, "no such server %q", name)
		}
		o := srv.Origin
		if o == nil {
			return fmt.Errorf("server %q was not imported from a plugin", name)
		}
		cat := e.OfflineCatalog(c)
		ps, ok := findPluginServer(cat, o.Source, o.Plugin, o.Server)
		if !ok {
			switch st := catalog.ReviewState(c, cat)[name]; st {
			case "gone":
				return wrap(ErrNotFound, "the plugin or its server is gone")
			default:
				return fmt.Errorf("cannot accept: %s", st)
			}
		}
		d := ps.Def
		srv.Type, srv.Command, srv.Args, srv.Env, srv.Cwd, srv.URL, srv.Headers = d.Type, d.Command, d.Args, d.Env, d.Cwd, d.URL, d.Headers
		o.DefHash = ps.DefHash
		return nil
	})
}
