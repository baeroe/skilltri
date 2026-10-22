// Package gateway is the harness-facing side of skilltri: one Session per harness connection,
// exposing only the downstream tools, prompts and resources whose folder rules match the session
// folder. Sessions hold no downstream connection; every list and call goes through the Manager.
package gateway

import (
	"sort"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/rules"
)

// ServerDecision is whether one server is active for a folder, and why.
type ServerDecision struct {
	Name     string
	Decision rules.Decision
}

// Servers decides every configured server for dir (already normalized), sorted by name.
func Servers(cfg *config.Config, dir string) []ServerDecision {
	names := make([]string, 0, len(cfg.Servers))
	for n := range cfg.Servers {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]ServerDecision, 0, len(names))
	for _, n := range names {
		out = append(out, ServerDecision{Name: n, Decision: rules.Evaluate([]rules.Level{serverLevel(n, cfg.Servers[n])}, dir)})
	}
	return out
}

// ToolDecision decides one tool of server for dir (already normalized) with the levels [server, tool].
func ToolDecision(cfg *config.Config, server, tool, dir string) rules.Decision {
	srv, ok := cfg.Servers[server]
	if !ok || srv == nil {
		return rules.Decision{Reason: "unknown server: " + server}
	}
	levels := []rules.Level{serverLevel(server, srv)}
	if t := srv.Tools[tool]; t != nil {
		levels = append(levels, rules.Level{Name: server + "/" + tool, Disabled: t.Disabled, Rules: t.Rules})
	}
	return rules.Evaluate(levels, dir)
}

func serverLevel(name string, s *config.Server) rules.Level {
	if s == nil {
		return rules.Level{Name: name, Disabled: true}
	}
	return rules.Level{Name: name, Disabled: s.Disabled, Rules: s.Rules}
}
