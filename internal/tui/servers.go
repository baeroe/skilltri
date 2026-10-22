package tui

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/baeroe/skilltri/internal/actions"
	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/daemon"
	"github.com/baeroe/skilltri/internal/downstream"
	"github.com/baeroe/skilltri/internal/gateway"
	"github.com/baeroe/skilltri/internal/rules"
	"github.com/baeroe/skilltri/internal/target"
)

// serversTab lists the configured MCP servers with their daemon state; the details pane shows the
// definition, rules, tools (from the daemon's which for the Preview folder) and the last error.
type serversTab struct {
	sel string // selected server name
	idx int    // its row, to pick a neighbour when it disappears

	toolFocus bool
	toolSel   string // selected tool name while toolFocus

	filtering bool
	filter    textinput.Model
	query     string

	which    *daemon.WhichReply // the daemon's which for Options.Cwd (nil: none yet / offline)
	whichErr error
	whichKey string // ready servers the which reply was asked for
}

// whichWork is the busy key of the which request.
const whichWork = "servers:which"

// whichMsg is the result of the tab's which request.
type whichMsg struct {
	key   string
	reply *daemon.WhichReply
	err   error
}

func newServersTab() *serversTab {
	f := newInput("", "filter")
	f.Prompt = "/"
	return &serversTab{filter: f}
}

func (t *serversTab) title() string   { return "Servers" }
func (t *serversTab) capturing() bool { return t.filtering }

func (t *serversTab) hints() []string {
	if t.toolFocus {
		return []string{"↑↓", "tool", "space", "on/off", "esc", "back"}
	}
	return []string{"a", "add", "e", "edit", "d", "delete", "space", "on/off", "t", "tools", "x", "restart", "A", "accept", "/", "filter"}
}

// --- data ----------------------------------------------------------------------------------

// rows are the server names shown (filtered by the query).
func (t *serversTab) rows(m *Model) []string {
	names := m.snap.serverNames()
	if t.query == "" {
		return names
	}
	q := strings.ToLower(t.query)
	return slices.DeleteFunc(names, func(n string) bool {
		s := m.snap.Config.Servers[n]
		hay := n
		if s != nil {
			hay += "\x00" + s.Command + "\x00" + s.URL
			if s.Origin != nil {
				hay += "\x00" + s.Origin.Source + "/" + s.Origin.Plugin
			}
		}
		return !strings.Contains(strings.ToLower(hay), q)
	})
}

// resolve keeps the selection on its name; a vanished name selects the row at the same place.
func (t *serversTab) resolve(m *Model) []string {
	rows := t.rows(m)
	if i := slices.Index(rows, t.sel); i >= 0 {
		t.idx = i
		return rows
	}
	if len(rows) == 0 {
		t.sel, t.idx, t.toolFocus = "", 0, false
		return rows
	}
	t.idx = min(max(t.idx, 0), len(rows)-1)
	t.sel, t.toolFocus = rows[t.idx], false
	return rows
}

// server is the selected server's config (nil when none).
func (t *serversTab) server(m *Model) *config.Server {
	if m.snap.Config == nil || t.sel == "" {
		return nil
	}
	return m.snap.Config.Servers[t.sel]
}

// readyKey identifies the set of ready servers: the which reply is asked again when it changes.
func readyKey(st *daemon.StatusReply) string {
	var ready []string
	for _, s := range st.Servers {
		if s.State == "ready" {
			ready = append(ready, s.Name)
		}
	}
	return strings.Join(ready, "\x00")
}

// fetchWhich asks the daemon (in a command) which tools the servers offer in the Preview folder.
func (t *serversTab) fetchWhich(m *Model) tea.Cmd {
	if m.snap.Status == nil || !m.beginPassive(whichWork, "asking the daemon for tool lists …") {
		return nil
	}
	key, ctl, dir := readyKey(m.snap.Status), m.opts.Control, m.opts.Cwd
	return func() tea.Msg {
		var r daemon.WhichReply
		if err := ctl(daemon.Hello{Cmd: "which", Args: []string{dir}}, &r); err != nil {
			return whichMsg{key: key, err: err}
		}
		return whichMsg{key: key, reply: &r}
	}
}

// toolItem is one row of the tool list: a tool the daemon listed and/or one with config settings.
type toolItem struct {
	name     string
	listed   bool // listed by the daemon's which
	decision rules.Decision
	cfg      *config.Tool // nil: no settings
}

func (it toolItem) active() bool {
	if it.listed {
		return it.decision.Active
	}
	return it.cfg == nil || !it.cfg.Disabled
}

// toolItems are the daemon's tools of server name plus the configured tools it did not list.
func (t *serversTab) toolItems(m *Model, name string) []toolItem {
	var cfgTools map[string]*config.Tool
	if s := m.snap.Config.Servers[name]; s != nil {
		cfgTools = s.Tools
	}
	var out []toolItem
	seen := map[string]bool{}
	if m.snap.Status != nil && t.which != nil {
		for _, ws := range t.which.Servers {
			if ws.Name != name {
				continue
			}
			for _, wt := range ws.Tools {
				seen[wt.Name] = true
				out = append(out, toolItem{name: wt.Name, listed: true, decision: wt.Decision, cfg: cfgTools[wt.Name]})
			}
		}
	}
	for _, n := range slices.Sorted(maps.Keys(cfgTools)) {
		if !seen[n] && cfgTools[n] != nil {
			out = append(out, toolItem{name: n, cfg: cfgTools[n]})
		}
	}
	return out
}

// --- update --------------------------------------------------------------------------------

func (t *serversTab) update(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case snapshotMsg:
		t.resolve(m)
		if m.snap.Status == nil {
			t.which, t.whichErr = nil, nil
			return nil
		}
		return t.fetchWhich(m)
	case statusMsg:
		if m.snap.Status == nil {
			t.which, t.whichErr = nil, nil
			return nil
		}
		// asked again when the ready servers changed, and on every tick after a failure
		if readyKey(m.snap.Status) != t.whichKey || t.which == nil || t.whichErr != nil {
			return t.fetchWhich(m)
		}
	case whichMsg:
		m.end(whichWork)
		t.whichKey = msg.key
		if msg.err != nil {
			t.whichErr = msg.err
			return nil
		}
		t.which, t.whichErr = msg.reply, nil
	case secretStoredMsg:
		m.end(msg.f.workKey("secret"))
	case serverSavedMsg:
		m.end(msg.f.workKey("save"))
		if msg.err != nil {
			if !slices.Contains(m.modals, modal(msg.f)) { // the form was closed while saving
				m.flash("save "+msg.name+": "+msg.err.Error(), true)
			}
			return nil
		}
		t.sel = msg.name
		m.flash("saved "+msg.name, false)
		return m.reload()
	case tea.KeyMsg:
		if t.filtering {
			return t.filterKey(m, msg)
		}
		if t.toolFocus {
			return t.toolKey(m, msg.String())
		}
		return t.listKey(m, msg.String())
	}
	return nil
}

func (t *serversTab) filterKey(m *Model, k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "enter":
		t.filtering = false
		t.filter.Blur()
		return nil
	case "esc":
		t.filtering, t.query = false, ""
		t.filter.SetValue("")
		t.filter.Blur()
		t.resolve(m)
		return nil
	}
	var cmd tea.Cmd
	t.filter, cmd = t.filter.Update(k)
	t.query = t.filter.Value()
	t.resolve(m)
	return cmd
}

func (t *serversTab) listKey(m *Model, key string) tea.Cmd {
	rows := t.resolve(m)
	switch key {
	case "j", "down":
		if t.idx < len(rows)-1 {
			t.idx++
			t.sel = rows[t.idx]
		}
		return nil
	case "k", "up":
		if t.idx > 0 {
			t.idx--
			t.sel = rows[t.idx]
		}
		return nil
	case "/":
		t.filter.Width = max(listPaneWidth(m.width)-6, 1)
		t.filtering = true
		t.filter.SetValue(t.query)
		t.filter.CursorEnd()
		t.filter.Focus()
		return nil
	case "esc":
		if t.query != "" {
			t.query = ""
			t.filter.SetValue("")
			t.resolve(m)
		}
		return nil
	}
	if !needConfig(m) {
		return nil
	}
	if key == "a" {
		sf := newServerForm(t, "", nil, m.opts.Home)
		sf.form.layout(m.width)
		m.openModal(sf)
		return nil
	}
	s := t.server(m)
	if s == nil {
		if strings.Contains(" e d x A t enter ", " "+key+" ") {
			m.flash("no server selected (a adds one)", true)
		}
		return nil
	}
	name := t.sel
	switch key {
	case "e":
		sf := newServerForm(t, name, s, m.opts.Home)
		sf.form.layout(m.width)
		m.openModal(sf)
	case "d":
		body := []string{"Remove server " + boldStyle.Render(oneLine(name)) + "?",
			dimStyle.Render("Its rules and tool settings go too.")}
		m.openModal(confirmDanger("Remove server", body, "remove", func(m *Model) tea.Cmd {
			env := m.opts.Env
			return m.act("server:"+name, "removing "+name+" …", true, func() (string, error) {
				return "removed " + name, actions.ServerRemove(env, name)
			})
		}))
	case " ":
		return toggleCmd(m, target.Target{Kind: "server", Server: name}, !s.Disabled, name, "")
	case "t", "enter":
		items := t.toolItems(m, name)
		if len(items) == 0 {
			m.flash("no tools listed for "+name+t.toolsWhy(m, name), false)
			return nil
		}
		if !slices.ContainsFunc(items, func(it toolItem) bool { return it.name == t.toolSel }) {
			t.toolSel = items[0].name
		}
		t.toolFocus = true
	case "x":
		if m.snap.Status == nil {
			m.flash("the daemon is not running: nothing to restart", true)
			return nil
		}
		key, ctl := "restart:"+name, m.opts.Control
		if !m.begin(key, "restarting "+name+" …") {
			return nil
		}
		return func() tea.Msg {
			if err := ctl(daemon.Hello{Cmd: "restart", Args: []string{name}}, nil); err != nil {
				return actionMsg{key: key, err: fmt.Errorf("restart %s: %w", name, err)}
			}
			return actionMsg{key: key, text: "restarted " + name, status: true}
		}
	case "A":
		return t.accept(m, name, s)
	}
	return nil
}

func (t *serversTab) toolKey(m *Model, key string) tea.Cmd {
	items := t.toolItems(m, t.sel)
	i := slices.IndexFunc(items, func(it toolItem) bool { return it.name == t.toolSel })
	if len(items) == 0 {
		t.toolFocus = false
		return nil
	}
	if i < 0 {
		i = 0
		t.toolSel = items[0].name
	}
	switch key {
	case "esc", "t", "left", "h":
		t.toolFocus = false
	case "j", "down":
		t.toolSel = items[min(i+1, len(items)-1)].name
	case "k", "up":
		t.toolSel = items[max(i-1, 0)].name
	case " ", "enter":
		if !needConfig(m) {
			return nil
		}
		it := items[i]
		disabled := it.cfg == nil || !it.cfg.Disabled
		note := ""
		if dir, ok := t.rulesExclude(m, t.sel, it.name); ok {
			if disabled {
				note = " (rules also exclude it in " + dir + ")"
			} else {
				note = ", but rules exclude it in " + dir
			}
		}
		return toggleCmd(m, target.Target{Kind: "tool", Server: t.sel, Tool: it.name}, disabled, "tool "+it.name+" of "+t.sel, note)
	}
	return nil
}

// rulesExclude reports whether the rules alone (ignoring the server's and the tool's Disabled
// flags) exclude the listed tool in the which folder, and that folder (with ~).
func (t *serversTab) rulesExclude(m *Model, server, tool string) (string, bool) {
	if t.which == nil || m.snap.Config == nil || m.snap.Config.Servers[server] == nil {
		return "", false
	}
	cfg := *m.snap.Config
	srv := *cfg.Servers[server]
	srv.Disabled = false
	if tc := srv.Tools[tool]; tc != nil {
		cp := *tc
		cp.Disabled = false
		srv.Tools = maps.Clone(srv.Tools)
		srv.Tools[tool] = &cp
	}
	cfg.Servers = maps.Clone(cfg.Servers)
	cfg.Servers[server] = &srv
	if gateway.ToolDecision(&cfg, server, tool, t.which.Dir).Active {
		return "", false
	}
	return tildify(oneLine(t.which.Dir), m.opts.Home), true
}

// pluginDef is the catalog's current definition of the plugin server s was imported from.
func pluginDef(c *catalog.Catalog, o *config.Origin) (config.Server, bool) {
	if c == nil || o == nil {
		return config.Server{}, false
	}
	for _, ps := range c.Servers {
		if ps.Source == o.Source && ps.Plugin == o.Plugin && ps.Name == o.Server {
			return ps.Def, true
		}
	}
	return config.Server{}, false
}

// accept shows the old and the new definition of a plugin server waiting for review; y accepts.
func (t *serversTab) accept(m *Model, name string, s *config.Server) tea.Cmd {
	if st := m.snap.Review[name]; st != "needs-review" {
		why := "it was not imported from a plugin"
		if s.Origin != nil {
			why = "its plugin definition is " + st
		}
		m.flash(name+" is not waiting for review: "+why, true)
		return nil
	}
	def, _ := pluginDef(m.snap.Catalog, s.Origin)
	body := []string{"The plugin changed the definition of " + boldStyle.Render(oneLine(name)) + ":", ""}
	diff := defDiff(*s, def, m.opts.Home)
	if len(diff) == 0 {
		diff = []string{dimStyle.Render("Same connection fields; the plugin's code changed.")}
	}
	body = append(body, diff...)
	body = append(body, "", dimStyle.Render("Rules, tools and flags stay."))
	env := m.opts.Env
	m.openModal(&confirmModal{title: "Accept plugin server", body: body, yes: "accept", color: cYellow, run: func(m *Model) tea.Cmd {
		return m.act("server:"+name, "accepting "+name+" …", true, func() (string, error) {
			return "accepted the new plugin definition of " + name, actions.PluginServerAccept(env, name)
		})
	}})
	return nil
}

// defDiff lists the connection fields that differ between old and new: the old value with "-",
// the new one with "+". Old env/header values are masked unless they are secret references. The
// new ones are shown on purpose: they come from the plugin's files, and reviewing them is the point.
func defDiff(old, new config.Server, home string) []string {
	const lw = 10
	var out []string
	add := func(label, o, n string) {
		if o == n {
			return
		}
		out = append(out, kv(label, badStyle.Render("- "+o), lw), kv("", goodStyle.Render("+ "+n), lw))
	}
	add("type", old.Type, new.Type)
	add("command", tildify(oneLine(old.Command), home), tildify(oneLine(new.Command), home))
	add("args", oneLine(joinArgs(old.Args)), oneLine(joinArgs(new.Args)))
	add("cwd", tildify(oneLine(old.Cwd), home), tildify(oneLine(new.Cwd), home))
	add("url", oneLine(old.URL), oneLine(new.URL))
	pairs := func(label string, o, n map[string]string) {
		keys := slices.Sorted(maps.Keys(o))
		for k := range n {
			if _, ok := o[k]; !ok {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		for _, k := range keys {
			ov, inOld := o[k]
			nv, inNew := n[k]
			switch {
			case inOld && inNew && ov == nv:
			case !inNew:
				out = append(out, kv(label, badStyle.Render("- "+oneLine(k)+" = "+maskValue(ov)), lw))
			case !inOld:
				out = append(out, kv(label, goodStyle.Render("+ "+oneLine(k)+" = "+oneLine(nv)), lw))
			default:
				out = append(out, kv(label, badStyle.Render("- "+oneLine(k)+" = "+maskValue(ov)), lw),
					kv("", goodStyle.Render("+ "+oneLine(k)+" = "+oneLine(nv)), lw))
			}
		}
	}
	pairs("env", old.Env, new.Env)
	pairs("header", old.Headers, new.Headers)
	return out
}

// maskValue shows secret references and hides every literal value.
func maskValue(v string) string {
	if strings.Contains(v, "${secret:") {
		return oneLine(v)
	}
	return "•••"
}

// --- view ----------------------------------------------------------------------------------

func (t *serversTab) view(m *Model, w, h int) string {
	rows := t.rows(m) // view changes nothing: the selection is resolved in update
	title := "Servers"
	right := ""
	if m.snap.Config != nil {
		if n := len(m.snap.Config.Servers); t.query != "" {
			right = dimStyle.Render(fmt.Sprintf("%d of %d", len(rows), n))
		} else if n > 0 {
			right = dimStyle.Render(itoa(n))
		}
	}
	list := func(iw, ih int) []string { return t.listLines(m, rows, iw, ih) }
	dtitle := "Details"
	if t.sel != "" {
		dtitle = oneLine(t.sel)
	}
	if w >= wideMin {
		lw := listPaneWidth(w)
		return hjoin(panel(title, list(lw-4, h-2), lw, h, !t.toolFocus, right),
			panel(dtitle, t.detailLines(m, w-lw-4), w-lw, h, t.toolFocus, ""))
	}
	// narrow: list on top, details below when there is room
	lh := min(max(len(rows)+2, 5), h)
	if h-lh < 8 {
		lh = min(max(h/2, 5), h)
	}
	if h-lh < 6 || t.sel == "" {
		if t.toolFocus { // no room for both: the details take the screen
			return panel(dtitle, t.detailLines(m, w-4), w, h, true, "")
		}
		return panel(title, list(w-4, h-2), w, h, true, right)
	}
	return panel(title, list(w-4, lh-2), w, lh, !t.toolFocus, right) + "\n" +
		panel(dtitle, t.detailLines(m, w-4), w, h-lh, t.toolFocus, "")
}

// listPaneWidth is the width of the list pane on a terminal w wide (all of it below wideMin).
func listPaneWidth(w int) int {
	if w >= wideMin {
		return min(max(w*9/20, 44), 90)
	}
	return w
}

// icon is the list icon of server name: ? needs review, ! error, ● ready, ○ otherwise.
func (t *serversTab) icon(m *Model, name string, s *config.Server) string {
	st := m.snap.statusOf(name)
	switch {
	case m.snap.Review[name] == "needs-review":
		return warnStyle.Render("?")
	case s.Disabled:
		return dimStyle.Render("○")
	case st != nil && st.State == "error":
		return badStyle.Render("!")
	case st != nil && st.State == "ready":
		return goodStyle.Render("●")
	case st != nil && st.State == "starting":
		return warnStyle.Render("○")
	}
	return dimStyle.Render("○")
}

// rulesSummary is "everywhere", "<mode> <path>" or "<mode> <path> +N rules".
func rulesSummary(rs []rules.Rule, home string) string {
	if len(rs) == 0 {
		return "everywhere"
	}
	s := string(rs[0].Mode) + " " + tildify(oneLine(rs[0].Path), home)
	switch n := len(rs) - 1; {
	case n == 1:
		s += " +1 rule"
	case n > 1:
		s += fmt.Sprintf(" +%d rules", n)
	}
	return s
}

func (t *serversTab) listLines(m *Model, rows []string, iw, ih int) []string {
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
	case len(m.snap.Config.Servers) == 0:
		lines = append(lines, "", dimStyle.Render(" No servers yet."), "", " "+keyHints("a", "add a server"))
	case len(rows) == 0:
		lines = append(lines, dimStyle.Render(" No match."))
	default:
		lines = append(lines, t.rowLines(m, rows, iw, max(avail, 1))...)
	}
	if filterLine != "" {
		for len(lines) < ih-1 {
			lines = append(lines, "")
		}
		lines = append(lines[:max(ih-1, 0)], fit(filterLine, iw))
	}
	return lines
}

func (t *serversTab) rowLines(m *Model, rows []string, iw, h int) []string {
	nameW := 4
	for _, n := range rows {
		nameW = max(nameW, width(oneLine(n)))
	}
	nameW = min(nameW, max(iw/3, 10), 32)
	toolsOf := func(st *downstream.ServerStatus) string {
		if st != nil && st.State == "ready" {
			return itoa(st.Tools) + " tools"
		}
		return ""
	}
	sessOf := func(st *downstream.ServerStatus) string {
		if st != nil && st.Sessions > 0 {
			return itoa(st.Sessions) + " sess"
		}
		return ""
	}
	toolsW, sessW := 0, 0 // the columns are as wide as their widest cell (0: not shown)
	for _, n := range rows {
		st := m.snap.statusOf(n)
		toolsW, sessW = max(toolsW, width(toolsOf(st))), max(sessW, width(sessOf(st)))
	}
	if iw < 40 {
		toolsW = 0
	}
	if iw < 52 {
		sessW = 0
	}
	var lines []string
	for _, n := range rows {
		s := m.snap.Config.Servers[n]
		if s == nil {
			continue
		}
		st := m.snap.statusOf(n)
		name := fit(oneLine(n), nameW)
		if n == t.sel {
			name = boldStyle.Render(name)
		}
		row := " " + t.icon(m, n, s) + " " + name + " " + dimStyle.Render(fit(s.Type, 5))
		if toolsW > 0 {
			row += " " + padLeft(toolsOf(st), toolsW)
		}
		if sessW > 0 {
			row += " " + padLeft(sessOf(st), sessW)
		}
		rest := rulesSummary(s.Rules, m.opts.Home)
		if s.Disabled {
			rest = "off · " + rest
		}
		origin := ""
		if o := s.Origin; o != nil {
			origin = " (plugin " + oneLine(o.Source) + "/" + oneLine(o.Plugin) + ")"
		}
		switch room := iw - width(row) - 1; {
		case room >= width(rest)+width(origin):
			row += " " + dimStyle.Render(rest+origin)
		case room-width(origin) >= 12:
			row += " " + dimStyle.Render(ellipsizeMiddle(rest, room-width(origin))+origin)
		case room >= 6:
			row += " " + dimStyle.Render(fit(rest+origin, room))
		}
		if n == t.sel {
			if !t.toolFocus {
				row = highlightRow(row, iw)
			} else {
				row = selStyle.Render("▸") + strings.TrimPrefix(row, " ")
			}
		}
		lines = append(lines, row)
	}
	if len(lines) > h {
		start := 0
		if cur := slices.Index(rows, t.sel); cur >= h {
			start = cur - h + 1
		}
		lines = lines[start:min(start+h, len(lines))]
	}
	return lines
}

// toolsWhy says why server name has no tool list (" (…)", or "" when it has one).
func (t *serversTab) toolsWhy(m *Model, name string) string {
	s := m.snap.Config.Servers[name]
	st := m.snap.statusOf(name)
	switch {
	case m.snap.Status == nil:
		return " (daemon not running)"
	case t.whichErr != nil:
		return " (" + oneLine(t.whichErr.Error()) + ")"
	case t.which == nil:
		return " (asking the daemon …)"
	case s != nil && s.PerSession:
		return " (per-session server)"
	case s != nil && s.Disabled:
		return " (server disabled)"
	case st == nil || st.State != "ready":
		return " (server not ready)"
	}
	return ""
}

func (t *serversTab) detailLines(m *Model, dw int) []string {
	s := t.server(m)
	if s == nil {
		return []string{dimStyle.Render("no server selected")}
	}
	const lw = 12
	vw := max(dw-lw, 1)
	home := m.opts.Home
	name := t.sel
	st := m.snap.statusOf(name)

	head := []string{}
	switch {
	case m.snap.Status == nil:
		head = append(head, dimStyle.Render("○ daemon not running"))
	case st == nil || st.State == "stopped":
		head = append(head, dimStyle.Render("○ stopped"))
	case st.State == "ready":
		head = append(head, goodStyle.Render("● ready"))
	case st.State == "error":
		head = append(head, badStyle.Render("! error"))
	default:
		head = append(head, warnStyle.Render("○ "+oneLine(st.State)))
	}
	if s.Disabled {
		head = append(head, warnStyle.Render("disabled"))
	}
	if m.snap.Review[name] == "needs-review" {
		head = append(head, warnStyle.Render("? needs review (A)"))
	}
	l := []string{fit(strings.Join(head, dimStyle.Render(" · ")), dw), ""}

	if s.Type == "http" {
		l = append(l, kv("URL", ellipsizeMiddle(oneLine(s.URL), vw), lw))
	} else {
		cmd := tildify(oneLine(s.Command), home)
		if len(s.Args) > 0 {
			cmd += " " + oneLine(joinArgs(s.Args))
		}
		l = append(l, kv("Command", ellipsizeMiddle(cmd, vw), lw))
		if s.Cwd != "" {
			l = append(l, kv("Cwd", ellipsizeMiddle(tildify(oneLine(s.Cwd), home), vw), lw))
		}
	}
	to := "60000 ms (default)"
	if s.RequestTimeoutMs > 0 {
		to = itoa(s.RequestTimeoutMs) + " ms"
	}
	l = append(l, kv("Timeout", to, lw))
	if s.PerSession {
		l = append(l, kv("Per session", "yes (one process per session)", lw))
	}
	pairs := func(label string, mp map[string]string) {
		for i, k := range slices.Sorted(maps.Keys(mp)) {
			lab := ""
			if i == 0 {
				lab = label
			}
			l = append(l, kv(lab, ellipsizeMiddle(oneLine(k)+" = "+maskValue(mp[k]), vw), lw))
		}
	}
	pairs("Env", s.Env)
	pairs("Headers", s.Headers)
	if o := s.Origin; o != nil {
		ref := oneLine(o.Source + "/" + o.Plugin + "/" + o.Server)
		state := m.snap.Review[name]
		if state == "needs-review" {
			state = "needs review: A shows the change"
		}
		l = append(l, kv("Plugin", ellipsizeMiddle(ref, vw), lw), kv("", dimStyle.Render(ellipsizeMiddle(oneLine(state), vw)), lw))
	}

	l = append(l, "", boldStyle.Render("Rules"))
	if len(s.Rules) == 0 {
		l = append(l, "  "+dimStyle.Render("everywhere (no rules)"))
	}
	for _, r := range s.Rules {
		l = append(l, "  "+fit(string(r.Mode), 9)+ellipsizeMiddle(tildify(oneLine(r.Path), home), max(dw-11, 1)))
	}

	items := t.toolItems(m, name)
	hint := ""
	if !t.toolFocus && len(items) > 0 {
		hint = "t select · space on/off"
	}
	l = append(l, "", boldStyle.Render("Tools")+"  "+dimStyle.Render(hint))
	if why := t.toolsWhy(m, name); why != "" {
		l = append(l, "  "+dimStyle.Render(fit("tools unavailable"+why, max(dw-2, 1))))
	}
	for _, it := range items {
		mark := goodStyle.Render("✓")
		if !it.active() {
			mark = badStyle.Render("✗")
		}
		own := ""
		switch {
		case it.cfg != nil && it.cfg.Disabled:
			own = "disabled"
		case it.cfg != nil && len(it.cfg.Rules) > 0:
			own = rulesSummary(it.cfg.Rules, home)
		case it.listed && !it.decision.Active:
			own = it.decision.Reason
		}
		nw := min(width(oneLine(it.name)), max(dw/2, 8))
		row := mark + " " + fit(oneLine(it.name), nw)
		if room := dw - 4 - width(row) - 2; own != "" && room >= 6 {
			row += "  " + dimStyle.Render(ellipsizeMiddle(oneLine(own), room))
		}
		if t.toolFocus && it.name == t.toolSel {
			l = append(l, highlightRow(selStyle.Render("▸ ")+row, dw))
		} else {
			l = append(l, "  "+row)
		}
	}

	if st != nil && (st.Error != "" || len(st.StderrTail) > 0) {
		l = append(l, "", boldStyle.Render("Last error"))
		if st.Error != "" {
			for _, e := range wrap(st.Error, max(dw-2, 1)) {
				l = append(l, "  "+badStyle.Render(e))
			}
		}
		for _, e := range st.StderrTail {
			l = append(l, "  "+dimStyle.Render(ellipsizeMiddle(oneLine(e), max(dw-2, 1))))
		}
	}
	return l
}
