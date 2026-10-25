package agenttools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/baeroe/skilltri/internal/actions"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/paths"
	"github.com/baeroe/skilltri/internal/rules"
	"github.com/baeroe/skilltri/internal/target"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const targetDesc = "Config node: server/<name>, server/<name>/tool/<tool>, source/<name>, source/<name>/plugin/<entry> or source/<name>/skill/<id>"

const ruleDesc = " A rule is {mode, path}: mode only (exactly this folder), tree (the folder and every folder below it), not (every folder except exactly this one) or not-tree (every folder outside the tree); path is absolute or starts with ~."

func modeSchema() map[string]any {
	return map[string]any{"type": "string", "enum": []string{"only", "tree", "not", "not-tree"}, "description": "Rule mode"}
}

func ruleSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"mode", "path"}, "properties": map[string]any{
		"mode": modeSchema(),
		"path": str("Absolute folder or ~/…"),
	}}
}

func init() {
	register(
		writeTool(&mcp.Tool{
			Name:        "skilltri_set_enabled",
			Description: "Enable or disable a config node (a server, one tool of a server, a skill source, a marketplace plugin or one skill) everywhere; folder rules still decide where an enabled node is active.",
			InputSchema: required(object(map[string]any{"target": str(targetDesc), "enabled": boolean("true enables, false disables")}), "target", "enabled"),
		}, setEnabled),
		writeTool(&mcp.Tool{
			Name:        "skilltri_add_rule",
			Description: "Append a folder rule to a config node, which then decides in which session folders the node is active." + ruleDesc,
			InputSchema: required(object(map[string]any{"target": str(targetDesc), "mode": modeSchema(), "path": str("Absolute folder or ~/…")}), "target", "mode", "path"),
		}, addRule),
		writeTool(&mcp.Tool{
			Name:        "skilltri_remove_rule",
			Description: "Remove one folder rule of a config node: index is 1-based in the order skilltri_list_servers or skilltri_list_sources show, and mode and path must match that rule (else nothing changes and the call fails, so a stale view never removes the wrong rule)." + ruleDesc,
			InputSchema: required(object(map[string]any{"target": str(targetDesc), "index": integer("1-based rule index"),
				"mode": modeSchema(), "path": str("Path of the rule to remove")}), "target", "index", "mode", "path"),
		}, removeRule),
		writeTool(&mcp.Tool{
			Name:        "skilltri_set_rules",
			Description: "Replace the whole folder rule list of a config node (an empty list removes every rule)." + ruleDesc,
			InputSchema: required(object(map[string]any{"target": str(targetDesc), "rules": map[string]any{"type": "array", "items": ruleSchema()}}), "target", "rules"),
		}, setRules),
	)
}

// targetArg parses the target string of a call and records it.
func targetArg(s string, ch *change) (target.Target, error) {
	ch.target = s
	if s == "" {
		return target.Target{}, errors.New("target is required")
	}
	return target.Parse(s)
}

func setEnabled(_ context.Context, b Backend, c Call, args json.RawMessage, ch *change) error {
	var a struct {
		Target  string `json:"target"`
		Enabled *bool  `json:"enabled"`
	}
	if err := parseArgs(args, &a); err != nil {
		return err
	}
	t, err := targetArg(a.Target, ch)
	if err != nil {
		return err
	}
	if a.Enabled == nil {
		return errors.New("enabled is required")
	}
	ch.summary = onOff(*a.Enabled)
	if ch.warnings, err = actions.SetDisabled(callEnv(b, c), t, !*a.Enabled); err != nil {
		return err
	}
	ch.message = fmt.Sprintf("%s %s.%s", t, ch.summary, applied)
	return nil
}

func ruleText(r rules.Rule) string { return string(r.Mode) + " " + r.Path }

func addRule(_ context.Context, b Backend, c Call, args json.RawMessage, ch *change) error {
	var a struct {
		Target string `json:"target"`
		Mode   string `json:"mode"`
		Path   string `json:"path"`
	}
	if err := parseArgs(args, &a); err != nil {
		return err
	}
	t, err := targetArg(a.Target, ch)
	if err != nil {
		return err
	}
	r := rules.Rule{Mode: rules.Mode(a.Mode), Path: a.Path}
	ch.summary = "add rule " + ruleText(r)
	if ch.warnings, err = actions.RuleAdd(callEnv(b, c), t, r); err != nil {
		return err
	}
	ch.message = fmt.Sprintf("Added rule %q to %s.%s", ruleText(r), t, applied)
	return nil
}

// currentRules reads the rule list of t from the config file (with create, a missing tool,
// plugin or skill node counts as having none).
func currentRules(e actions.Env, t target.Target, create bool) ([]rules.Rule, error) {
	path := e.ConfigPath
	if path == "" {
		path = paths.ConfigFile()
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	ref, err := target.Resolve(cfg, t, create)
	if err != nil {
		return nil, err
	}
	return slices.Clone(*ref.Rules), nil
}

func removeRule(_ context.Context, b Backend, c Call, args json.RawMessage, ch *change) error {
	var a struct {
		Target string `json:"target"`
		Index  int    `json:"index"`
		Mode   string `json:"mode"`
		Path   string `json:"path"`
	}
	if err := parseArgs(args, &a); err != nil {
		return err
	}
	t, err := targetArg(a.Target, ch)
	if err != nil {
		return err
	}
	want := rules.Rule{Mode: rules.Mode(a.Mode), Path: a.Path}
	ch.summary = fmt.Sprintf("remove rule %d (%s)", a.Index, ruleText(want))
	e := callEnv(b, c)
	cur, err := currentRules(e, t, false)
	if err != nil {
		return err
	}
	if a.Index < 1 || a.Index > len(cur) {
		return &actions.NoRuleError{Index: fmt.Sprint(a.Index), Target: t.String(), Count: len(cur)}
	}
	if got := cur[a.Index-1]; got != want {
		return fmt.Errorf("rule %d of %s is %q, not %q: list the rules again", a.Index, t, ruleText(got), ruleText(want))
	}
	next := slices.Delete(slices.Clone(cur), a.Index-1, a.Index)
	if err := actions.RuleSet(e, t, cur, next); err != nil {
		return err
	}
	ch.message = fmt.Sprintf("Removed rule %d (%s) of %s.%s", a.Index, ruleText(want), t, applied)
	return nil
}

// setRulesAttempts bounds the retries of skilltri_set_rules when another edit changes the list
// between reading and writing it.
const setRulesAttempts = 5

func setRules(_ context.Context, b Backend, c Call, args json.RawMessage, ch *change) error {
	var a struct {
		Target string        `json:"target"`
		Rules  *[]rules.Rule `json:"rules"`
	}
	if err := parseArgs(args, &a); err != nil {
		return err
	}
	t, err := targetArg(a.Target, ch)
	if err != nil {
		return err
	}
	if a.Rules == nil {
		return errors.New("rules is required (an empty list removes every rule)")
	}
	rs := *a.Rules
	ch.summary = fmt.Sprintf("set %d rules", len(rs))
	e := callEnv(b, c)
	for attempt := 1; ; attempt++ {
		cur, err := currentRules(e, t, true)
		if err != nil {
			return err
		}
		err = actions.RuleSet(e, t, cur, rs)
		if err == nil {
			break
		}
		if !errors.Is(err, actions.ErrChanged) || attempt == setRulesAttempts {
			return err
		}
	}
	ch.message = fmt.Sprintf("%s now has %d rules.%s", t, len(rs), applied)
	return nil
}
