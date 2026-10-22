package tui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
)

// maxPathCands is how many matching folders a completion lists.
const maxPathCands = 8

// pathInput adds folder completion to a text input holding an absolute or ~-prefixed path: tab
// extends the value to the longest common prefix of the matching folders and lists up to
// maxPathCands of them. A leading ~ is expanded with home for the lookup but kept in the value.
type pathInput struct {
	home  string
	cands []string // folders matching the last completion (empty: none shown)
	more  int      // matches beyond cands
}

// clear hides the candidate list (after any key other than tab).
func (p *pathInput) clear() { p.cands, p.more = nil, 0 }

// complete completes the value of ti; it reports whether it changed the value or listed
// candidates (false: nothing to complete, e.g. a relative path or no folder matches).
func (p *pathInput) complete(ti *textinput.Model) bool {
	p.clear()
	v := ti.Value()
	set := func(nv string) {
		ti.SetValue(nv)
		ti.CursorEnd()
	}
	switch {
	case v == "~":
		set("~/")
		return true
	case !strings.HasPrefix(v, "~/") && !filepath.IsAbs(v):
		return false
	}
	i := strings.LastIndex(v, "/")
	dirPart, base := v[:i+1], v[i+1:]
	lookup := dirPart
	if strings.HasPrefix(lookup, "~/") {
		lookup = filepath.Join(p.home, lookup[2:])
	}
	entries, err := os.ReadDir(lookup)
	if err != nil {
		return false
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if !strings.HasPrefix(n, base) || strings.HasPrefix(n, ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		if !e.IsDir() {
			if fi, err := os.Stat(filepath.Join(lookup, n)); err != nil || !fi.IsDir() { // symlinks
				continue
			}
		}
		names = append(names, n)
	}
	switch len(names) {
	case 0:
		return false
	case 1:
		nv := dirPart + names[0] + "/"
		set(nv)
		return nv != v
	}
	if cp := commonPrefix(names); len(cp) > len(base) {
		set(dirPart + cp)
	}
	p.cands = names[:min(len(names), maxPathCands)]
	p.more = len(names) - len(p.cands)
	return true
}

// commonPrefix is the longest common prefix of names (byte-wise, cut back to whole runes).
func commonPrefix(names []string) string {
	cp := names[0]
	for _, n := range names[1:] {
		for !strings.HasPrefix(n, cp) {
			cp = cp[:len(cp)-1]
		}
	}
	return strings.ToValidUTF8(cp, "")
}

// candLines are the listed candidates, wrapped to w cells (nil when none are listed).
func (p *pathInput) candLines(w int) []string {
	if len(p.cands) == 0 {
		return nil
	}
	var parts []string
	for _, c := range p.cands {
		parts = append(parts, oneLine(c)+"/")
	}
	if p.more > 0 {
		parts = append(parts, "+"+itoa(p.more)+" more")
	}
	return wrap(strings.Join(parts, "  "), w)
}
