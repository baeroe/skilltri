package harness

import (
	"slices"
	"sort"
	"strings"

	"github.com/baeroe/skilltri/internal/config"
)

// Names are the display names of the harness ids.
var Names = map[string]string{
	Claude: "Claude Code", Codex: "Codex CLI", Gemini: "Gemini CLI", OpenCode: "opencode", Cursor: "Cursor",
}

// Duplicate is an MCP server entry of a harness config that registers a server skilltri serves
// too, so the harness gets it twice.
type Duplicate struct {
	Harness string // harness id
	Entry   Entry
	Server  string // the skilltri server it duplicates
	Reason  string // "same name" | "same command" | "same URL"
}

// Duplicates returns the entries of every harness config under home (Claude Code's per-project
// ones included) that duplicate an enabled server of servers: the same name, the same command and
// arguments, or the same URL (ignoring a trailing slash). Entries registering skilltri itself or
// 1MCP are left out. The result is in IDs order, then by project and name.
func Duplicates(home string, servers map[string]*config.Server) []Duplicate {
	names := make([]string, 0, len(servers))
	for n, s := range servers {
		if s != nil && !s.Disabled {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	var out []Duplicate
	for _, id := range IDs {
		es := entries(home, id)
		sort.SliceStable(es, func(i, j int) bool {
			if es[i].Project != es[j].Project {
				return es[i].Project < es[j].Project
			}
			return es[i].Name < es[j].Name
		})
		for _, e := range es {
			if e.Skilltri() || e.OneMCP() {
				continue
			}
			if srv, why := duplicateOf(e, names, servers); srv != "" {
				out = append(out, Duplicate{Harness: id, Entry: e, Server: srv, Reason: why})
			}
		}
	}
	return out
}

// duplicateOf returns the first server of names (sorted) that e duplicates and why.
func duplicateOf(e Entry, names []string, servers map[string]*config.Server) (string, string) {
	if slices.Contains(names, e.Name) {
		return e.Name, "same name"
	}
	for _, n := range names {
		s := servers[n]
		switch {
		case s.Command != "" && e.Command == s.Command && slices.Equal(e.Args, s.Args):
			return n, "same command"
		case s.URL != "" && e.URL != "" && strings.TrimSuffix(e.URL, "/") == strings.TrimSuffix(s.URL, "/"):
			return n, "same URL"
		}
	}
	return "", ""
}
