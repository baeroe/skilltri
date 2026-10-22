// Package target addresses the nodes of the config tree (servers, tools, sources, plugins,
// skills) with path-like strings shared by the CLI and the TUI.
package target

import (
	"fmt"
	"strings"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/rules"
)

// Target names one node. Kind is "server", "tool", "source", "plugin" or "skill".
type Target struct {
	Kind                                string
	Server, Tool, Source, Plugin, Skill string
}

// Parse reads "server/<n>", "server/<n>/tool/<t>", "source/<n>", "source/<n>/plugin/<p>" or
// "source/<n>/skill/<id>".
func Parse(s string) (Target, error) {
	p := strings.Split(s, "/")
	for _, seg := range p {
		if seg == "" {
			return Target{}, fmt.Errorf("invalid target %q", s)
		}
	}
	switch {
	case len(p) == 2 && p[0] == "server":
		return Target{Kind: "server", Server: p[1]}, nil
	case len(p) == 4 && p[0] == "server" && p[2] == "tool":
		return Target{Kind: "tool", Server: p[1], Tool: p[3]}, nil
	case len(p) == 2 && p[0] == "source":
		return Target{Kind: "source", Source: p[1]}, nil
	case len(p) == 4 && p[0] == "source" && p[2] == "plugin":
		return Target{Kind: "plugin", Source: p[1], Plugin: p[3]}, nil
	case len(p) == 4 && p[0] == "source" && p[2] == "skill":
		return Target{Kind: "skill", Source: p[1], Skill: p[3]}, nil
	}
	return Target{}, fmt.Errorf("invalid target %q (want server/<n>[/tool/<t>] or source/<n>[/plugin/<p>|/skill/<id>])", s)
}

// String is the inverse of Parse.
func (t Target) String() string {
	switch t.Kind {
	case "server":
		return "server/" + t.Server
	case "tool":
		return "server/" + t.Server + "/tool/" + t.Tool
	case "source":
		return "source/" + t.Source
	case "plugin":
		return "source/" + t.Source + "/plugin/" + t.Plugin
	case "skill":
		return "source/" + t.Source + "/skill/" + t.Skill
	}
	return ""
}

// Ref points at the live Disabled flag and rule list of a node inside a Config.
type Ref struct {
	Disabled *bool
	Rules    *[]rules.Rule
}

// Resolve returns a Ref into cfg. With create, a missing tool, plugin or skill entry is made (a new
// plugin node keeps the plugin's current on/off state, see config.Source.PluginEnabled);
// a missing server or source is always an error, and a failed Resolve changes nothing.
func Resolve(cfg *config.Config, t Target, create bool) (Ref, error) {
	switch t.Kind {
	case "server", "tool":
		srv := cfg.Servers[t.Server]
		if srv == nil {
			return Ref{}, fmt.Errorf("no such server %q", t.Server)
		}
		if t.Kind == "server" {
			return Ref{&srv.Disabled, &srv.Rules}, nil
		}
		tool := srv.Tools[t.Tool]
		if tool == nil {
			if !create {
				return Ref{}, fmt.Errorf("no settings for tool %q of server %q", t.Tool, t.Server)
			}
			if srv.Tools == nil {
				srv.Tools = map[string]*config.Tool{}
			}
			tool = &config.Tool{}
			srv.Tools[t.Tool] = tool
		}
		return Ref{&tool.Disabled, &tool.Rules}, nil
	case "source", "plugin", "skill":
		src := cfg.Sources[t.Source]
		if src == nil {
			return Ref{}, fmt.Errorf("no such source %q", t.Source)
		}
		if t.Kind == "source" {
			return Ref{&src.Disabled, &src.Rules}, nil
		}
		nodes, name := &src.Plugins, t.Plugin
		if t.Kind == "skill" {
			nodes, name = &src.Skills, t.Skill
		}
		n := (*nodes)[name]
		if n == nil {
			if !create {
				return Ref{}, fmt.Errorf("no settings for %s %q of source %q", t.Kind, name, t.Source)
			}
			if *nodes == nil {
				*nodes = map[string]*config.Node{}
			}
			n = &config.Node{}
			if t.Kind == "plugin" { // a new node must not change whether the plugin is on
				n.Disabled = !src.PluginEnabled(name)
			}
			(*nodes)[name] = n
		}
		return Ref{&n.Disabled, &n.Rules}, nil
	}
	return Ref{}, fmt.Errorf("unknown target kind %q", t.Kind)
}
