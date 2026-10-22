package actions

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/daemon"
	"github.com/baeroe/skilltri/internal/rules"
	"github.com/baeroe/skilltri/internal/target"
)

// warnUnknown returns a warning when a plugin, skill or tool target names nothing the current
// catalog (or, for tools, the running daemon) knows, with close matches. Settings for such names
// are kept: the plugin may not be fetched yet or the server not started. Nothing is returned when
// the check cannot be made.
func (e Env) warnUnknown(t target.Target) []string {
	var msg string
	switch t.Kind {
	case "plugin", "skill":
		msg = e.unknownCatalogTarget(t)
	case "tool":
		msg = e.unknownTool(t)
	}
	if msg == "" {
		return nil
	}
	return []string{msg}
}

func (e Env) currentCatalog() *catalog.Catalog {
	if c, err := catalog.Load(e.catalogPath()); err == nil {
		return c
	}
	cfg, err := config.Load(e.configPath())
	if err != nil {
		return nil
	}
	return e.OfflineCatalog(cfg)
}

func (e Env) unknownCatalogTarget(t target.Target) string {
	c := e.currentCatalog()
	if c == nil {
		return ""
	}
	var known, alias []string // alias: names that are not the key but mean it
	switch t.Kind {
	case "plugin":
		for _, p := range c.Plugins {
			if p.Source != t.Source {
				continue
			}
			if p.Plugin == t.Plugin {
				return ""
			}
			known = append(known, p.Plugin)
			if p.Name == t.Plugin {
				alias = append(alias, p.Plugin)
			}
		}
		if known == nil {
			return "" // not a marketplace, or not fetched yet: nothing to compare with
		}
		return fmt.Sprintf("source %s has no plugin %q in the current catalog (plugin targets use the marketplace entry name)%s",
			t.Source, t.Plugin, didYouMean(t, t.Plugin, known, alias))
	default:
		for _, e := range c.Skills {
			if e.Source != t.Source {
				continue
			}
			if e.ID == t.Skill {
				return ""
			}
			known = append(known, e.ID)
			if _, name, ok := strings.Cut(e.ID, ":"); ok && name == t.Skill {
				alias = append(alias, e.ID)
			}
		}
		if known == nil {
			return ""
		}
		return fmt.Sprintf("source %s has no skill %q in the current catalog (plugin skills are named <plugin>:<skill>)%s",
			t.Source, t.Skill, didYouMean(t, t.Skill, known, alias))
	}
}

// unknownTool asks the daemon which tools the server offers in the working directory; without a
// daemon, a server that is not ready or a folder where it is off, it cannot tell.
func (e Env) unknownTool(t target.Target) string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	var reply daemon.WhichReply
	if daemon.Control(e.socket(), daemon.Hello{Cmd: "which", Args: []string{rules.Normalize(wd)}}, &reply) != nil {
		return ""
	}
	for _, s := range reply.Servers {
		if s.Name != t.Server || s.State != "ready" || len(s.Tools) == 0 {
			continue
		}
		var known []string
		for _, tool := range s.Tools {
			if tool.Name == t.Tool {
				return ""
			}
			known = append(known, tool.Name)
		}
		return fmt.Sprintf("server %s has no tool %q%s", t.Server, t.Tool, didYouMean(t, t.Tool, known, nil))
	}
	return ""
}

// didYouMean suggests up to three targets: the aliases first, then names containing or contained
// in name or within two edits of it.
func didYouMean(t target.Target, name string, known, alias []string) string {
	seen := map[string]bool{}
	var picks []string
	add := func(n string) {
		if !seen[n] && len(picks) < 3 {
			seen[n] = true
			picks = append(picks, n)
		}
	}
	for _, a := range alias {
		add(a)
	}
	sort.Strings(known)
	lower := strings.ToLower(name)
	for _, k := range known {
		lk := strings.ToLower(k)
		if strings.Contains(lk, lower) || strings.Contains(lower, lk) || editDistance(lk, lower) <= 2 {
			add(k)
		}
	}
	if len(picks) == 0 {
		return ""
	}
	var out []string
	for _, p := range picks {
		s := t
		switch t.Kind {
		case "plugin":
			s.Plugin = p
		case "skill":
			s.Skill = p
		case "tool":
			s.Tool = p
		}
		out = append(out, s.String())
	}
	return "; did you mean " + strings.Join(out, " or ") + "?"
}

// editDistance is the Levenshtein distance of a and b (bytes).
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
