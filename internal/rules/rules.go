// Package rules decides whether an MCP server, tool or skill is active for a session folder.
// Rules are path-only and compared after normalization (~, Clean, symlinks); the nearest level
// that has rules decides, and every decision carries a human-readable reason.
package rules

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Mode is how a rule's path is compared with the session folder.
type Mode string

const (
	Only    Mode = "only"     // exactly this folder
	Tree    Mode = "tree"     // this folder or any folder below it
	Not     Mode = "not"      // any folder except exactly this one
	NotTree Mode = "not-tree" // any folder except this one and the folders below it
)

// Rule is a path plus a mode.
type Rule struct {
	Path string `json:"path"`
	Mode Mode   `json:"mode"`
}

// Validate checks for a known mode and a non-empty absolute or ~-prefixed path.
func (r Rule) Validate() error {
	switch r.Mode {
	case Only, Tree, Not, NotTree:
	default:
		return fmt.Errorf("unknown rule mode %q", r.Mode)
	}
	if r.Path == "" {
		return fmt.Errorf("rule path is empty")
	}
	if r.Path != "~" && !strings.HasPrefix(r.Path, "~/") && !filepath.IsAbs(r.Path) {
		return fmt.Errorf("rule path %q must be absolute or start with ~", r.Path)
	}
	return nil
}

func (r Rule) include() bool { return r.Mode == Only || r.Mode == Tree }

// Level is one step of a hierarchy (source, plugin, skill or server, tool).
type Level struct {
	Name     string
	Disabled bool
	Rules    []Rule
}

// Decision is the result of Evaluate.
type Decision struct {
	Active bool
	Reason string
}

// Normalize expands a leading ~, makes the path absolute and clean, and resolves symlinks.
// It never fails: for a path that does not exist, symlinks are resolved on the longest
// existing ancestor and the remaining components are appended.
func Normalize(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[1:])
		}
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	path = filepath.Clean(path)

	rest := ""
	cur := path
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return path
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// matches reports whether dir (normalized) satisfies the rule at normalized path p.
func matches(m Mode, p, dir string) bool {
	switch m {
	case Only, Not:
		return dir == p
	case Tree, NotTree:
		return dir == p || strings.HasPrefix(dir, strings.TrimSuffix(p, "/")+"/")
	}
	return false
}

// Evaluate decides whether an item is active for dir (already normalized). Levels run from
// outermost to innermost. Any disabled level switches the item off; otherwise the innermost
// level with rules decides: active if it has no includes or one matches, and no exclude matches.
func Evaluate(levels []Level, dir string) Decision {
	for _, l := range levels {
		if l.Disabled {
			return Decision{Reason: "disabled: " + l.Name}
		}
	}
	var lvl *Level
	for i := len(levels) - 1; i >= 0; i-- {
		if len(levels[i].Rules) > 0 {
			lvl = &levels[i]
			break
		}
	}
	if lvl == nil {
		return Decision{Active: true, Reason: "no rules: active everywhere"}
	}

	norm := make(map[string]string, len(lvl.Rules))
	normalized := func(path string) string {
		n, ok := norm[path]
		if !ok {
			n = Normalize(path)
			norm[path] = n
		}
		return n
	}

	for _, r := range lvl.Rules {
		if !r.include() && matches(r.Mode, normalized(r.Path), dir) {
			return Decision{Reason: fmt.Sprintf("%s: excluded by %s %s", lvl.Name, r.Mode, normalized(r.Path))}
		}
	}
	hasInclude := false
	for _, r := range lvl.Rules {
		if !r.include() {
			continue
		}
		hasInclude = true
		if matches(r.Mode, normalized(r.Path), dir) {
			return Decision{Active: true, Reason: fmt.Sprintf("%s: included by %s %s", lvl.Name, r.Mode, normalized(r.Path))}
		}
	}
	if hasInclude {
		return Decision{Reason: lvl.Name + ": no include matches"}
	}
	return Decision{Active: true, Reason: lvl.Name + ": no exclude matches"}
}
