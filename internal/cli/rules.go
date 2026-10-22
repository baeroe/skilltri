package cli

import (
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/baeroe/skilltri/internal/actions"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/paths"
	"github.com/baeroe/skilltri/internal/rules"
	"github.com/baeroe/skilltri/internal/target"
)

func sortStrings(s []string) { sort.Strings(s) }

// warn prints the warnings of an action to errOut.
func warn(ws []string) {
	for _, w := range ws {
		fmt.Fprintf(errOut, "skilltri: warning: %s\n", w)
	}
}

// RuleAdd appends a rule (mode, path as given) to the node named by tgt.
func RuleAdd(w io.Writer, tgt, mode, path string) int {
	t, err := target.Parse(tgt)
	if err != nil {
		fmt.Fprintf(errOut, "skilltri: %v\n", err)
		return 2
	}
	ws, err := actions.RuleAdd(actEnv, t, rules.Rule{Path: path, Mode: rules.Mode(mode)})
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(w, "added rule %s %s to %s\n", mode, path, t)
	warn(ws)
	return 0
}

// RuleRemove removes the rule with the 1-based index shown by `rule list`, or all rules.
func RuleRemove(w io.Writer, tgt, index string) int {
	t, err := target.Parse(tgt)
	if err != nil {
		fmt.Fprintf(errOut, "skilltri: %v\n", err)
		return 2
	}
	if index == "all" {
		err = actions.RuleClear(actEnv, t)
	} else if n, convErr := strconv.Atoi(index); convErr != nil {
		err = fmt.Errorf("no rule %q on %s", index, t)
	} else {
		err = actions.RuleRemove(actEnv, t, n)
	}
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(w, "removed rule %s from %s\n", index, t)
	return 0
}

// RuleSet is the rule list of one target.
type RuleSet struct {
	Target string       `json:"target"`
	Rules  []rules.Rule `json:"rules"`
}

// RuleList prints the rules of tgt, or of every node that has rules when tgt is empty.
func RuleList(w io.Writer, tgt string, jsonOut bool) int {
	cfg, err := config.Load(paths.ConfigFile())
	if err != nil {
		return fail(err)
	}
	var sets []RuleSet
	if tgt != "" {
		t, err := target.Parse(tgt)
		if err != nil {
			fmt.Fprintf(errOut, "skilltri: %v\n", err)
			return 2
		}
		ref, err := target.Resolve(cfg, t, false)
		if err != nil {
			return fail(err)
		}
		sets = append(sets, RuleSet{t.String(), append([]rules.Rule{}, *ref.Rules...)})
	} else {
		sets = allRuleSets(cfg)
	}
	if jsonOut {
		if sets == nil {
			sets = []RuleSet{}
		}
		printJSON(w, sets)
		return 0
	}
	if len(sets) == 0 {
		fmt.Fprintln(w, "no rules")
	}
	for _, s := range sets {
		fmt.Fprintln(w, s.Target)
		for i, r := range s.Rules {
			fmt.Fprintf(w, "  %d  %s  %s\n", i+1, r.Mode, r.Path)
		}
	}
	return 0
}

// allRuleSets lists every node of cfg that has at least one rule, in target order.
func allRuleSets(cfg *config.Config) []RuleSet {
	var out []RuleSet
	add := func(t target.Target, rs []rules.Rule) {
		if len(rs) > 0 {
			out = append(out, RuleSet{t.String(), rs})
		}
	}
	for _, n := range sortedNames(cfg.Servers) {
		s := cfg.Servers[n]
		add(target.Target{Kind: "server", Server: n}, s.Rules)
		for _, tn := range sortedNames(s.Tools) {
			add(target.Target{Kind: "tool", Server: n, Tool: tn}, s.Tools[tn].Rules)
		}
	}
	for _, n := range sortedNames(cfg.Sources) {
		s := cfg.Sources[n]
		add(target.Target{Kind: "source", Source: n}, s.Rules)
		for _, p := range sortedNames(s.Plugins) {
			add(target.Target{Kind: "plugin", Source: n, Plugin: p}, s.Plugins[p].Rules)
		}
		for _, k := range sortedNames(s.Skills) {
			add(target.Target{Kind: "skill", Source: n, Skill: k}, s.Skills[k].Rules)
		}
	}
	return out
}

// SetDisabled enables (disabled=false) or disables the node named by tgt.
func SetDisabled(w io.Writer, tgt string, disabled bool) int {
	t, err := target.Parse(tgt)
	if err != nil {
		fmt.Fprintf(errOut, "skilltri: %v\n", err)
		return 2
	}
	ws, err := actions.SetDisabled(actEnv, t, disabled)
	if err != nil {
		return fail(err)
	}
	verb := "enabled"
	if disabled {
		verb = "disabled"
	}
	fmt.Fprintf(w, "%s %s\n", verb, t)
	warn(ws)
	return 0
}
