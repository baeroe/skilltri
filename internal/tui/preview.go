package tui

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/baeroe/skilltri/internal/daemon"
	"github.com/baeroe/skilltri/internal/rules"
)

// previewDebounce is how long the folder input rests before the preview is asked again.
const previewDebounce = 200 * time.Millisecond

// previewWork is the busy key of the which query.
const previewWork = "preview:which"

// previewCopyWork is the busy key of the clipboard copy.
const previewCopyWork = "preview:copy"

// previewDebounceMsg fires previewDebounce after an input change; only the newest one acts.
type previewDebounceMsg struct{ seq int }

// previewMsg is the result of a which query; seq tells stale results apart.
type previewMsg struct {
	seq     int
	reply   *daemon.WhichReply
	offline bool // decided from the files: no tool lists
	err     error
}

type pvKind int

const (
	pvBlank pvKind = iota
	pvHeader
	pvServer
	pvTool
	pvSkill
)

// pvRow is one line of the preview list; server, tool and skill rows can be selected.
type pvRow struct {
	kind   pvKind
	key    string
	text   string // header title
	label  string // server name, exposed tool name or skill ID
	server string // tool rows: their server
	from   string // skill rows: source / plugin
	state  string // server rows
	skill  *daemon.WhichSkill
	dec    rules.Decision
}

func (r pvRow) selectable() bool { return r.kind >= pvServer }

// previewTab shows what a session in a folder gets: servers with their tools and skills, each with
// the deciding reason.
type previewTab struct {
	input   textinput.Model
	paths   pathInput
	editing bool
	inSeq   int // newest input change (debounce)

	seq     int // newest issued query
	dir     string
	reply   *daemon.WhichReply
	offline bool
	err     error
	asked   string // snapshot key of the last query

	onlyActive bool
	rows       []pvRow
	sel        string
	idx        int
}

func newPreviewTab(cwd, home string) *previewTab {
	return &previewTab{input: newInput(tildify(cwd, home), "absolute folder (or ~/…)"), paths: pathInput{home: home}}
}

func (t *previewTab) title() string   { return "Preview" }
func (t *previewTab) capturing() bool { return t.editing }

func (t *previewTab) hints() []string {
	if t.editing {
		return []string{"tab", "complete", "enter", "done", "esc", "done"}
	}
	f := "only active"
	if t.onlyActive {
		f = "show all"
	}
	return []string{"/", "folder", "f", f, "enter", "jump to", "c", "copy env line"}
}

// --- query ---------------------------------------------------------------------------------

// folder is the normalized folder of the input; ok is false (with why) when it is not a path.
func (t *previewTab) folder(m *Model) (dir, why string) {
	v := strings.TrimSpace(t.input.Value())
	if !isRulePath(v) {
		return "", "enter an absolute folder (or ~/…)"
	}
	if v == "~" || strings.HasPrefix(v, "~/") {
		v = m.opts.Home + v[1:]
	}
	return rules.Normalize(v), ""
}

// snapKey changes when the data a query depends on changes: another snapshot, the daemon coming
// or going, other servers becoming ready.
func (t *previewTab) snapKey(m *Model) string {
	k := itoa(m.snapSeq) + "|"
	if m.snap.Status != nil {
		k += "daemon|" + readyKey(m.snap.Status)
	}
	return k
}

// start asks for the preview of the input's folder: the daemon when it runs, else (or when it does
// not answer) the files. An older query still in flight is superseded. passive marks a re-query
// the user did not ask for (the snapshot or the daemon status changed): see Model.beginPassive.
func (t *previewTab) start(m *Model, passive bool) tea.Cmd {
	t.asked = t.snapKey(m)
	dir, why := t.folder(m)
	t.seq++
	m.end(previewWork)
	if why != "" {
		t.dir, t.reply, t.err = "", nil, errors.New(why)
		t.rebuild()
		return nil
	}
	t.dir = dir
	text := "previewing " + ellipsizeMiddle(tildeDir(san(dir), m.opts.Home), 40) + " …"
	if passive {
		m.beginPassive(previewWork, text)
	} else {
		m.begin(previewWork, text)
	}
	seq, ctl, via := t.seq, m.opts.Control, m.snap.Status != nil
	cfg, cat := m.snap.Config, m.snap.Catalog
	return func() tea.Msg {
		if via {
			var r daemon.WhichReply
			if err := ctl(daemon.Hello{Cmd: "which", Args: []string{dir}}, &r); err == nil {
				return previewMsg{seq: seq, reply: &r}
			}
		}
		if cfg == nil {
			return previewMsg{seq: seq, err: errors.New("config not loaded")}
		}
		r := daemon.WhichOffline(cfg, cat, dir)
		return previewMsg{seq: seq, reply: &r, offline: true}
	}
}

// inputChanged schedules a query after the debounce (tests, with NoTick, send the message).
func (t *previewTab) inputChanged(m *Model) tea.Cmd {
	t.inSeq++
	if m.opts.NoTick {
		return nil
	}
	seq := t.inSeq
	return tea.Tick(previewDebounce, func(time.Time) tea.Msg { return previewDebounceMsg{seq: seq} })
}

// --- rows ----------------------------------------------------------------------------------

// rebuild derives the list rows from the reply and the filter and keeps the selection by key.
func (t *previewTab) rebuild() {
	t.rows = nil
	if t.reply != nil {
		t.rows = buildPreviewRows(*t.reply, t.onlyActive)
	}
	sel := -1
	first := -1
	for i, r := range t.rows {
		if !r.selectable() {
			continue
		}
		if first < 0 {
			first = i
		}
		if r.key == t.sel {
			sel = i
		}
	}
	if sel < 0 {
		sel = first
		if sel < 0 {
			t.sel, t.idx = "", 0
			return
		}
		t.sel = t.rows[sel].key
	}
	t.idx = sel
}

func buildPreviewRows(r daemon.WhichReply, onlyActive bool) []pvRow {
	var rows []pvRow
	servers, tools, toolsOn := 0, 0, 0
	for _, s := range r.Servers {
		if s.Decision.Active {
			servers++
			for _, tl := range s.Tools {
				tools++
				if tl.Decision.Active {
					toolsOn++
				}
			}
		}
	}
	title := "Servers " + itoa(servers) + "/" + itoa(len(r.Servers))
	if tools > 0 {
		title += " · tools " + itoa(toolsOn) + "/" + itoa(tools)
	}
	rows = append(rows, pvRow{kind: pvHeader, text: title})
	for _, s := range r.Servers {
		if onlyActive && !s.Decision.Active {
			continue
		}
		rows = append(rows, pvRow{kind: pvServer, key: "server/" + s.Name, label: s.Name, state: s.State, dec: s.Decision})
		if !s.Decision.Active {
			continue
		}
		for _, tl := range s.Tools {
			if onlyActive && !tl.Decision.Active {
				continue
			}
			rows = append(rows, pvRow{kind: pvTool, key: "tool/" + s.Name + "/" + tl.Name, label: tl.Exposed, server: s.Name, dec: tl.Decision})
		}
	}
	active := 0
	for _, s := range r.Skills {
		if s.Decision.Active {
			active++
		}
	}
	rows = append(rows, pvRow{kind: pvBlank}, pvRow{kind: pvHeader, text: "Skills " + itoa(active) + "/" + itoa(len(r.Skills))})
	for i := range r.Skills {
		s := &r.Skills[i]
		if onlyActive && !s.Decision.Active {
			continue
		}
		from := s.Source
		if s.Plugin != "" {
			from += " / " + s.Plugin
		}
		rows = append(rows, pvRow{kind: pvSkill, key: "skill/" + s.Source + "/" + s.ID, label: s.ID, from: from, skill: s, dec: s.Decision})
	}
	return rows
}

func (t *previewTab) selected() *pvRow {
	if t.sel == "" || t.idx >= len(t.rows) {
		return nil
	}
	return &t.rows[t.idx]
}

// move selects the next (dir 1) or previous (-1) selectable row.
func (t *previewTab) move(dir int) {
	for i := t.idx + dir; i >= 0 && i < len(t.rows); i += dir {
		if t.rows[i].selectable() {
			t.idx, t.sel = i, t.rows[i].key
			return
		}
	}
}

// --- update --------------------------------------------------------------------------------

func (t *previewTab) update(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case snapshotMsg, statusMsg:
		if t.asked != t.snapKey(m) {
			return t.start(m, true)
		}
	case previewDebounceMsg:
		if msg.seq == t.inSeq {
			return t.start(m, false)
		}
	case previewMsg:
		if msg.seq != t.seq {
			return nil // superseded by a newer query
		}
		m.end(previewWork)
		t.reply, t.offline, t.err = msg.reply, msg.offline, msg.err
		t.rebuild()
	case tea.KeyMsg:
		if t.editing {
			return t.editKey(m, msg)
		}
		return t.listKey(m, msg.String())
	}
	return nil
}

func (t *previewTab) editKey(m *Model, k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		t.stopEditing()
		return nil
	case "enter":
		t.stopEditing()
		t.inSeq++
		return t.start(m, false)
	case "tab":
		if t.paths.complete(&t.input) {
			return t.inputChanged(m)
		}
		return nil
	}
	t.paths.clear()
	before := t.input.Value()
	var cmd tea.Cmd
	t.input, cmd = t.input.Update(k)
	if t.input.Value() != before {
		return tea.Batch(cmd, t.inputChanged(m))
	}
	return cmd
}

func (t *previewTab) stopEditing() {
	t.editing = false
	t.input.Blur()
	t.paths.clear()
}

func (t *previewTab) listKey(m *Model, key string) tea.Cmd {
	switch key {
	case "j", "down":
		t.move(1)
	case "k", "up":
		t.move(-1)
	case "/", "e":
		t.editing = true
		t.input.Focus()
		t.input.CursorEnd()
	case "f":
		t.onlyActive = !t.onlyActive
		t.rebuild()
	case "c":
		return t.copyEnv(m)
	case "enter":
		t.jump(m)
	}
	return nil
}

// envLine is the shell line that applies the skill environment of dir.
func envLine(dir string) string {
	return `eval "$(skilltri env ` + shellQuote(dir) + ` --shell zsh)"`
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_./~+@:,=%-]+$`)

func shellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (t *previewTab) copyEnv(m *Model) tea.Cmd {
	dir, why := t.folder(m)
	if why != "" {
		m.flash(why, true)
		return nil
	}
	line, clip := envLine(dir), m.opts.Clipboard
	return m.act(previewCopyWork, "copying …", false, func() (string, error) {
		if err := clip(line); err != nil {
			return "", err
		}
		return "copied " + line, nil
	})
}

// jump opens the selected item in its tab.
func (t *previewTab) jump(m *Model) {
	r := t.selected()
	if r == nil {
		return
	}
	switch r.kind {
	case pvServer, pvTool:
		name := r.label
		if r.kind == pvTool {
			name = r.server
		}
		if m.tabs[TabServers].(*serversTab).selectServer(m, name) {
			m.switchTab(TabServers)
			return
		}
		m.flash("server "+oneLine(name)+" is not in the config", true)
	case pvSkill:
		if m.tabs[TabSkills].(*skillsTab).selectSkill(m, r.skill.Source, r.skill.Plugin, r.skill.ID) {
			m.switchTab(TabSkills)
			return
		}
		m.flash("skill "+oneLine(r.skill.ID)+" is not in the catalog", true)
	}
}

// --- view ----------------------------------------------------------------------------------

const pvLabelW = 8

func (t *previewTab) view(m *Model, w, h int) string {
	folder := t.folderPanel(m, w, h)
	fh := strings.Count(folder, "\n") + 1
	rest := max(h-fh, 0)
	if rest < 3 {
		return folder
	}
	if w >= wideMin {
		lw := listPaneWidth(w)
		return folder + "\n" + hjoin(
			panel("Preview", t.listLines(m, lw-4, rest-2), lw, rest, !t.editing, ""),
			panel("Details", t.detailLines(m, w-lw-4), w-lw, rest, false, ""))
	}
	dh := 0
	if rest >= 14 {
		dh = 7
	}
	out := folder + "\n" + panel("Preview", t.listLines(m, w-4, rest-dh-2), w, rest-dh, !t.editing, "")
	if dh > 0 {
		out += "\n" + panel("Details", t.detailLines(m, w-4), w, dh, false, "")
	}
	return out
}

// folderPanel is the folder input, the normalized folder and where the data comes from.
func (t *previewTab) folderPanel(m *Model, w, h int) string {
	iw := max(w-4, 1)
	t.input.Width = max(iw-pvLabelW-1, 1)
	label := labelStyle.Render(fit("Folder", pvLabelW))
	if t.editing {
		label = selStyle.Render(fit("Folder", pvLabelW))
	}
	lines := []string{label + t.input.View()}
	switch {
	case t.dir != "":
		src := ""
		switch {
		case t.reply == nil:
		case t.offline:
			src = "  " + warnStyle.Render("offline: no tool lists")
		default:
			src = "  " + dimStyle.Render("from the daemon")
		}
		shown := ellipsizeMiddle(tildeDir(san(t.dir), m.opts.Home), max(iw-pvLabelW-width(src), 8))
		lines = append(lines, labelStyle.Render(fit("", pvLabelW))+dimStyle.Render(shown)+src)
	default:
		if _, why := t.folder(m); why != "" {
			lines = append(lines, labelStyle.Render(fit("", pvLabelW))+warnStyle.Render(why))
		}
	}
	if t.editing && h >= 14 {
		for _, l := range t.paths.candLines(max(iw-pvLabelW, 1)) {
			lines = append(lines, strings.Repeat(" ", pvLabelW)+dimStyle.Render(l))
		}
	}
	return panel("Folder", lines, w, len(lines)+2, t.editing, "")
}

// listLines renders the rows around the selection into ih lines of iw cells.
func (t *previewTab) listLines(m *Model, iw, ih int) []string {
	if len(t.rows) == 0 {
		return t.emptyLines(m, iw)
	}
	top := min(max(t.idx-ih/2, 0), max(len(t.rows)-ih, 0))
	var out []string
	for i := top; i < len(t.rows) && len(out) < ih; i++ {
		r := t.rows[i]
		line := t.rowLine(m, r, iw)
		if i == t.idx {
			line = highlightRow(line, iw)
		}
		out = append(out, line)
	}
	return out
}

func (t *previewTab) emptyLines(m *Model, iw int) []string {
	switch {
	case m.busy(previewWork):
		return []string{dimStyle.Render("loading …")}
	case t.err != nil:
		var out []string
		for _, l := range wrap(t.err.Error(), iw) {
			out = append(out, badStyle.Render(l))
		}
		return out
	}
	return []string{dimStyle.Render("nothing to show")}
}

func (t *previewTab) rowLine(m *Model, r pvRow, iw int) string {
	switch r.kind {
	case pvBlank:
		return ""
	case pvHeader:
		return boldStyle.Render(oneLine(r.text))
	}
	icon := goodStyle.Render("✓")
	if !r.dec.Active {
		icon = badStyle.Render("✗")
	}
	prefix := icon + " "
	if r.kind == pvTool {
		prefix = "  " + prefix
	}
	avail := max(iw-width(prefix), 1)
	label := ellipsizeMiddle(oneLine(r.label), min(max(avail*3/5, 8), avail))
	line := prefix + label
	left := avail - width(label)
	add := func(s string, st func(string) string) {
		if s == "" || left < 4 {
			return
		}
		s = ellipsizeMiddle(s, left-2)
		line += "  " + st(s)
		left -= width(s) + 2
	}
	dim := func(s string) string { return dimStyle.Render(s) }
	switch r.kind {
	case pvServer:
		if r.state != "" && r.state != "unknown" {
			add(oneLine(r.state), dim)
		}
	case pvSkill:
		add(oneLine(r.from), dim)
	}
	if reason := t.reason(m, r); reason != "" && (!r.dec.Active || r.kind != pvTool) {
		add(reason, dim)
	}
	return line
}

// reason is the decision's reason on one line, with the home folder as ~.
func (t *previewTab) reason(m *Model, r pvRow) string {
	return tildeText(oneLine(r.dec.Reason), m.opts.Home)
}

// tildeText shows the home folder (as given and normalized: macOS temp folders are symlinks) as ~
// in text.
func tildeText(s, home string) string {
	if home == "" {
		return s
	}
	for _, h := range []string{rules.Normalize(home), home} {
		if h = strings.TrimSuffix(h, "/"); h != "" {
			s = strings.ReplaceAll(s, h+"/", "~/")
		}
	}
	return s
}

// tildeDir is tildify for a normalized folder.
func tildeDir(p, home string) string {
	if home == "" {
		return p
	}
	if q := tildify(p, rules.Normalize(home)); q != p {
		return q
	}
	return tildify(p, home)
}

// detailLines describe the selected row in full.
func (t *previewTab) detailLines(m *Model, iw int) []string {
	r := t.selected()
	if r == nil {
		return []string{dimStyle.Render("nothing selected")}
	}
	lw := 8
	val := func(s string) string { return ellipsizeMiddle(oneLine(s), max(iw-lw, 1)) }
	verdict := goodStyle.Render("✓ active")
	if !r.dec.Active {
		verdict = badStyle.Render("✗ inactive")
	}
	var out []string
	switch r.kind {
	case pvServer:
		out = append(out, kv("server", val(r.label), lw))
		if r.state != "" {
			out = append(out, kv("state", val(r.state), lw))
		}
	case pvTool:
		out = append(out, kv("tool", val(r.label), lw), kv("server", val(r.server), lw))
	case pvSkill:
		out = append(out, kv("skill", val(r.label), lw), kv("from", val(r.from), lw))
	}
	out = append(out, kv("result", verdict, lw))
	reason := t.reason(m, *r)
	if reason == "" {
		reason = "no reason given"
	}
	for i, l := range wrap(reason, max(iw-lw, 1)) {
		k := ""
		if i == 0 {
			k = "reason"
		}
		out = append(out, kv(k, l, lw))
	}
	return out
}
