package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/rules"
)

// --- view ----------------------------------------------------------------------------------

func (t *skillsTab) view(m *Model, w, h int) string {
	rows := t.rows(m) // view changes nothing: the selection is resolved in update
	right := ""
	if c := m.snap.Catalog; c != nil && len(c.Skills) > 0 {
		right = dimStyle.Render(plural(len(c.Skills), "skill"))
	}
	var sel *treeRow
	for i := range rows {
		if rows[i].node.key == t.sel {
			sel = &rows[i]
		}
	}
	list := func(iw, ih int) []string { return t.listLines(m, rows, iw, ih) }
	dtitle := "Details"
	if sel != nil {
		dtitle = nodeTitle(sel.node)
	}
	if w >= wideMin {
		lw := listPaneWidth(w)
		return hjoin(panel("Skills", list(lw-4, h-2), lw, h, true, right),
			panel(dtitle, t.detailLines(m, sel, w-lw-4), w-lw, h, false, ""))
	}
	lh := min(max(len(rows)+2, 5), h)
	if h-lh < 8 {
		lh = min(max(h/2, 5), h)
	}
	if h-lh < 6 || sel == nil {
		return panel("Skills", list(w-4, h-2), w, h, true, right)
	}
	return panel("Skills", list(w-4, lh-2), w, lh, true, right) + "\n" +
		panel(dtitle, t.detailLines(m, sel, w-4), w, h-lh, false, "")
}

func nodeTitle(n *treeNode) string {
	switch n.kind {
	case nodePlugin:
		return "plugin " + oneLine(n.plugin.Plugin)
	case nodeServer:
		return "server " + oneLine(n.server.Name)
	case nodeSkill:
		return oneLine(n.skill.ID)
	}
	return "source " + oneLine(n.source)
}

func (t *skillsTab) listLines(m *Model, rows []treeRow, iw, ih int) []string {
	var lines []string
	if m.snap.Config == nil {
		if m.snap.ConfigErr != nil {
			for _, l := range wrap("config error: "+m.snap.ConfigErr.Error(), iw) {
				lines = append(lines, badStyle.Render(l))
			}
		} else {
			lines = append(lines, dimStyle.Render("loading …"))
		}
		return lines
	}
	filterLine := ""
	if t.filtering {
		filterLine = t.filter.View()
	} else if t.query != "" {
		filterLine = accentStyle.Render("/"+oneLine(t.query)) + dimStyle.Render("  esc clears")
	}
	avail := ih
	if filterLine != "" {
		avail -= 2
	}
	switch {
	case len(m.snap.Config.Sources) == 0:
		lines = append(lines, "", dimStyle.Render(" No skill sources yet."), "",
			" "+keyHints("a", "add a source", "n", "new local skill"))
	case len(rows) == 0:
		lines = append(lines, dimStyle.Render(" No match."))
	default:
		cur := 0
		for i, r := range rows {
			if r.node.key == t.sel {
				cur = i
			}
		}
		start := 0
		if h := max(avail, 1); cur >= h {
			start = cur - h + 1
		}
		for i := start; i < len(rows) && i < start+max(avail, 1); i++ {
			lines = append(lines, t.rowLine(m, rows[i], iw))
		}
	}
	if filterLine != "" {
		for len(lines) < ih-1 {
			lines = append(lines, "")
		}
		lines = append(lines[:max(ih-1, 0)], fit(filterLine, iw))
	}
	return lines
}

// serverState is the state of a plugin server node: available, enabled as <name> or needs review.
func serverState(m *Model, n *treeNode) string {
	switch {
	case n.as == "":
		return "available"
	case m.snap.Review[n.as] == "needs-review":
		return "needs review (Servers tab: A)"
	}
	return "enabled as " + oneLine(n.as)
}

// rowLine renders one tree row in iw cells.
func (t *skillsTab) rowLine(m *Model, r treeRow, iw int) string {
	n := r.node
	arrow := "  "
	if len(n.children) > 0 {
		arrow = "▸ "
		if r.open {
			arrow = "▾ "
		}
	}
	var name, mark, after, rest, errText string
	dim := false
	switch n.kind {
	case nodeSource:
		name = oneLine(n.source)
		parts := []string{n.src.Type}
		if n.src.Ref != "" {
			parts = append(parts, "@"+oneLine(n.src.Ref))
		}
		if st := n.state; st != nil {
			if st.Commit != "" {
				parts = append(parts, short(st.Commit))
			}
			if !st.FetchedAt.IsZero() {
				parts = append(parts, "fetched "+ago(st.FetchedAt, m.opts.Now()))
			}
			errText = st.Error
		}
		switch {
		case n.src.Disabled:
			parts = append(parts, "disabled")
			dim = true
		case n.src.Type == "marketplace":
			parts = append(parts, plural(len(n.children), "plugin"))
		default:
			parts = append(parts, plural(len(n.children), "skill"))
		}
		rest = strings.Join(parts, " · ")
	case nodePlugin:
		name = oneLine(n.plugin.Plugin)
		if n.plugin.Name != "" && n.plugin.Name != n.plugin.Plugin {
			after = " (" + oneLine(n.plugin.Name) + ")"
		}
		var parts []string
		switch {
		case !n.src.PluginEnabled(n.plugin.Plugin):
			parts, dim = append(parts, "disabled"), true
		case !n.plugin.Loaded:
			if n.state == nil || n.state.Error == "" {
				parts = append(parts, "not loaded")
			}
		default:
			k := 0
			for _, c := range n.children {
				if c.kind == nodeSkill {
					k++
				}
			}
			parts = append(parts, plural(k, "skill"))
		}
		if len(n.plugin.Extras) > 0 {
			parts = append(parts, strings.Join(n.plugin.Extras, " "))
		}
		if n.state != nil {
			errText = n.state.Error
		}
		rest = strings.Join(parts, " · ")
	case nodeServer:
		name = "server " + oneLine(n.server.Name)
		after = " — " + serverState(m, n)
	case nodeSkill:
		mark = goodStyle.Render("✓") + " "
		if sn := n.src.Skills[n.skill.ID]; sn != nil && sn.Disabled {
			mark = badStyle.Render("✗") + " "
		}
		name = oneLine(n.skill.ID)
		rest = oneLine(n.skill.Skill.Description)
	}
	selected := n.key == t.sel
	prefix := strings.Repeat("  ", r.depth) + arrow
	room := iw - 1 - width(prefix) - width(mark)
	head := name + after
	if width(head) > room {
		head = ellipsizeMiddle(head, max(room, 1))
	}
	nameStyled := head
	if selected && !dim {
		nameStyled = boldStyle.Render(head)
	}
	line := " " + prefix + mark + nameStyled
	room -= width(head) + 2
	if errText != "" && room >= 8 {
		e := "! " + oneLine(errText)
		ew := min(width(e), max(room/2, 8))
		if rest == "" {
			ew = min(width(e), room)
		}
		errText = fit(e, ew)
		room -= ew + 2
	} else {
		errText = ""
	}
	if rest != "" && room >= 6 {
		line += "  " + dimStyle.Render(fit(rest, min(width(rest), room)))
	}
	if errText != "" {
		line += "  " + badStyle.Render(errText)
	}
	if dim {
		line = dimStyle.Render(ansi.Strip(line))
	}
	if selected {
		return highlightRow(line, iw)
	}
	return line
}

// short is the first 7 characters of a commit.
func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

// --- details -------------------------------------------------------------------------------

const detailLabelW = 14

func (t *skillsTab) detailLines(m *Model, r *treeRow, dw int) []string {
	if r == nil {
		return []string{dimStyle.Render("nothing selected")}
	}
	switch r.node.kind {
	case nodePlugin:
		return t.pluginDetails(m, r.node, dw)
	case nodeServer:
		return serverDetails(m, r.node, dw)
	case nodeSkill:
		return skillDetails(m, r.node, dw)
	}
	return t.sourceDetails(m, r.node, dw)
}

// val ellipsizes a plain value to the value column.
func val(s string, dw int) string { return ellipsizeMiddle(oneLine(s), max(dw-detailLabelW, 1)) }

// warningsAbout are the catalog warnings of source that contain every one of subs.
func warningsAbout(c *catalog.Catalog, source string, subs ...string) []string {
	if c == nil {
		return nil
	}
	var out []string
	for _, w := range c.Warnings {
		rest, ok := strings.CutPrefix(w, source+": ")
		if !ok {
			continue
		}
		all := true
		for _, s := range subs {
			all = all && strings.Contains(rest, s)
		}
		if all {
			out = append(out, rest)
		}
	}
	return out
}

// textBlock is a bold title and the wrapped, styled lines of texts (nothing when texts is empty).
func textBlock(title string, texts []string, dw int, st func(...string) string) []string {
	if len(texts) == 0 {
		return nil
	}
	l := []string{"", boldStyle.Render(title)}
	for _, tx := range texts {
		for _, w := range wrap(tx, max(dw-2, 1)) {
			l = append(l, "  "+st(w))
		}
	}
	return l
}

// rulesBlock lists own rules, else the inherited ones (labelled with their level), else everywhere.
func rulesBlock(m *Model, own []rules.Rule, inherited []rules.Rule, from string, dw int) []string {
	l := []string{"", boldStyle.Render("Rules")}
	rs := own
	switch {
	case len(own) > 0:
	case len(inherited) > 0:
		rs = inherited
		l = append(l, "  "+dimStyle.Render("inherited from the "+from))
	default:
		return append(l, "  "+dimStyle.Render("everywhere (no rules)"))
	}
	for _, r := range rs {
		l = append(l, "  "+fit(string(r.Mode), 9)+ellipsizeMiddle(tildify(oneLine(r.Path), m.opts.Home), max(dw-11, 1)))
	}
	return l
}

func onOff(disabled bool) string {
	if disabled {
		return warnStyle.Render("○ disabled")
	}
	return goodStyle.Render("● enabled")
}

func (t *skillsTab) sourceDetails(m *Model, n *treeNode, dw int) []string {
	s, home := n.src, m.opts.Home
	head := onOff(s.Disabled)
	if n.state != nil && n.state.Error != "" {
		head += dimStyle.Render(" · ") + badStyle.Render("! error")
	}
	l := []string{fit(head, dw), "", kv("Type", s.Type, detailLabelW)}
	if s.Path != "" {
		l = append(l, kv("Path", val(tildify(s.Path, home), dw), detailLabelW))
	}
	if s.URL != "" {
		l = append(l, kv("URL", val(s.URL, dw), detailLabelW))
	}
	if s.Type != "local" {
		ref := s.Ref
		if ref == "" {
			ref = "default branch"
		}
		l = append(l, kv("Ref", val(ref, dw), detailLabelW))
		if s.Subpath != "" {
			l = append(l, kv("Subpath", val(s.Subpath, dw), detailLabelW))
		}
		iv := s.UpdateEvery
		switch iv {
		case "":
			iv = "1h (default)"
		case "0":
			iv = "manual only (u)"
		}
		l = append(l, kv("Interval", val(iv, dw), detailLabelW))
	}
	if s.Type == "marketplace" {
		pd := "enabled by default"
		if s.PluginsDefault == config.PluginsDisabled {
			pd = "disabled by default"
		}
		l = append(l, kv("Plugins", pd, detailLabelW))
	}
	if st := n.state; st != nil {
		if st.Commit != "" {
			l = append(l, kv("Commit", short(st.Commit), detailLabelW))
		}
		if !st.FetchedAt.IsZero() {
			l = append(l, kv("Fetched", ago(st.FetchedAt, m.opts.Now()), detailLabelW))
		}
	}
	if res, ok := t.results[n.source]; ok {
		r := goodStyle.Render("ok")
		if res.Error != "" {
			r = badStyle.Render("failed")
		}
		l = append(l, kv("Last update", r, detailLabelW))
	}
	if n.state != nil {
		l = append(l, textBlock("Last error", nonEmpty(n.state.Error), dw, badStyle.Render)...)
	}
	l = append(l, rulesBlock(m, s.Rules, nil, "", dw)...)
	return append(l, textBlock("Warnings", warningsAbout(m.snap.Catalog, n.source), dw, warnStyle.Render)...)
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

func (t *skillsTab) pluginDetails(m *Model, n *treeNode, dw int) []string {
	p, s := n.plugin, n.src
	on := s.PluginEnabled(p.Plugin)
	how := "set here"
	if s.Plugins[p.Plugin] == nil {
		how = "source default"
	}
	l := []string{fit(onOff(!on)+dimStyle.Render(" ("+how+")"), dw), ""}
	if p.Name != "" {
		l = append(l, kv("Name", val(p.Name, dw), detailLabelW))
	}
	if p.Dir != "" {
		l = append(l, kv("Dir", val(tildify(p.Dir, m.opts.Home), dw), detailLabelW))
	}
	if !p.Loaded {
		l = append(l, kv("Folder", dimStyle.Render("not read (see warnings)"), detailLabelW))
	}
	if st := n.state; st != nil && st.Commit != "" {
		l = append(l, kv("Commit", short(st.Commit), detailLabelW))
	}
	if len(p.Extras) > 0 {
		l = append(l, kv("Not delivered", val(strings.Join(p.Extras, ", "), dw), detailLabelW))
	}
	var servers []string
	for _, c := range n.children {
		if c.kind == nodeServer {
			servers = append(servers, fmt.Sprintf("%s — %s", c.server.Name, serverState(m, c)))
		}
	}
	if len(servers) > 0 {
		l = append(l, "", boldStyle.Render("MCP servers")+"  "+dimStyle.Render("E enables one"))
		for _, sv := range servers {
			l = append(l, "  "+ellipsizeMiddle(oneLine(sv), max(dw-2, 1)))
		}
	}
	if n.state != nil {
		l = append(l, textBlock("Last error", nonEmpty(n.state.Error), dw, badStyle.Render)...)
	}
	var own []rules.Rule
	if pn := s.Plugins[p.Plugin]; pn != nil {
		own = pn.Rules
	}
	l = append(l, rulesBlock(m, own, s.Rules, "source", dw)...)
	return append(l, textBlock("Warnings", warningsAbout(m.snap.Catalog, n.source, "plugin "+p.Plugin), dw, warnStyle.Render)...)
}

func serverDetails(m *Model, n *treeNode, dw int) []string {
	d := n.server.Def
	l := []string{fit(serverState(m, n), dw), ""}
	if d.Type == "http" {
		l = append(l, kv("URL", val(d.URL, dw), detailLabelW))
	} else {
		cmd := tildify(oneLine(d.Command), m.opts.Home)
		if len(d.Args) > 0 {
			cmd += " " + oneLine(joinArgs(d.Args))
		}
		l = append(l, kv("Command", val(cmd, dw), detailLabelW))
	}
	l = append(l, kv("Plugin", val(n.source+"/"+n.plugin.Plugin, dw), detailLabelW))
	if n.as == "" {
		l = append(l, "", dimStyle.Render(fit("E copies it into the gateway's servers.", dw)))
	}
	return l
}

func skillDetails(m *Model, n *treeNode, dw int) []string {
	e, s := n.skill, n.src
	sn := s.Skills[e.ID]
	l := []string{fit(onOff(sn != nil && sn.Disabled), dw), ""}
	if e.Skill.Name != e.ID {
		l = append(l, kv("Name", val(e.Skill.Name, dw), detailLabelW))
	}
	l = append(l, kv("Dir", val(tildify(e.Skill.Dir, m.opts.Home), dw), detailLabelW))
	if e.Plugin != "" {
		l = append(l, kv("Plugin", val(e.Plugin, dw), detailLabelW))
	}
	l = append(l, textBlock("Description", nonEmpty(e.Skill.Description), dw, func(s ...string) string { return strings.Join(s, " ") })...)
	var own, inherited []rules.Rule
	if sn != nil {
		own = sn.Rules
	}
	from := "source"
	if pn := s.Plugins[e.Plugin]; e.Plugin != "" && pn != nil && len(pn.Rules) > 0 {
		inherited, from = pn.Rules, "plugin"
	} else {
		inherited = s.Rules
	}
	l = append(l, rulesBlock(m, own, inherited, from, dw)...)
	warns := append([]string(nil), e.Skill.Warnings...)
	warns = append(warns, warningsAbout(m.snap.Catalog, n.source, e.Skill.Dir)...)
	return append(l, textBlock("Warnings", warns, dw, warnStyle.Render)...)
}
