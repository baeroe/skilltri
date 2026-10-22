package tui

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/baeroe/skilltri/internal/actions"
	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/rules"
	"github.com/baeroe/skilltri/internal/target"
)

// ruleModes are the rule modes in the order the add form cycles them.
var ruleModes = []string{string(rules.Only), string(rules.Tree), string(rules.Not), string(rules.NotTree)}

// --- the node's levels ------------------------------------------------------------------------

// ruleNode is the hierarchy of a rules target as the gateway and the catalog evaluate it: levels
// from the outermost to the node's own (last), with a label per level ("source demo").
type ruleNode struct {
	levels []rules.Level
	labels []string
}

func (n *ruleNode) add(label string, l rules.Level) {
	n.levels = append(n.levels, l)
	n.labels = append(n.labels, label)
}

// own are the node's own rules.
func (n ruleNode) own() []rules.Rule { return n.levels[len(n.levels)-1].Rules }

// inherited are the rules of the nearest ancestor level that has rules, and that level's label.
func (n ruleNode) inherited() ([]rules.Rule, string) {
	for i := len(n.levels) - 2; i >= 0; i-- {
		if len(n.levels[i].Rules) > 0 {
			return n.levels[i].Rules, n.labels[i]
		}
	}
	return nil, ""
}

// disabled reports whether a level is disabled and which ancestor ("" for the node itself).
func (n ruleNode) disabled() (by string, off bool) {
	for i, l := range n.levels {
		if l.Disabled {
			if i == len(n.levels)-1 {
				return "", true
			}
			return n.labels[i], true
		}
	}
	return "", false
}

// resolveRuleNode finds the levels of tg in the snapshot: [server] and [server, tool] like the
// gateway, [source], [source, plugin] and catalog.Levels for skills. ok=false when the node no
// longer exists (its server or source is gone, or the catalog no longer has the plugin or skill).
func resolveRuleNode(s Snapshot, tg target.Target) (n ruleNode, ok bool) {
	cfg := s.Config
	if cfg == nil {
		return n, false
	}
	cat := s.Catalog
	if cat == nil {
		cat = &catalog.Catalog{}
	}
	switch tg.Kind {
	case "server", "tool":
		srv := cfg.Servers[tg.Server]
		if srv == nil {
			return n, false
		}
		n.add("server "+tg.Server, rules.Level{Name: tg.Server, Disabled: srv.Disabled, Rules: srv.Rules})
		if tg.Kind == "tool" {
			l := rules.Level{Name: tg.Server + "/" + tg.Tool}
			if tc := srv.Tools[tg.Tool]; tc != nil {
				l.Disabled, l.Rules = tc.Disabled, tc.Rules
			}
			n.add("tool "+tg.Tool, l)
		}
	case "source":
		src := cfg.Sources[tg.Source]
		if src == nil {
			return n, false
		}
		n.add("source "+tg.Source, rules.Level{Name: tg.Source, Disabled: src.Disabled, Rules: src.Rules})
	case "plugin":
		if cfg.Sources[tg.Source] == nil || !slices.ContainsFunc(cat.Plugins, func(p catalog.PluginInfo) bool {
			return p.Source == tg.Source && p.Plugin == tg.Plugin
		}) {
			return n, false
		}
		lv := catalog.Levels(cfg, catalog.Entry{Source: tg.Source, Plugin: tg.Plugin})
		n.add("source "+tg.Source, lv[0])
		n.add("plugin "+tg.Plugin, lv[1])
	case "skill":
		i := slices.IndexFunc(cat.Skills, func(e catalog.Entry) bool { return e.Source == tg.Source && e.ID == tg.Skill })
		if cfg.Sources[tg.Source] == nil || i < 0 {
			return n, false
		}
		e := cat.Skills[i]
		lv := catalog.Levels(cfg, e)
		n.add("source "+tg.Source, lv[0])
		if e.Plugin != "" {
			n.add("plugin "+e.Plugin, lv[1])
		}
		n.add("skill "+e.ID, lv[len(lv)-1])
	default:
		return n, false
	}
	return n, true
}

// explainRules says in one line where rules rs make a node active.
func explainRules(rs []rules.Rule, home string) string {
	var inc, exc []string
	for _, r := range rs {
		p := tildify(oneLine(r.Path), home)
		switch r.Mode {
		case rules.Only:
			inc = append(inc, p)
		case rules.Tree:
			inc = append(inc, p+" and below")
		case rules.Not:
			exc = append(exc, p)
		case rules.NotTree:
			exc = append(exc, p+" and below")
		}
	}
	switch {
	case len(inc) == 0 && len(exc) == 0:
		return "active everywhere"
	case len(inc) == 0:
		return "active everywhere except " + strings.Join(exc, ", ")
	case len(exc) == 0:
		return "active only in " + strings.Join(inc, " or ")
	}
	return "active only in " + strings.Join(inc, " or ") + "; except " + strings.Join(exc, ", ")
}

// explain is the effective behaviour of n: disabled, else that of its own or inherited rules.
func (n ruleNode) explain(home string) string {
	if by, off := n.disabled(); off {
		if by != "" {
			return "disabled by " + by
		}
		return "disabled"
	}
	rs := n.own()
	if len(rs) == 0 {
		rs, _ = n.inherited()
	}
	return explainRules(rs, home)
}

// isRulePath reports whether p is absolute or starts with ~ (as rules.Rule.Validate wants).
func isRulePath(p string) bool { return p == "~" || strings.HasPrefix(p, "~/") || filepath.IsAbs(p) }

// parseRule checks the add form's values: the inline errors of the mode and of the path field.
// The path is cleaned (a completed "~/dev/" becomes "~/dev") and keeps its ~.
func parseRule(mode, path string) (r rules.Rule, modeErr, pathErr string) {
	if !slices.Contains(ruleModes, mode) {
		modeErr = fmt.Sprintf("unknown mode %q (only, tree, not or not-tree)", mode)
	}
	switch {
	case path == "":
		pathErr = "enter a folder"
	case !isRulePath(path):
		pathErr = "must be an absolute path (or start with ~/)"
	case slices.Contains(strings.Split(path, "/"), ".."):
		pathErr = "no .. in rule paths: enter the folder itself"
	default:
		path = filepath.Clean(path)
	}
	r = rules.Rule{Path: path, Mode: rules.Mode(mode)}
	if modeErr == "" && pathErr == "" {
		if err := r.Validate(); err != nil {
			pathErr = err.Error()
		}
	}
	return r, modeErr, pathErr
}

// checkFolder evaluates n for the folder dir (~ expanded with home); the text is the reason with
// home shown as ~, or why dir cannot be checked.
func checkFolder(n ruleNode, dir, home string) (active bool, text string, ok bool) {
	dir = strings.TrimSpace(dir)
	if !isRulePath(dir) {
		return false, "enter an absolute folder (or ~/…)", false
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		dir = filepath.Join(home, dir[1:])
	}
	d := rules.Evaluate(n.levels, rules.Normalize(dir))
	text = oneLine(d.Reason)
	if home != "" {
		for _, h := range []string{rules.Normalize(home), home} {
			text = strings.ReplaceAll(text, strings.TrimSuffix(h, "/")+"/", "~/")
		}
	}
	return d.Active, text, true
}

// --- the dialog ---------------------------------------------------------------------------

// rulesModal edits the rules of one node: its own rules (numbered, editable), the inherited ones
// (read-only), the effective behaviour and a live check for a folder. It re-reads the node from
// every snapshot and stays open across reloads; a node that disappeared can no longer be edited.
type rulesModal struct {
	tg       target.Target
	what     string // "skill demo-alpha of demo-local"
	sel      int    // selected own rule (0-based)
	undo     *deletedRule
	check    textinput.Model // the live check's folder
	paths    pathInput
	checking bool         // the folder input has the focus
	busy     string       // a write runs or its reload is pending: keys are ignored, esc too
	own      []rules.Rule // the rules a successful write left, shown until its reload lands
	ownSet   bool
	waitSeq  int // the snapshot load that must land before the dialog takes keys again (0: none)
	memo     checkMemo
	note     string // the last result
	err      string
	warns    []string
}

// deletedRule is what u puts back: the rule and its 0-based index.
type deletedRule struct {
	rule  rules.Rule
	index int
}

// checkMemo caches the live check for one folder value and rule set (Normalize resolves symlinks).
type checkMemo struct {
	key           string
	active, valid bool
	text          string
}

// ruleSavedMsg is the result of a write of dialog d (add from the form add, delete, undo).
type ruleSavedMsg struct {
	d       *rulesModal
	add     *ruleAddModal
	text    string
	warns   []string
	err     error
	rules   []rules.Rule // the node's rules after the write
	deleted *deletedRule // a delete: what u restores
	undone  bool
}

// openRules opens the rules dialog of tg; what names the node in its title.
func openRules(m *Model, tg target.Target, what string) tea.Cmd {
	d := &rulesModal{tg: tg, what: what, check: newInput(tildify(m.opts.Cwd, m.opts.Home), "absolute folder (or ~/…)"),
		paths: pathInput{home: m.opts.Home}}
	d.layout(m.width)
	m.openModal(d)
	return nil
}

// node is the dialog's node from the snapshot, with the rules of a write whose reload is pending.
func (d *rulesModal) node(m *Model) (ruleNode, bool) {
	n, ok := resolveRuleNode(m.snap, d.tg)
	if ok && d.ownSet {
		n.levels[len(n.levels)-1].Rules = d.own
	}
	return n, ok
}

func (d *rulesModal) workKey() string { return fmt.Sprintf("rules:%p", d) }
func (d *rulesModal) ownsEsc() bool   { return d.checking || d.busy != "" }

// dims are the box width and its inner width in a body w cells wide.
func (d *rulesModal) dims(w int) (bw, iw int) {
	bw = min(w, 84)
	return bw, max(bw-4, 1)
}

func (d *rulesModal) layout(w int) {
	_, iw := d.dims(w)
	d.check.Width = max(iw-checkLabelW-1, 1)
}

const checkLabelW = 7

func (d *rulesModal) hints() []string {
	if d.checking {
		return []string{"tab", "complete", "enter esc", "done"}
	}
	return []string{"a", "add", "d", "delete", "u", "undo", "↑↓", "select", "c", "check folder", "esc", "close"}
}

func (d *rulesModal) update(m *Model, msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case ruleSavedMsg:
		if msg.d != d {
			return nil, false
		}
		if msg.err != nil {
			m.end(d.workKey())
			d.busy = ""
			if errors.Is(msg.err, actions.ErrChanged) {
				d.err, d.note = msg.err.Error()+": reloaded, check and try again", ""
				return m.reload(), false
			}
			if msg.add == nil { // the add form shows its own errors
				d.err, d.note = msg.err.Error(), ""
			}
			return nil, false
		}
		d.err, d.note, d.warns = "", msg.text, msg.warns
		switch {
		case msg.deleted != nil:
			d.undo = msg.deleted
		case msg.undone:
			d.undo = nil
		}
		// stay busy, showing the written list, until the reload lands: a key before that would
		// act on stale rules
		d.own, d.ownSet = msg.rules, true
		d.busy = "reloading …"
		cmd := m.reload()
		d.waitSeq = m.loadSeq
		return cmd, false
	case snapshotMsg:
		if d.waitSeq > 0 && m.snapSeq >= d.waitSeq {
			d.waitSeq, d.own, d.ownSet, d.busy = 0, nil, false, ""
			m.end(d.workKey())
		}
	case tea.KeyMsg:
		d.layout(m.width)
		return d.key(m, msg), false
	}
	return nil, false
}

func (d *rulesModal) key(m *Model, k tea.KeyMsg) tea.Cmd {
	if d.checking {
		switch k.String() {
		case "esc", "enter":
			d.checking = false
			d.check.Blur()
			d.paths.clear()
			return nil
		case "tab":
			d.paths.complete(&d.check)
			return nil
		}
		d.paths.clear()
		var cmd tea.Cmd
		d.check, cmd = d.check.Update(k)
		return cmd
	}
	if d.busy != "" {
		return nil
	}
	n, ok := d.node(m)
	var own []rules.Rule
	if ok {
		own = n.own()
	}
	d.sel = min(max(d.sel, 0), max(len(own)-1, 0))
	switch k.String() {
	case "j", "down":
		d.sel = min(d.sel+1, max(len(own)-1, 0))
		return nil
	case "k", "up":
		d.sel = max(d.sel-1, 0)
		return nil
	case "c":
		d.checking = true
		d.check.Focus()
		d.check.CursorEnd()
		return nil
	case "a", "d", "u":
	default:
		return nil
	}
	if !ok {
		d.err, d.note = d.what+" no longer exists", ""
		return nil
	}
	env, tg := m.opts.Env, d.tg
	switch k.String() {
	case "a":
		a := newRuleAdd(d, m.opts.Home)
		a.form.layout(m.width)
		m.openModal(a)
		return nil
	case "d":
		if len(own) == 0 {
			d.err, d.note = "no own rule to delete", ""
			return nil
		}
		i, r := d.sel, own[d.sel]
		expect := slices.Clone(own)
		rs := slices.Delete(slices.Clone(own), i, i+1)
		return d.write(m, "deleting the rule …", func() ruleSavedMsg {
			return ruleSavedMsg{text: fmt.Sprintf("deleted rule %d: %s %s (u undoes)", i+1, r.Mode, oneLine(r.Path)),
				err: actions.RuleSet(env, tg, expect, rs), rules: rs, deleted: &deletedRule{rule: r, index: i}}
		})
	default: // u
		if d.undo == nil {
			d.err, d.note = "", "nothing to undo"
			return nil
		}
		u := *d.undo
		if slices.Contains(own, u.rule) {
			d.undo = nil
			d.err, d.note = "", "nothing to undo: "+string(u.rule.Mode)+" "+oneLine(u.rule.Path)+" is already there"
			return nil
		}
		i := min(u.index, len(own))
		expect := slices.Clone(own)
		rs := slices.Insert(slices.Clone(own), i, u.rule)
		d.sel = i
		return d.write(m, "restoring the rule …", func() ruleSavedMsg {
			return ruleSavedMsg{text: fmt.Sprintf("restored rule %d: %s %s", i+1, u.rule.Mode, oneLine(u.rule.Path)),
				err: actions.RuleSet(env, tg, expect, rs), rules: rs, undone: true}
		})
	}
}

// write runs fn (an actions call) in a command as the dialog's busy work.
func (d *rulesModal) write(m *Model, busy string, fn func() ruleSavedMsg) tea.Cmd {
	if !m.begin(d.workKey(), busy) {
		return nil
	}
	d.busy = busy
	return func() tea.Msg {
		msg := fn()
		msg.d = d
		return msg
	}
}

func (d *rulesModal) view(m *Model, w, h int) string {
	bw, iw := d.dims(w)
	home := m.opts.Home
	title := "Rules of " + oneLine(d.what)
	n, ok := d.node(m)
	if !ok {
		lines := []string{badStyle.Render(fit("✗ "+oneLine(d.what)+" no longer exists", iw)), "", dimStyle.Render("esc closes")}
		return modalBox(title, lines[:min(len(lines), max(h-2, 1))], bw, cAccent)
	}
	own := n.own()
	sel := min(max(d.sel, 0), max(len(own)-1, 0))
	numW := width(itoa(len(own)))
	pathW := max(iw-2-numW-2-9, 1)
	ruleRow := func(num string, r rules.Rule) string {
		return padLeft(num, numW) + "  " + fit(string(r.Mode), 9) + ellipsizeMiddle(tildify(oneLine(r.Path), home), pathW)
	}

	head := []string{boldStyle.Render("Own rules")}
	var rows []string
	if len(own) == 0 {
		rows = append(rows, "  "+dimStyle.Render("no own rules"))
	}
	for i, r := range own {
		if i == sel && !d.checking {
			rows = append(rows, highlightRow(selStyle.Render("▸ ")+ruleRow(itoa(i+1), r), iw))
		} else {
			rows = append(rows, "  "+ruleRow(itoa(i+1), r))
		}
	}

	var rest []string
	if inh, from := n.inherited(); len(inh) > 0 {
		label := boldStyle.Render("inherited from " + oneLine(from))
		if len(own) > 0 {
			label += dimStyle.Render(" (overridden by the own rules)")
		}
		rest = append(rest, "", fit(label, iw))
		for _, r := range inh {
			rest = append(rest, "  "+dimStyle.Render(ruleRow("", r)))
		}
	}
	rest = append(rest, "")
	rest = append(rest, capLines(wrap(n.explain(home), iw), 3, accentStyle.Render)...)

	rest = append(rest, "")
	label := labelStyle.Render(fit("Check", checkLabelW))
	if d.checking {
		label = selStyle.Render(fit("Check", checkLabelW))
	}
	rest = append(rest, label+ansi.Truncate(d.check.View(), max(iw-checkLabelW, 1), ""))
	indent := strings.Repeat(" ", checkLabelW)
	if d.checking {
		for _, l := range d.paths.candLines(max(iw-checkLabelW, 1)) {
			rest = append(rest, indent+dimStyle.Render(l))
		}
	}
	if key := d.check.Value() + "\x00" + fmt.Sprint(n.levels); key != d.memo.key {
		active, text, valid := checkFolder(n, d.check.Value(), home)
		d.memo = checkMemo{key: key, active: active, valid: valid, text: text} // a cache, not state
	}
	active, text, valid := d.memo.active, d.memo.text, d.memo.valid
	mark, st := "✗ ", badStyle
	switch {
	case !valid:
		mark, st = "", dimStyle
	case active:
		mark, st = "✓ ", goodStyle
	}
	for _, l := range capLines(wrap(mark+text, max(iw-checkLabelW, 1)), 2, st.Render) {
		rest = append(rest, indent+l)
	}

	var foot []string
	for _, wn := range d.warns {
		foot = append(foot, capLines(wrap("! "+wn, iw), 2, warnStyle.Render)...)
	}
	switch {
	case d.busy != "":
		foot = append(foot, warnStyle.Render(fit(d.busy, iw)))
	case d.err != "":
		foot = append(foot, capLines(wrap("✗ "+d.err, iw), 2, badStyle.Render)...)
	case d.note != "":
		foot = append(foot, capLines(wrap(d.note, iw), 2, dimStyle.Render)...)
	}
	if len(foot) > 0 {
		rest = append(rest, "")
		rest = append(rest, foot...)
	}

	room := max(h-2, 1)
	if over := len(head) + len(rows) + len(rest) - room; over > 0 && len(rows) > 1 {
		keep := max(len(rows)-over, 1) // scroll the own rules, keeping the selected one
		start := min(max(sel-keep/2, 0), len(rows)-keep)
		rows = rows[start : start+keep]
	}
	lines := append(append(head, rows...), rest...)
	if len(lines) > room {
		lines = lines[:room]
	}
	return modalBox(title, lines, bw, cAccent)
}

// capLines styles lines with st and keeps at most n of them, the last one ending in "…" when cut.
func capLines(lines []string, n int, st func(...string) string) []string {
	if len(lines) > n {
		lines = append(lines[:n-1:n-1], lines[n-1]+" …")
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = st(l)
	}
	return out
}

// --- the add form -------------------------------------------------------------------------

// ruleAddModal asks for the mode and the folder of a new rule of its dialog's node.
type ruleAddModal struct {
	d    *rulesModal
	form *form
}

func newRuleAdd(d *rulesModal, home string) *ruleAddModal {
	f := newForm("Add a rule to " + oneLine(d.what))
	md := f.addChoice("mode", "Mode", ruleModes, string(rules.Tree))
	md.help = "only: this folder · tree: and below · not, not-tree: everywhere except"
	p := f.addPath("path", "Folder", "", "absolute folder (or ~/…)", home)
	p.help = "tab completes folders"
	return &ruleAddModal{d: d, form: f}
}

func (a *ruleAddModal) formOf() *form                  { return a.form }
func (a *ruleAddModal) view(m *Model, w, h int) string { return a.form.view(w, h) }
func (a *ruleAddModal) ownsEsc() bool                  { return a.form.busy != "" }
func (a *ruleAddModal) hints() []string {
	return []string{"ctrl+s", "add", "←→", "mode", "tab", "complete / next", "esc", "cancel"}
}

func (a *ruleAddModal) update(m *Model, msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if a.form.busy != "" {
			return nil, false
		}
		a.form.layout(m.width)
		cmd, submit := a.form.key(msg)
		if submit {
			return a.save(m), false
		}
		return cmd, false
	case ruleSavedMsg:
		if msg.add != a {
			return nil, false
		}
		a.form.busy = ""
		switch {
		case msg.err == nil:
			return nil, true
		case errors.Is(msg.err, actions.ErrNotFound):
			a.form.err = a.d.what + " no longer exists (esc discards the form)"
		default:
			a.form.err = msg.err.Error()
		}
	}
	return nil, false
}

// save validates the fields (inline errors, nothing written) and adds the rule in a command.
func (a *ruleAddModal) save(m *Model) tea.Cmd {
	f := a.form
	f.clearErrors()
	r, me, pe := parseRule(f.get("mode").value(), f.get("path").value())
	f.get("mode").err, f.get("path").err = me, pe
	if f.hasErrors() {
		f.focusFirstError()
		return nil
	}
	env, tg := m.opts.Env, a.d.tg
	var after []rules.Rule
	if n, ok := a.d.node(m); ok {
		after = append(slices.Clone(n.own()), r)
	}
	cmd := a.d.write(m, "adding the rule …", func() ruleSavedMsg {
		warns, err := actions.RuleAdd(env, tg, r)
		return ruleSavedMsg{add: a, text: "added rule " + string(r.Mode) + " " + oneLine(r.Path), warns: warns, err: err, rules: after}
	})
	if cmd != nil {
		f.busy = "adding …"
	}
	return cmd
}
