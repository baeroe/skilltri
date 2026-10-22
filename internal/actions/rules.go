package actions

import (
	"fmt"
	"strconv"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/rules"
	"github.com/baeroe/skilltri/internal/target"
)

// resolve is target.Resolve with a failure wrapped in ErrNotFound.
func resolve(c *config.Config, t target.Target, create bool) (target.Ref, error) {
	ref, err := target.Resolve(c, t, create)
	if err != nil {
		return ref, &kindError{msg: err.Error(), kind: ErrNotFound, cause: err}
	}
	return ref, nil
}

// RuleAdd validates r and appends it to the node t. The warnings name targets the catalog or the
// running daemon does not know (the rule is kept anyway).
func RuleAdd(e Env, t target.Target, r rules.Rule) (warnings []string, err error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	err = e.edit(func(c *config.Config) error {
		ref, err := resolve(c, t, true)
		if err != nil {
			return err
		}
		*ref.Rules = append(*ref.Rules, r)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return e.warnUnknown(t), nil
}

// NoRuleError says a rule index is out of range for a node; it matches ErrNotFound.
type NoRuleError struct {
	Index  string
	Target string
	Count  int
}

func (e *NoRuleError) Error() string {
	return fmt.Sprintf("no rule %q on %s (it has %d)", e.Index, e.Target, e.Count)
}
func (e *NoRuleError) Unwrap() error { return ErrNotFound }

// RuleRemove removes the rule with the 1-based index shown by `rule list`. A missing node or an
// index out of range wraps ErrNotFound.
func RuleRemove(e Env, t target.Target, index int) error {
	return e.edit(func(c *config.Config) error {
		ref, err := resolve(c, t, false)
		if err != nil {
			return err
		}
		if index < 1 || index > len(*ref.Rules) {
			return &NoRuleError{Index: strconv.Itoa(index), Target: t.String(), Count: len(*ref.Rules)}
		}
		*ref.Rules = append((*ref.Rules)[:index-1:index-1], (*ref.Rules)[index:]...)
		return nil
	})
}

// RuleClear removes every rule of the node t.
func RuleClear(e Env, t target.Target) error {
	return e.edit(func(c *config.Config) error {
		ref, err := resolve(c, t, false)
		if err != nil {
			return err
		}
		*ref.Rules = nil
		return nil
	})
}

// RuleSet replaces the rule list of the node t with rs (validated; nil clears it), e.g. to put a
// removed rule back at its old place. A missing tool, plugin or skill entry is made like RuleAdd.
func RuleSet(e Env, t target.Target, rs []rules.Rule) error {
	for _, r := range rs {
		if err := r.Validate(); err != nil {
			return err
		}
	}
	return e.edit(func(c *config.Config) error {
		ref, err := resolve(c, t, true)
		if err != nil {
			return err
		}
		*ref.Rules = append([]rules.Rule(nil), rs...)
		return nil
	})
}

// SetDisabled enables (disabled=false) or disables the node t. The warnings are those of RuleAdd.
func SetDisabled(e Env, t target.Target, disabled bool) (warnings []string, err error) {
	err = e.edit(func(c *config.Config) error {
		ref, err := resolve(c, t, true)
		if err != nil {
			return err
		}
		*ref.Disabled = disabled
		return nil
	})
	if err != nil {
		return nil, err
	}
	return e.warnUnknown(t), nil
}
