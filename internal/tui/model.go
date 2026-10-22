// Package tui is the skilltri terminal UI (Bubble Tea): servers, skills, a folder preview, harness
// setup and doctor, on top of an asynchronously loaded snapshot of config, catalog and daemon status.
//
// Structure (later tabs and dialogs plug into it):
//
//   - Each tab is a tabModel (see below) with its own state, keys, view and hints. The Model routes
//     every key the global handler does not take to the active tab, and every other message
//     (snapshotMsg, statusMsg, results of the tab's own commands, …) to all tabs, after the Model
//     applied it. A tab keeps its selection by key (e.g. the selected server name), not by index,
//     and re-resolves it when it sees a snapshotMsg: reloads never reset it.
//   - Dialogs are modals on a stack (Model.openModal); only the top one gets keys. esc closes the top
//     modal (unless it implements escOwner), q quits only when no modal is open, ctrl+c always quits.
//     Non-key messages go to every open modal, so a form under a confirmation still gets its results.
//   - Nothing in Update blocks: loading, daemon calls and other work run in tea.Cmds; results come
//     back as messages. Action results are reported with Model.flash in the footer's status line.
package tui

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/baeroe/skilltri/internal/actions"
	"github.com/baeroe/skilltri/internal/daemon"
)

// Tab identifies a tab.
type Tab int

const (
	TabServers Tab = iota
	TabSkills
	TabPreview
	TabSetup
	TabDoctor
	numTabs
)

var tabNames = []string{"Servers", "Skills", "Preview", "Setup", "Doctor"}

// ParseTab maps a tab name (as for `skilltri --tab NAME`, case-insensitive) to its Tab.
func ParseTab(name string) (Tab, bool) {
	for i, n := range tabNames {
		if strings.EqualFold(n, name) {
			return Tab(i), true
		}
	}
	return 0, false
}

// statusTTL is how long a flash message stays in the status line.
const statusTTL = 5 * time.Second

// Options configure the model; tests inject fakes.
type Options struct {
	Tab     Tab
	Env     actions.Env
	Home    string                                // for Setup/Doctor (default os.UserHomeDir)
	Bin     string                                // absolute skilltri path for snippets (default os.Executable)
	Cwd     string                                // initial Preview folder (default os.Getwd)
	NoTick  bool                                  // tests drive ticks
	Now     func() time.Time                      // default time.Now
	Control func(h daemon.Hello, reply any) error // default daemon.ControlTimeout with 2 s
	Editor  func(path string) *exec.Cmd           // default $VISUAL/$EDITOR/vi
}

// tabModel is one tab. update gets the keys the Model does not handle globally and every non-key
// message; it returns follow-up work as a command. view renders the body in exactly w x h cells
// (lines are clamped, but a tab must not rely on that for its layout). hints are key/description
// pairs for the footer ("a", "add", "d", "delete"); the global ones are appended by the Model.
type tabModel interface {
	title() string
	update(m *Model, msg tea.Msg) tea.Cmd
	view(m *Model, w, h int) string
	hints() []string
}

// inputCapturer is implemented by tabs with a text input (the list filter): while capturing is
// true the tab gets every key, including q, ?, tab and digits (ctrl+c still quits).
type inputCapturer interface{ capturing() bool }

// modal is a dialog on top of the tabs. update gets the keys while it is the top modal (esc
// excepted, see escOwner) and every non-key message; closed=true removes it from the stack. view
// renders the box (at most w x h), which is centered over the dimmed body. Modals must be pointer
// types: the stack removes them by identity.
type modal interface {
	update(m *Model, msg tea.Msg) (tea.Cmd, bool)
	view(m *Model, w, h int) string
}

// modalHinter is implemented by modals that show their own footer hints (pairs as in tabModel).
type modalHinter interface{ hints() []string }

// escOwner is implemented by modals that handle esc themselves (e.g. to leave a sub-mode first);
// the others are closed by esc without seeing it.
type escOwner interface{ ownsEsc() bool }

// Model is the root Bubble Tea model.
type Model struct {
	opts   Options
	tab    Tab
	tabs   [numTabs]tabModel
	modals []modal
	width  int
	height int

	snap       Snapshot
	stamps     fileStamps
	seq        int  // last issued load/status sequence number
	snapSeq    int  // seq of the applied snapshot
	statusSeq  int  // seq of the applied daemon status (from a snapshot or a status refresh)
	loading    bool // a full snapshot load is in flight
	statusBusy bool // a status refresh is in flight

	status    string
	statusErr bool
	statusAt  time.Time

	quitting bool
}

// New creates the model; Init starts the first snapshot load.
func New(o Options) *Model {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Home == "" {
		o.Home, _ = os.UserHomeDir()
	}
	if o.Bin == "" {
		o.Bin, _ = os.Executable()
	}
	if o.Cwd == "" {
		o.Cwd, _ = os.Getwd()
	}
	if o.Control == nil {
		sock := o.socket()
		o.Control = func(h daemon.Hello, reply any) error {
			return daemon.ControlTimeout(sock, h, reply, controlTimeout)
		}
	}
	if o.Editor == nil {
		o.Editor = defaultEditor
	}
	if o.Tab < 0 || o.Tab >= numTabs {
		o.Tab = TabServers
	}
	m := &Model{opts: o, tab: o.Tab}
	for t := Tab(0); t < numTabs; t++ {
		m.tabs[t] = &placeholderTab{tab: t}
	}
	return m
}

// defaultEditor runs $VISUAL, else $EDITOR (which may carry arguments), else vi on path.
func defaultEditor(path string) *exec.Cmd {
	ed := os.Getenv("VISUAL")
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	f := strings.Fields(ed)
	if len(f) == 0 {
		f = []string{"vi"}
	}
	return exec.Command(f[0], append(f[1:], path)...)
}

// Init loads the first snapshot and starts the refresh ticker.
func (m *Model) Init() tea.Cmd {
	if m.opts.NoTick {
		return m.reload()
	}
	return tea.Batch(m.reload(), tick())
}

// reload starts a full snapshot load.
func (m *Model) reload() tea.Cmd {
	m.seq++
	seq, load := m.seq, loadSnapshot(m.opts)
	m.loading = true
	return func() tea.Msg {
		msg := load().(snapshotMsg)
		msg.seq = seq
		return msg
	}
}

// refreshStatus starts a status-only refresh (skipped while one is in flight).
func (m *Model) refreshStatus() tea.Cmd {
	if m.statusBusy {
		return nil
	}
	m.seq++
	m.statusBusy = true
	seq, load := m.seq, loadStatus(m.opts)
	return func() tea.Msg {
		msg := load().(statusMsg)
		msg.seq = seq
		return msg
	}
}

// flash shows text in the status line; it clears itself after statusTTL.
func (m *Model) flash(text string, isErr bool) {
	m.status, m.statusErr, m.statusAt = oneLine(text), isErr, m.opts.Now()
}

// statusText is the current status line text ("" when none or expired).
func (m *Model) statusText() string {
	if m.status == "" || m.opts.Now().Sub(m.statusAt) >= statusTTL {
		return ""
	}
	return m.status
}

func (m *Model) openModal(md modal) { m.modals = append(m.modals, md) }

func (m *Model) removeModal(md modal) {
	for i := len(m.modals) - 1; i >= 0; i-- {
		if m.modals[i] == md {
			m.modals = append(m.modals[:i], m.modals[i+1:]...)
			return
		}
	}
}

func (m *Model) switchTab(t Tab) { m.tab = (t + numTabs) % numTabs }

func (m *Model) quit() tea.Cmd {
	m.quitting = true
	return tea.Quit
}

// Update handles messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		return m, m.handleKey(msg)
	case tickMsg:
		if m.statusText() == "" {
			m.status = ""
		}
		cmds := []tea.Cmd{m.refreshStatus()}
		if !m.opts.NoTick {
			cmds = append(cmds, tick())
		}
		return m, tea.Batch(cmds...)
	case snapshotMsg:
		if msg.seq < m.snapSeq {
			return m, nil // an older load finished after a newer one
		}
		if msg.seq == m.seq {
			m.loading = false
		}
		st, derr, sseq := m.snap.Status, m.snap.DaemonErr, m.statusSeq
		m.snap, m.stamps, m.snapSeq = msg.snap, msg.stamps, msg.seq
		if msg.seq < sseq { // a newer status refresh already arrived: keep it
			m.snap.Status, m.snap.DaemonErr = st, derr
		} else {
			m.statusSeq = msg.seq
		}
	case statusMsg:
		m.statusBusy = false
		if msg.seq <= m.statusSeq {
			return m, nil
		}
		m.statusSeq = msg.seq
		m.snap.Status, m.snap.DaemonErr = msg.status, msg.err
		var reload tea.Cmd
		if msg.stamps != m.stamps && !m.loading {
			reload = m.reload()
		}
		return m, tea.Batch(reload, m.broadcast(msg))
	}
	return m, m.broadcast(msg)
}

// broadcast hands a non-key message to every tab and every open modal.
func (m *Model) broadcast(msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd
	for _, t := range m.tabs {
		cmds = append(cmds, t.update(m, msg))
	}
	for _, md := range append([]modal(nil), m.modals...) {
		cmd, closed := md.update(m, msg)
		if closed {
			m.removeModal(md)
		}
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

func (m *Model) handleKey(k tea.KeyMsg) tea.Cmd {
	key := k.String()
	if key == "ctrl+c" {
		return m.quit()
	}
	if n := len(m.modals); n > 0 {
		top := m.modals[n-1]
		if o, ok := top.(escOwner); key == "esc" && (!ok || !o.ownsEsc()) {
			m.removeModal(top)
			return nil
		}
		cmd, closed := top.update(m, k)
		if closed {
			m.removeModal(top)
		}
		return cmd
	}
	tab := m.tabs[m.tab]
	if c, ok := tab.(inputCapturer); ok && c.capturing() {
		return tab.update(m, k)
	}
	switch key {
	case "q":
		return m.quit()
	case "?":
		m.openModal(&helpModal{})
		return nil
	case "tab":
		m.switchTab(m.tab + 1)
		return nil
	case "shift+tab":
		m.switchTab(m.tab - 1)
		return nil
	case "1", "2", "3", "4", "5":
		m.switchTab(Tab(key[0] - '1'))
		return nil
	case "R":
		return m.reload()
	}
	return tab.update(m, k)
}

// --- view ------------------------------------------------------------------------------------

// View renders the header, the active tab's body with the open modals over it, and the footer.
func (m *Model) View() string {
	if m.quitting {
		return ""
	}
	w, h := m.width, m.height
	if w <= 0 || h <= 0 {
		w, h = 100, 30
	}
	bh := max(h-2, 1)
	body := m.tabs[m.tab].view(m, w, bh)
	for _, md := range m.modals {
		body = overlay(body, md.view(m, w, bh), w, bh)
	}
	return fit(m.header(w), w) + "\n" + clampLines(body, w, bh) + "\n" + m.footer(w)
}

// chips are the header's status chips; compact is the short form for narrow terminals.
func (m *Model) chips(compact bool) string {
	s := m.snap
	if s.Loaded.IsZero() {
		return dimStyle.Render("loading …")
	}
	var parts []string
	switch {
	case s.Status != nil && compact:
		parts = append(parts, goodStyle.Render("●"))
	case s.Status != nil:
		parts = append(parts, dimStyle.Render("daemon ")+goodStyle.Render("● running"))
	case compact:
		parts = append(parts, dimStyle.Render("○"))
	default:
		t := "○ stopped"
		if !daemonStopped(s.DaemonErr) {
			t = "○ no answer"
		}
		parts = append(parts, dimStyle.Render("daemon "+t))
	}
	switch {
	case s.Status != nil:
		ready := 0
		for _, st := range s.Status.Servers {
			if st.State == "ready" {
				ready++
			}
		}
		t := "servers " + itoa(ready) + "/" + itoa(len(s.Status.Servers)) + " ready"
		if compact {
			t = "srv " + itoa(ready) + "/" + itoa(len(s.Status.Servers))
		}
		parts = append(parts, t)
	case s.Config != nil:
		t := "servers " + itoa(len(s.Config.Servers))
		if compact {
			t = "srv " + itoa(len(s.Config.Servers))
		}
		parts = append(parts, t)
	}
	if s.Catalog != nil {
		t := "skills " + itoa(len(s.Catalog.Skills))
		if compact {
			t = "sk " + itoa(len(s.Catalog.Skills))
		}
		parts = append(parts, t)
	}
	if n := len(s.warnings()); n > 0 {
		parts = append(parts, alertChip.Render("! "+itoa(n)))
	}
	sep := dimStyle.Render(" · ")
	if compact {
		sep = " "
	}
	return strings.Join(parts, sep)
}

func (m *Model) tabLabels(short bool) string {
	var parts []string
	for i, n := range tabNames {
		label := itoa(i+1) + " " + n
		if Tab(i) == m.tab {
			parts = append(parts, tabActive.Render(label))
			continue
		}
		if short {
			label = itoa(i + 1)
		}
		parts = append(parts, tabInactive.Render(label))
	}
	return strings.Join(parts, "")
}

// header is the tab bar with the chips right-aligned; it degrades step by step on narrow terminals.
func (m *Model) header(w int) string {
	app := " " + appName.Render("skilltri") + "  "
	for _, v := range []struct {
		app, short, compact bool
	}{{true, false, false}, {true, false, true}, {false, false, true}, {false, true, true}} {
		l := m.tabLabels(v.short)
		if v.app {
			l = app + l
		}
		c := m.chips(v.compact)
		if width(l)+width(c)+2 <= w {
			return l + strings.Repeat(" ", w-width(l)-width(c)-1) + c + " "
		}
	}
	return fit(m.tabLabels(true)+" "+m.chips(true), w)
}

func (m *Model) hints() []string {
	if n := len(m.modals); n > 0 {
		if h, ok := m.modals[n-1].(modalHinter); ok {
			return h.hints()
		}
		return []string{"esc", "close"}
	}
	return append(append([]string{}, m.tabs[m.tab].hints()...), "?", "help", "q", "quit")
}

// footer shows the key hints on the left and the status line on the right.
func (m *Model) footer(w int) string {
	left := " " + keyHints(m.hints()...)
	status := m.statusText()
	if status == "" {
		return fit(left, w)
	}
	st := goodStyle
	if m.statusErr {
		st = badStyle
	}
	room := w - width(left) - 3
	if room < 20 {
		return fit(" "+st.Render(fit(status, max(w-2, 0))), w)
	}
	status = fit(status, min(room, width(status)))
	return left + strings.Repeat(" ", w-width(left)-width(status)-1) + st.Render(status) + " "
}

func itoa(n int) string { return strconv.Itoa(n) }

// --- placeholder tab -----------------------------------------------------------------------------

// placeholderTab stands in for tabs that are not implemented yet: it lists what the snapshot has.
type placeholderTab struct{ tab Tab }

func (p *placeholderTab) title() string                  { return tabNames[p.tab] }
func (p *placeholderTab) update(*Model, tea.Msg) tea.Cmd { return nil }
func (p *placeholderTab) hints() []string                { return []string{"R", "reload"} }
func (p *placeholderTab) view(m *Model, w, h int) string {
	iw := w - 4
	var lines []string
	s := m.snap
	switch p.tab {
	case TabServers:
		for _, n := range s.serverNames() {
			icon := dimStyle.Render("○")
			if s.serverState(n) == "ready" {
				icon = goodStyle.Render("●")
			}
			lines = append(lines, icon+" "+oneLine(n))
		}
	case TabSkills:
		if s.Catalog != nil {
			for _, e := range s.Catalog.Skills {
				lines = append(lines, oneLine(e.ID))
			}
		}
	case TabPreview:
		lines = append(lines, kv("folder", ellipsizeMiddle(tildify(san(m.opts.Cwd)), max(iw-8, 1)), 8))
	case TabSetup:
		lines = append(lines,
			kv("binary", ellipsizeMiddle(tildify(san(m.opts.Bin)), max(iw-8, 1)), 8),
			kv("config", ellipsizeMiddle(tildify(m.opts.configPath()), max(iw-8, 1)), 8))
	case TabDoctor:
		for _, wn := range s.warnings() {
			for _, l := range wrap(wn, max(iw-2, 1)) {
				lines = append(lines, warnStyle.Render("! ")+l)
			}
		}
	}
	if len(lines) == 0 {
		lines = append(lines, dimStyle.Render("nothing here yet"))
	}
	soon := []string{dimStyle.Render("coming soon")}
	if w >= wideMin {
		lw := min(max(w*2/5, 30), 60)
		return hjoin(panel(p.title(), lines, lw, h, true, ""), panel("Details", soon, w-lw, h, false, ""))
	}
	return panel(p.title(), append(lines, "", soon[0]), w, h, true, "")
}
