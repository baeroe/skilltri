// Package tui is the skilltri terminal UI (Bubble Tea): servers, skills, a folder preview, harness
// setup and doctor, on top of an asynchronously loaded snapshot of config, catalog and daemon status.
//
// Structure (later tabs and dialogs plug into it):
//
//   - Each tab is a tabModel (see below) with its own state, keys, view and hints. The Model routes
//     every key the global handler does not take to the active tab, and every other message except
//     tickMsg and tea.WindowSizeMsg (snapshotMsg, statusMsg, results of commands, …) to all tabs,
//     after the Model applied it. Every tab therefore sees every result: a tab returns results of
//     its own work as its own message types and ignores types it does not know. A tab keeps its
//     selection by key (e.g. the selected server name), not by index, and re-resolves it when it
//     sees a snapshotMsg: reloads never reset it. The terminal size is m.width/m.height (view also
//     gets the body size). A tab that implements activator is told when it becomes the active tab
//     (also the initial one), e.g. to start work it does only once it is looked at.
//   - Dialogs are modals on a stack (Model.openModal); only the top one gets keys. esc closes the top
//     modal (unless it implements escOwner), q quits only when no modal is open, ctrl+c always quits.
//     Non-key messages go to every open modal, so a form under a confirmation still gets its results.
//     A modal that owns background work implements closer: closed is called whenever it leaves the
//     stack (esc, closed=true, quit) so it can cancel that work. Tabs may implement closer too; it
//     is called on quit.
//   - Nothing in Update blocks: loading, daemon calls and other work run in tea.Cmds; results come
//     back as messages. Action results are reported with Model.flash in the footer's status line.
//   - Work in flight is shown with Model.begin(key, text): the status line shows a spinner and text
//     until Model.end(key), which the handler of the result message calls. begin returns false while
//     key is already pending, so a repeated key press does not start the same work twice. Background
//     polls the user did not ask for (the which retry) use Model.beginPassive: they show only while
//     no flash message is shown. The spinner advances on a spinMsg every spinInterval, scheduled
//     only while something is busy.
//   - Model.act runs a one-shot action in a command and reports its result (actionMsg) itself: the
//     Model ends the busy key, flashes the text or error and reloads when asked.
package tui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/baeroe/skilltri/internal/actions"
	"github.com/baeroe/skilltri/internal/daemon"
	"github.com/baeroe/skilltri/internal/doctor"
	"github.com/baeroe/skilltri/internal/skilllock"
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
	// Clipboard copies text to the system clipboard (default pbcopy / wl-copy / xclip).
	Clipboard func(text string) error
	// Update syncs the skill sources names (all enabled ones when empty): through the daemon's
	// "update" when viaDaemon (falling back to offline when it does not listen), else offline,
	// saving the catalog. It returns when ctx is done. Default Options.updateSources.
	Update func(ctx context.Context, names []string, viaDaemon bool) (daemon.UpdateReply, error)
	// Service finds, installs and removes the daemon's login service (default: the service package).
	Service Service
	// Doctor runs the health checks (default doctor.Run). The tab cancels ctx on esc and quit and
	// drops a cancelled run's result; doctor.Run itself is short and read-only.
	Doctor func(ctx context.Context, e doctor.Env) []doctor.Finding
	// Environ is the TUI's environment (default os.Environ()): SKILLTRI_HOOK and the doctor's Environ.
	Environ []string
	// StatePath is the skill lock file, for the doctor (default skilllock.Path()).
	StatePath string
}

// tabModel is one tab. update gets the keys the Model does not handle globally and every non-key
// message except tickMsg and tea.WindowSizeMsg (results of other tabs included: use own message
// types and ignore the others); it returns follow-up work as a command. view renders the body in exactly w x h cells
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

// closer is implemented by modals (and tabs) that own background work: closed is called when the
// modal leaves the stack for any reason, and for every open modal and every tab on quit.
type closer interface{ closed(m *Model) }

// activator is implemented by tabs that want to know when they become the active tab.
type activator interface{ activated(m *Model) tea.Cmd }

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
	issued     int  // last issued load/status sequence number (orders snapshot and status results)
	loadSeq    int  // seq of the last issued snapshot load
	snapSeq    int  // seq of the applied snapshot
	statusSeq  int  // seq of the applied daemon status (from a snapshot or a status refresh)
	loading    bool // a full snapshot load is in flight
	statusBusy bool // a status refresh is in flight

	status    string
	statusErr bool
	statusAt  time.Time

	busyKeys    []string          // pending work, oldest first (see begin)
	busyText    map[string]string // its status text
	busyPassive map[string]bool   // keys started with beginPassive
	spinning    bool              // a spinMsg is scheduled
	spinStep    int

	quitting bool

	cmdName string // how the Setup tab's lines call skilltri (commandName)
}

// spinInterval is the spinner's frame time; spinFrames are its ASCII frames.
const spinInterval = 120 * time.Millisecond

var spinFrames = []string{"|", "/", "-", "\\"}

// spinMsg advances the spinner.
type spinMsg struct{}

// begin marks the work key as running with the status text ("restarting demo …"). It returns
// false, changing nothing, when key is already running.
func (m *Model) begin(key, text string) bool { return m.startBusy(key, text, false) }

// beginPassive is begin for background work the user did not ask for: its text does not replace a
// flash message in the status line.
func (m *Model) beginPassive(key, text string) bool { return m.startBusy(key, text, true) }

func (m *Model) startBusy(key, text string, passive bool) bool {
	if _, ok := m.busyText[key]; ok {
		return false
	}
	if m.busyText == nil {
		m.busyText, m.busyPassive = map[string]string{}, map[string]bool{}
	}
	m.busyKeys = append(m.busyKeys, key)
	m.busyText[key] = text
	if passive {
		m.busyPassive[key] = true
	}
	return true
}

// end marks the work key as done (a no-op when it is not running).
func (m *Model) end(key string) {
	if _, ok := m.busyText[key]; !ok {
		return
	}
	delete(m.busyText, key)
	delete(m.busyPassive, key)
	for i, k := range m.busyKeys {
		if k == key {
			m.busyKeys = append(m.busyKeys[:i], m.busyKeys[i+1:]...)
			break
		}
	}
}

// busy reports whether the work key is running.
func (m *Model) busy(key string) bool { _, ok := m.busyText[key]; return ok }

// busyLine is the spinner and the newest pending work that is passive or not ("" when none).
func (m *Model) busyLine(passive bool) string {
	var keys []string
	for _, k := range m.busyKeys {
		if m.busyPassive[k] == passive {
			keys = append(keys, k)
		}
	}
	n := len(keys)
	if n == 0 {
		return ""
	}
	s := spinFrames[m.spinStep%len(spinFrames)] + " " + m.busyText[keys[n-1]]
	if n > 1 {
		s += " (+" + itoa(n-1) + ")"
	}
	return s
}

// spin schedules the next spinner frame while something is busy (not with NoTick: tests).
func (m *Model) spin() tea.Cmd {
	if len(m.busyKeys) == 0 || m.spinning || m.opts.NoTick {
		return nil
	}
	m.spinning = true
	return tea.Tick(spinInterval, func(time.Time) tea.Msg { return spinMsg{} })
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
	if o.Env.Dir == "" {
		o.Env.Dir = o.Cwd // unknown-tool checks of rule edits look at the TUI's folder
	}
	if o.Env.ControlTimeout == 0 {
		o.Env.ControlTimeout = controlTimeout
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
	if o.Clipboard == nil {
		o.Clipboard = copyToClipboard
	}
	if o.Update == nil {
		o.Update = o.updateSources
	}
	o.Service = o.Service.withDefaults()
	if o.Doctor == nil {
		o.Doctor = doctor.Run
	}
	if o.Environ == nil {
		o.Environ = os.Environ()
	}
	if o.StatePath == "" {
		o.StatePath = skilllock.Path()
	}
	if o.Tab < 0 || o.Tab >= numTabs {
		o.Tab = TabServers
	}
	m := &Model{opts: o, tab: o.Tab, cmdName: commandName(o.Bin, exec.LookPath)}
	m.tabs[TabServers] = newServersTab()
	m.tabs[TabSkills] = newSkillsTab()
	m.tabs[TabPreview] = newPreviewTab(o.Cwd, o.Home)
	m.tabs[TabSetup] = newSetupTab(o.Bin)
	m.tabs[TabDoctor] = &doctorTab{}
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
	cmds := []tea.Cmd{m.reload(), m.activate()}
	if !m.opts.NoTick {
		cmds = append(cmds, tick())
	}
	return tea.Batch(cmds...)
}

// activate tells the active tab that it became active (see activator).
func (m *Model) activate() tea.Cmd {
	if a, ok := m.tabs[m.tab].(activator); ok {
		return a.activated(m)
	}
	return nil
}

// reload starts a full snapshot load.
func (m *Model) reload() tea.Cmd {
	m.issued++
	m.loadSeq = m.issued
	seq, load := m.issued, loadSnapshot(m.opts)
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
	m.issued++
	m.statusBusy = true
	seq, load := m.issued, loadStatus(m.opts)
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
			if c, ok := md.(closer); ok {
				c.closed(m)
			}
			return
		}
	}
}

// switchTab makes t the active tab and returns the work its activator starts.
func (m *Model) switchTab(t Tab) tea.Cmd {
	m.tab = (t + numTabs) % numTabs
	return m.activate()
}

// quit closes every modal (top first) and tells the tabs, so background work is cancelled.
func (m *Model) quit() tea.Cmd {
	for len(m.modals) > 0 {
		m.removeModal(m.modals[len(m.modals)-1])
	}
	for _, t := range m.tabs {
		if c, ok := t.(closer); ok {
			c.closed(m)
		}
	}
	m.quitting = true
	return tea.Quit
}

// Update handles messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.update(msg)
	return m, tea.Batch(cmd, m.spin())
}

func (m *Model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case spinMsg:
		m.spinning = false
		m.spinStep++
		return nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case actionMsg:
		return m.actionDone(msg)
	case tickMsg:
		if m.statusText() == "" {
			m.status = ""
		}
		cmds := []tea.Cmd{m.refreshStatus()}
		if !m.opts.NoTick {
			cmds = append(cmds, tick())
		}
		return tea.Batch(cmds...)
	case snapshotMsg:
		if msg.seq < m.snapSeq {
			return nil // an older load finished after a newer one
		}
		if msg.seq == m.loadSeq {
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
			return nil
		}
		m.statusSeq = msg.seq
		m.snap.Status, m.snap.DaemonErr = msg.status, msg.err
		var reload tea.Cmd
		if msg.stamps != m.stamps && !m.loading {
			reload = m.reload()
		}
		return tea.Batch(reload, m.broadcast(msg))
	}
	return m.broadcast(msg)
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
		return m.switchTab(m.tab + 1)
	case "shift+tab":
		return m.switchTab(m.tab - 1)
	case "1", "2", "3", "4", "5":
		return m.switchTab(Tab(key[0] - '1'))
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
	status, st := m.statusText(), goodStyle
	if m.statusErr {
		st = badStyle
	}
	if b := m.busyLine(false); b != "" {
		status, st = b, warnStyle
	} else if b := m.busyLine(true); b != "" && status == "" {
		status, st = b, warnStyle
	}
	if status == "" {
		return fit(left, w)
	}
	room := w - width(left) - 3
	if room < width(status) { // the status wins over the hints while it is shown
		return fit(" "+st.Render(fit(status, max(w-2, 0))), w)
	}
	return left + strings.Repeat(" ", w-width(left)-width(status)-1) + st.Render(status) + " "
}

func itoa(n int) string { return strconv.Itoa(n) }

// copyToClipboard pipes text into the first clipboard tool found.
func copyToClipboard(text string) error {
	for _, c := range [][]string{{"pbcopy"}, {"wl-copy"}, {"xclip", "-selection", "clipboard"}} {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdin = strings.NewReader(text)
		return cmd.Run()
	}
	return errors.New("no clipboard tool (pbcopy, wl-copy or xclip)")
}
