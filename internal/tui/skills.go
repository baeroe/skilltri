package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/baeroe/skilltri/internal/actions"
	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/daemon"
	"github.com/baeroe/skilltri/internal/target"
)

// skillsTab is the source → plugin → skill tree with the plugin MCP servers; the details pane
// shows the selected node. Updates of the sources run in the background and are cancelled with esc.
type skillsTab struct {
	sel  string          // selected node key
	idx  int             // its row, to pick a neighbour when it disappears
	open map[string]bool // expanded state set by the user (else the default, see flattenTree)

	filtering bool
	filter    textinput.Model
	query     string

	upd    *updateRun // the running update (nil: none)
	updGen int        // generation of the last started update
	// stopping is the generation of a cancelled update that has not returned yet (0: none). Its
	// goroutine may still sync, build and save; no new update starts on the checkouts meanwhile.
	stopping int
	results  map[string]catalog.SourceState // last update result per source / remote plugin
}

// updateRun is a running source update.
type updateRun struct {
	gen       int
	cancel    context.CancelFunc
	viaDaemon bool
}

// updateWork is the busy key of the source update.
const updateWork = "skills:update"

// updateDoneMsg is the result of an update; gen tells a cancelled update's late result apart.
type updateDoneMsg struct {
	gen   int
	reply daemon.UpdateReply
	err   error
}

// editorDoneMsg reports that the editor on path returned.
type editorDoneMsg struct {
	path string
	err  error
}

func newSkillsTab() *skillsTab {
	f := newInput("", "filter")
	f.Prompt = "/"
	return &skillsTab{filter: f, open: map[string]bool{}, results: map[string]catalog.SourceState{}}
}

func (t *skillsTab) title() string   { return "Skills" }
func (t *skillsTab) capturing() bool { return t.filtering }

func (t *skillsTab) hints() []string {
	h := []string{"a", "add", "e", "edit", "d", "delete", "n", "new skill", "o", "open", "space", "on/off",
		"r", "rules", "u U", "update", "E", "enable server", "enter", "expand", "/", "filter"}
	if t.upd != nil {
		h = append([]string{"esc", "cancel update"}, h...)
	}
	return h
}

// closed cancels a running update (on quit).
func (t *skillsTab) closed(m *Model) {
	if t.upd != nil {
		t.upd.cancel()
		t.upd = nil
		m.end(updateWork)
	}
}

// --- data ----------------------------------------------------------------------------------

func (t *skillsTab) rows(m *Model) []treeRow {
	return flattenTree(buildTree(m.snap), t.open, t.query)
}

// resolve keeps the selection on its key; a vanished key selects the row at the same place.
func (t *skillsTab) resolve(m *Model) []treeRow {
	rows := t.rows(m)
	if i := slices.IndexFunc(rows, func(r treeRow) bool { return r.node.key == t.sel }); i >= 0 {
		t.idx = i
		return rows
	}
	if len(rows) == 0 {
		t.sel, t.idx = "", 0
		return rows
	}
	t.idx = min(max(t.idx, 0), len(rows)-1)
	t.sel = rows[t.idx].node.key
	return rows
}

// selected is the selected row (nil when none).
func (t *skillsTab) selected(m *Model) *treeRow {
	rows := t.resolve(m)
	if len(rows) == 0 {
		return nil
	}
	return &rows[t.idx]
}

// selectSkill selects the skill id of source (plugin: the marketplace entry, "" for none) and
// opens the nodes above it; false when the tree has no such skill.
func (t *skillsTab) selectSkill(m *Model, source, plugin, id string) bool {
	key := skillKey(source, id)
	var found bool
	var walk func(ns []*treeNode)
	walk = func(ns []*treeNode) {
		for _, n := range ns {
			found = found || n.key == key
			walk(n.children)
		}
	}
	walk(buildTree(m.snap))
	if !found {
		return false
	}
	t.query, t.filtering = "", false
	t.filter.SetValue("")
	t.filter.Blur()
	t.open[srcKey(source)] = true
	if plugin != "" {
		t.open[pluginKey(source, plugin)] = true
	}
	t.sel = key
	t.resolve(m)
	return true
}

// --- update --------------------------------------------------------------------------------

func (t *skillsTab) update(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case snapshotMsg:
		t.resolve(m)
	case updateDoneMsg:
		if msg.gen == t.stopping {
			t.stopping = 0
		}
		if t.upd == nil || msg.gen != t.upd.gen {
			return nil // cancelled: already reported
		}
		t.upd = nil
		m.end(updateWork)
		if msg.err != nil {
			m.flash("update: "+msg.err.Error(), true)
		} else {
			for _, st := range msg.reply.Sources {
				t.results[st.Name] = st
			}
			text, failed := updateSummary(msg.reply)
			m.flash(text, failed)
		}
		return m.reload()
	case editorDoneMsg:
		if msg.err != nil {
			m.flash("editor: "+msg.err.Error(), true)
		}
		return m.reload()
	case sourceSavedMsg:
		m.end(msg.f.workKey())
		if msg.err != nil {
			if !slices.Contains(m.modals, modal(msg.f)) { // the form was closed while saving
				m.flash("save source "+msg.name+": "+msg.err.Error(), true)
			}
			return nil
		}
		t.sel = srcKey(msg.name)
		verb := "saved source "
		if msg.add {
			verb = "added source "
		}
		m.flash(verb+msg.name, false)
		return m.reload()
	case tea.KeyMsg:
		if t.filtering {
			return t.filterKey(m, msg)
		}
		return t.listKey(m, msg.String())
	}
	return nil
}

// updateSummary is the status line text of an update and whether a source failed.
func updateSummary(r daemon.UpdateReply) (string, bool) {
	n := len(r.Sources)
	if n == 0 {
		return "nothing to update (no git or marketplace sources)", false
	}
	text := "updated " + plural(n, "source")
	var failed []catalog.SourceState
	for _, st := range r.Sources {
		if st.Error != "" {
			failed = append(failed, st)
		}
	}
	if len(failed) == 0 {
		return text, false
	}
	text += fmt.Sprintf(", %d failed: %s: %s", len(failed), failed[0].Name, failed[0].Error)
	if len(failed) > 1 {
		text += fmt.Sprintf(" (+%d more)", len(failed)-1)
	}
	return text, true
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return itoa(n) + " " + what + "s"
}

func (t *skillsTab) filterKey(m *Model, k tea.KeyMsg) tea.Cmd {
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
	if q := t.filter.Value(); q != t.query {
		t.query = q
		rows := t.resolve(m)
		// select the first match unless the selection is one
		if i := slices.IndexFunc(rows, func(r treeRow) bool { return r.match }); i >= 0 && !rows[t.idx].match {
			t.idx, t.sel = i, rows[i].node.key
		}
	}
	return cmd
}

func (t *skillsTab) listKey(m *Model, key string) tea.Cmd {
	rows := t.resolve(m)
	switch key {
	case "j", "down":
		if t.idx < len(rows)-1 {
			t.idx++
			t.sel = rows[t.idx].node.key
		}
		return nil
	case "k", "up":
		if t.idx > 0 {
			t.idx--
			t.sel = rows[t.idx].node.key
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
		switch {
		case t.upd != nil:
			t.cancelUpdate(m)
		case t.query != "":
			t.query = ""
			t.filter.SetValue("")
			t.resolve(m)
		}
		return nil
	}
	var r *treeRow
	if len(rows) > 0 {
		r = &rows[t.idx]
	}
	switch key {
	case "enter", "right", "l", "left", "h":
		if r != nil {
			t.expand(rows, r, key)
		}
		return nil
	case "U":
		return t.startUpdate(m, nil, "all sources")
	case "u":
		if r == nil {
			m.flash("no source selected (a adds one)", true)
			return nil
		}
		switch src := r.node.src; {
		case src.Type == "local":
			m.flash(r.node.source+" is a local folder: nothing to update", false)
		case src.Disabled:
			m.flash(r.node.source+" is disabled: enable it first (space)", true)
		default:
			return t.startUpdate(m, []string{r.node.source}, r.node.source)
		}
		return nil
	}
	if !needConfig(m) {
		return nil
	}
	switch key {
	case "a":
		sf := newSourceForm("", nil, m.opts.Home)
		sf.form.layout(m.width)
		m.openModal(sf)
		return nil
	case "n":
		return t.newSkill(m)
	}
	if r == nil {
		if strings.Contains(" e d o r E ", " "+key+" ") {
			m.flash("no source selected (a adds one)", true)
		}
		return nil
	}
	n := r.node
	switch key {
	case "e":
		sf := newSourceForm(n.source, n.src, m.opts.Home)
		sf.form.layout(m.width)
		m.openModal(sf)
	case "d":
		if n.kind != nodeSource {
			m.flash("select a source to remove (d removes the whole source)", true)
			return nil
		}
		name, env := n.source, m.opts.Env
		body := []string{"Remove source " + boldStyle.Render(oneLine(name)) + "?",
			dimStyle.Render("Its rules and plugin/skill settings go too; checkouts stay on disk.")}
		m.openModal(confirmDanger("Remove source", body, "remove", func(m *Model) tea.Cmd {
			return m.act(srcKey(name), "removing source "+name+" …", true, func() (string, error) {
				return "removed source " + name, actions.SourceRemove(env, name)
			})
		}))
	case "o":
		if n.kind != nodeSkill {
			m.flash("select a skill to open its SKILL.md", true)
			return nil
		}
		return t.edit(m, filepath.Join(n.skill.Skill.Dir, "SKILL.md"))
	case " ":
		return t.toggle(m, n)
	case "r":
		return t.rules(m, n)
	case "E":
		return t.enableServer(m, n)
	}
	return nil
}

// expand opens (enter toggles, right opens, left closes) the row; left on a closed or leaf row
// moves to its parent. Rows the filter forces open are not opened or closed (their state would flip
// unseen); left still moves to their parent.
func (t *skillsTab) expand(rows []treeRow, r *treeRow, key string) {
	hasKids := len(r.node.children) > 0
	switch {
	case (key == "left" || key == "h") && (!hasKids || !r.open || r.forced):
		for i := t.idx - 1; i >= 0; i-- {
			if rows[i].depth < r.depth {
				t.idx, t.sel = i, rows[i].node.key
				return
			}
		}
	case !hasKids, r.forced:
	case key == "enter":
		t.open[r.node.key] = !r.open
	case key == "right" || key == "l":
		t.open[r.node.key] = true
	default:
		t.open[r.node.key] = false
	}
}

// startUpdate syncs names (all when nil) in the background; what names them in the status line.
func (t *skillsTab) startUpdate(m *Model, names []string, what string) tea.Cmd {
	if t.upd != nil {
		m.flash("an update is already running (esc on the Skills tab cancels it)", true)
		return nil
	}
	if t.stopping != 0 {
		m.flash("previous update still stopping, try again in a moment", true)
		return nil
	}
	if !m.begin(updateWork, "updating "+what+" … (esc on Skills cancels)") {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.updGen++
	t.upd = &updateRun{gen: t.updGen, cancel: cancel, viaDaemon: m.snap.Status != nil}
	gen, via, upd := t.updGen, t.upd.viaDaemon, m.opts.Update
	return func() tea.Msg {
		defer cancel()
		r, err := upd(ctx, names, via)
		return updateDoneMsg{gen: gen, reply: r, err: err}
	}
}

// cancelUpdate cancels the running update; its late result is ignored, but no new update starts
// until it has arrived.
func (t *skillsTab) cancelUpdate(m *Model) {
	t.upd.cancel()
	t.stopping = t.upd.gen
	if t.upd.viaDaemon {
		m.flash("stopped waiting for the daemon (it finishes the update in the background)", false)
	} else {
		m.flash("update cancelled", false)
	}
	t.upd = nil
	m.end(updateWork)
}

// edit opens path in the editor (the TUI is suspended meanwhile); the snapshot reloads afterwards.
func (t *skillsTab) edit(m *Model, path string) tea.Cmd {
	return tea.ExecProcess(m.opts.Editor(path), func(err error) tea.Msg { return editorDoneMsg{path: path, err: err} })
}

// newSkill asks for a name, creates the skill in the local source and opens it in the editor.
func (t *skillsTab) newSkill(m *Model) tea.Cmd {
	env := m.opts.Env
	p := newNamePrompt("New local skill", "Name", "", "lowercase letters, digits, '-'", "creating the skill …")
	p.check = func(name string) string {
		if name == "" {
			return "enter a name"
		}
		return ""
	}
	p.run = func(name string) tea.Cmd {
		return func() tea.Msg {
			dir, err := actions.SkillNew(env, name)
			if err != nil {
				return promptDoneMsg{p: p, err: err}
			}
			return promptDoneMsg{p: p, then: func(m *Model) tea.Cmd {
				t.sel = skillKey("local", name)
				m.flash("created "+tildify(dir, m.opts.Home), false)
				return tea.Batch(m.reload(), t.edit(m, filepath.Join(dir, "SKILL.md")))
			}}
		}
	}
	p.form.layout(m.width)
	m.openModal(p)
	return nil
}

// rules opens the rules dialog of the node; a plugin server row opens that of the config server it
// was enabled as.
func (t *skillsTab) rules(m *Model, n *treeNode) tea.Cmd {
	switch n.kind {
	case nodeSource:
		return openRules(m, target.Target{Kind: "source", Source: n.source}, "source "+n.source)
	case nodePlugin:
		return openRules(m, target.Target{Kind: "plugin", Source: n.source, Plugin: n.plugin.Plugin},
			"plugin "+n.plugin.Plugin+" of "+n.source)
	case nodeSkill:
		return openRules(m, target.Target{Kind: "skill", Source: n.source, Skill: n.skill.ID},
			"skill "+n.skill.ID+" of "+n.source)
	}
	if n.as == "" {
		m.flash("enable the server first (E), then edit its rules", true)
		return nil
	}
	return openRules(m, target.Target{Kind: "server", Server: n.as}, "server "+n.as)
}

// toggle enables or disables the node (servers are switched on the Servers tab).
func (t *skillsTab) toggle(m *Model, n *treeNode) tea.Cmd {
	switch n.kind {
	case nodeSource:
		return toggleCmd(m, target.Target{Kind: "source", Source: n.source}, !n.src.Disabled, "source "+n.source, "")
	case nodePlugin:
		on := n.src.PluginEnabled(n.plugin.Plugin)
		return toggleCmd(m, target.Target{Kind: "plugin", Source: n.source, Plugin: n.plugin.Plugin}, on, "plugin "+n.plugin.Plugin, "")
	case nodeSkill:
		id := n.skill.ID
		node := n.src.Skills[id]
		return toggleCmd(m, target.Target{Kind: "skill", Source: n.source, Skill: id}, node == nil || !node.Disabled, "skill "+id, "")
	}
	if n.as != "" {
		m.flash("server "+n.as+" is switched on and off on the Servers tab", false)
	} else {
		m.flash("E enables server "+n.server.Name+" in the gateway", false)
	}
	return nil
}

// enableServer asks for the server name (prefilled with the default) and copies the plugin server
// into the config.
func (t *skillsTab) enableServer(m *Model, n *treeNode) tea.Cmd {
	if n.kind != nodeServer {
		m.flash("select a plugin server (E enables it in the gateway)", true)
		return nil
	}
	if n.as != "" {
		m.flash(n.server.Name+" is already enabled as "+n.as, false)
		return nil
	}
	env := m.opts.Env
	ps := n.server
	ref := ps.Source + "/" + ps.Plugin + "/" + ps.Name
	p := newNamePrompt("Enable "+oneLine(ps.Name)+" of "+oneLine(ps.Plugin), "Server name",
		actions.DefaultServerName(ps.Plugin, ps.Name), "letters, digits, '-'", "enabling "+ps.Name+" …")
	p.check = func(name string) string {
		switch {
		case name == config.ReservedName:
			return fmt.Sprintf("%q is reserved", config.ReservedName)
		case !config.ValidServerName(name):
			return "use letters, digits and '-' (max 32 characters, starting with a letter or digit)"
		}
		return ""
	}
	p.run = func(name string) tea.Cmd {
		return func() tea.Msg {
			got, err := actions.PluginServerEnable(env, ref, name)
			if errors.Is(err, actions.ErrExists) {
				err = fmt.Errorf("server %s already exists", name)
			}
			if err != nil {
				return promptDoneMsg{p: p, err: err}
			}
			return promptDoneMsg{p: p, then: func(m *Model) tea.Cmd {
				m.flash("enabled "+ps.Name+" as "+got, false)
				return tea.Batch(m.reload(), m.refreshStatus())
			}}
		}
	}
	p.form.layout(m.width)
	m.openModal(p)
	return nil
}

// selectSource selects the source name (dropping a filter that hides it); false when the config
// has no such source.
func (t *skillsTab) selectSource(m *Model, name string) bool {
	if m.snap.Config == nil || m.snap.Config.Sources[name] == nil {
		return false
	}
	t.query, t.filtering = "", false
	t.filter.SetValue("")
	t.filter.Blur()
	t.sel = srcKey(name)
	t.resolve(m)
	return true
}
