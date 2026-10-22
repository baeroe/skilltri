package tui

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/baeroe/skilltri/internal/actions"
	"github.com/baeroe/skilltri/internal/adapters"
	"github.com/baeroe/skilltri/internal/daemon"
	harnesscfg "github.com/baeroe/skilltri/internal/harness"
	"github.com/baeroe/skilltri/internal/service"
)

// Service is the daemon's login service as the Setup tab uses it; tests inject fakes so nothing
// runs launchctl or systemctl. Zero fields default to the service package.
type Service struct {
	File      func() (string, error) // the plist / unit path
	Installed func() bool            // the service file exists
	Install   func(exe string) error
	Uninstall func() error
}

func (s Service) withDefaults() Service {
	if s.File == nil {
		s.File = service.File
	}
	if s.Installed == nil {
		s.Installed = service.Installed
	}
	if s.Install == nil {
		s.Install = service.Install
	}
	if s.Uninstall == nil {
		s.Uninstall = service.Uninstall
	}
	return s
}

// Busy keys of the Setup tab.
const (
	setupScanWork    = "setup:scan"
	setupServiceWork = "setup:service"
	setupCopyWork    = "setup:copy"
	setupAdapterWork = "setup:adapter:" // + adapter id
)

// setupScanMsg is what the Setup tab reads from the files (in a command): harness registrations,
// 1MCP leftovers, detected adapters and the service file.
type setupScanMsg struct {
	seq        int
	registered map[string]bool
	oneMCP     []string
	detected   map[string]bool
	installed  bool
	file       string
	fileErr    error
}

type suKind int

const (
	suHeader suKind = iota
	suBlank
	suHarness
	suShell
	suService
	suAdapter
	suImport
)

// suRow is one line of the Setup list; every kind but header and blank can be selected.
type suRow struct {
	kind    suKind
	text    string // header title
	snip    harnesscfg.Snippet
	shell   string
	adapter adapters.Adapter
	imp     string // importOneMCP | importClaude
}

func (r suRow) selectable() bool { return r.kind > suBlank }

// shells are the shells skilltri has a hook for.
var shells = []string{"zsh", "bash", "fish"}

// commandName is how the Setup lines call skilltri: "skilltri" when that resolves on PATH (with
// lookPath) to bin, else bin itself, shell-quoted.
func commandName(bin string, lookPath func(string) (string, error)) string {
	if p, err := lookPath("skilltri"); err == nil && sameFile(p, bin) {
		return "skilltri"
	}
	if bin == "" {
		return "skilltri"
	}
	return shellQuote(bin)
}

func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// hookLine is the rc-file line that loads the shell hook, calling skilltri as cmd.
func hookLine(shell, cmd string) string {
	if shell == "fish" {
		return cmd + " shell-init fish | source"
	}
	return `eval "$(` + cmd + ` shell-init ` + shell + `)"`
}

// rcFile is where hookLine goes.
func rcFile(shell string) string {
	switch shell {
	case "bash":
		return "~/.bashrc"
	case "fish":
		return "~/.config/fish/config.fish"
	}
	return "~/.zshrc"
}

// harnessNames are the display names of the harness ids.
var harnessNames = map[string]string{
	harnesscfg.Claude: "Claude Code", harnesscfg.Codex: "Codex CLI", harnesscfg.Gemini: "Gemini CLI",
	harnesscfg.OpenCode: "opencode", harnesscfg.Cursor: "Cursor",
}

// harnessFiles are the user config files the detection reads.
var harnessFiles = map[string]string{
	harnesscfg.Claude: "~/.claude.json", harnesscfg.Codex: "~/.codex/config.toml", harnesscfg.Gemini: "~/.gemini/settings.json",
	harnesscfg.OpenCode: "~/.config/opencode/opencode.json", harnesscfg.Cursor: "~/.cursor/mcp.json",
}

// setupTab shows how skilltri is wired into the system: harness registration, the shell hook, the
// daemon service, the skill adapters and the imports from 1MCP and Claude Code.
type setupTab struct {
	rows []suRow
	idx  int

	seq  int // newest issued scan
	scan *setupScanMsg

	imp     *importModal // the open import preview, if any
	readSeq int          // newest import read; older (cancelled or superseded) previews are dropped
}

func newSetupTab(bin string) *setupTab {
	rows := []suRow{{kind: suHeader, text: "Harnesses"}}
	for _, s := range harnesscfg.Snippets(bin) {
		rows = append(rows, suRow{kind: suHarness, snip: s})
	}
	rows = append(rows, suRow{kind: suBlank}, suRow{kind: suHeader, text: "Shell hook"})
	for _, sh := range shells {
		rows = append(rows, suRow{kind: suShell, shell: sh})
	}
	rows = append(rows, suRow{kind: suBlank}, suRow{kind: suHeader, text: "Daemon service"}, suRow{kind: suService},
		suRow{kind: suBlank}, suRow{kind: suHeader, text: "Adapters"})
	for _, a := range adapters.Registry {
		rows = append(rows, suRow{kind: suAdapter, adapter: a})
	}
	rows = append(rows, suRow{kind: suBlank}, suRow{kind: suHeader, text: "Import"},
		suRow{kind: suImport, imp: importOneMCP}, suRow{kind: suImport, imp: importClaude})
	return &setupTab{rows: rows, idx: 1}
}

func (t *setupTab) title() string { return "Setup" }

func (t *setupTab) hints() []string {
	switch t.rows[t.idx].kind {
	case suHarness:
		return []string{"c", "copy snippet", "R", "reload"}
	case suShell:
		return []string{"c", "copy line", "R", "reload"}
	case suService:
		return []string{"i", "install", "x", "uninstall", "s", "stop daemon", "R", "reload"}
	case suAdapter:
		return []string{"space", "on/off", "R", "reload"}
	case suImport:
		return []string{"enter", "preview import", "R", "reload"}
	}
	return []string{"R", "reload"}
}

// hookShell is SKILLTRI_HOOK in the TUI's environment ("" when the hook is not loaded).
func (m *Model) hookShell() string {
	v := ""
	for _, kv := range m.opts.Environ {
		if k, val, ok := strings.Cut(kv, "="); ok && k == "SKILLTRI_HOOK" {
			v = val // later entries win
		}
	}
	return v
}

// startScan reads the setup state from the files in a command (a background poll: passive).
func (t *setupTab) startScan(m *Model) tea.Cmd {
	t.seq++
	m.end(setupScanWork)
	m.beginPassive(setupScanWork, "checking the setup …")
	seq, home, svc := t.seq, m.opts.Home, m.opts.Service
	return func() tea.Msg {
		msg := setupScanMsg{seq: seq, registered: harnesscfg.Registered(home), oneMCP: harnesscfg.OneMCPRegistered(home),
			detected: map[string]bool{}, installed: svc.Installed()}
		for _, a := range adapters.Detected(home) {
			msg.detected[a.ID] = true
		}
		msg.file, msg.fileErr = svc.File()
		return msg
	}
}

func (t *setupTab) update(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case snapshotMsg: // also after the service and adapter actions, which reload
		return t.startScan(m)
	case setupScanMsg:
		if msg.seq != t.seq {
			return nil
		}
		m.end(setupScanWork)
		t.scan = &msg
	case importPreviewMsg, importDoneMsg:
		return t.importUpdate(m, msg)
	case tea.KeyMsg:
		return t.key(m, msg.String())
	}
	return nil
}

func (t *setupTab) move(dir int) {
	for i := t.idx + dir; i >= 0 && i < len(t.rows); i += dir {
		if t.rows[i].selectable() {
			t.idx = i
			return
		}
	}
}

func (t *setupTab) key(m *Model, key string) tea.Cmd {
	r := t.rows[t.idx]
	switch key {
	case "j", "down":
		t.move(1)
	case "k", "up":
		t.move(-1)
	case "c":
		switch r.kind {
		case suHarness:
			return copyCmd(m, r.snip.Text, "copied the "+r.snip.Name+" snippet")
		case suShell:
			return copyCmd(m, hookLine(r.shell, m.cmdName), "copied "+hookLine(r.shell, m.cmdName))
		}
	case "i":
		if r.kind == suService {
			return t.install(m)
		}
	case "x":
		if r.kind == suService {
			t.confirmUninstall(m)
		}
	case "s":
		if r.kind == suService {
			return t.stopDaemon(m)
		}
	case " ":
		if r.kind == suAdapter {
			return t.toggleAdapter(m, r.adapter)
		}
	case "enter":
		if r.kind == suImport {
			return t.startImport(m, r.imp)
		}
	case "esc": // stop waiting for an import's source to be read (not for an apply in flight)
		for _, k := range []string{importOneMCP, importClaude} {
			if m.busy(importWork(k)) && !t.applying() {
				t.readSeq++ // the late preview is dropped
				m.end(importWork(k))
				m.flash("import "+importTitles[k]+" cancelled", false)
			}
		}
	}
	return nil
}

// applying reports whether an import is being applied (its dialog may be closed already).
func (t *setupTab) applying() bool { return t.imp != nil && t.imp.applying }

// copyCmd copies text to the clipboard in a command.
func copyCmd(m *Model, text, done string) tea.Cmd {
	clip := m.opts.Clipboard
	return m.act(setupCopyWork, "copying …", false, func() (string, error) {
		if err := clip(text); err != nil {
			return "", err
		}
		return done, nil
	})
}

func (t *setupTab) install(m *Model) tea.Cmd {
	svc, exe := m.opts.Service, m.opts.Bin
	return m.act(setupServiceWork, "installing the daemon service …", true, func() (string, error) {
		if err := svc.Install(exe); err != nil {
			return "", err
		}
		return "installed the daemon service", nil
	})
}

func (t *setupTab) confirmUninstall(m *Model) {
	if t.scan != nil && !t.scan.installed {
		m.flash("the daemon service is not installed", true)
		return
	}
	file := "the service file"
	if t.scan != nil && t.scan.file != "" {
		file = tildify(san(t.scan.file), m.opts.Home)
	}
	body := []string{
		"Stop the login service and remove " + file + ".",
		dimStyle.Render("The daemon then no longer starts at login; harnesses still start it on demand."),
	}
	m.openModal(confirmDanger("Uninstall the daemon service", body, "uninstall", func(m *Model) tea.Cmd {
		svc := m.opts.Service
		return m.act(setupServiceWork, "uninstalling the daemon service …", true, func() (string, error) {
			if err := svc.Uninstall(); err != nil {
				return "", err
			}
			return "uninstalled the daemon service", nil
		})
	}))
}

func (t *setupTab) stopDaemon(m *Model) tea.Cmd {
	ctl := m.opts.Control
	return m.act(setupServiceWork, "stopping the daemon …", true, func() (string, error) {
		err := ctl(daemon.Hello{Cmd: "shutdown"}, nil)
		switch {
		case daemonStopped(err):
			return "the daemon is not running", nil
		case err != nil:
			return "", errors.New("stop the daemon: " + err.Error())
		}
		return "stopped the daemon", nil
	})
}

func (t *setupTab) toggleAdapter(m *Model, a adapters.Adapter) tea.Cmd {
	if !needConfig(m) {
		return nil
	}
	on := !slices.Contains(m.snap.Config.EnabledAdapters(), a.ID)
	env := m.opts.Env
	verb, done := "disabling ", "disabled the "
	if on {
		verb, done = "enabling ", "enabled the "
	}
	return m.act(setupAdapterWork+a.ID, verb+a.Display+" …", true, func() (string, error) {
		if err := actions.SetAdapter(env, a.ID, on); err != nil {
			return "", err
		}
		return done + a.Display + " adapter", nil
	})
}

// --- view ----------------------------------------------------------------------------------

func (t *setupTab) view(m *Model, w, h int) string {
	if w >= wideMin {
		lw := listPaneWidth(w)
		return hjoin(panel("Setup", t.listLines(m, lw-4, h-2), lw, h, true, ""),
			panel("Details", t.detailLines(m, w-lw-4), w-lw, h, false, ""))
	}
	dh := 0
	if h >= 16 {
		dh = h * 2 / 5
	}
	out := panel("Setup", t.listLines(m, w-4, h-dh-2), w, h-dh, true, "")
	if dh > 0 {
		out += "\n" + panel("Details", t.detailLines(m, w-4), w, dh, false, "")
	}
	return out
}

func (t *setupTab) listLines(m *Model, iw, ih int) []string {
	top := min(max(t.idx-ih/2, 0), max(len(t.rows)-ih, 0))
	var out []string
	for i := top; i < len(t.rows) && len(out) < ih; i++ {
		line := t.rowLine(m, t.rows[i], iw)
		if i == t.idx {
			line = highlightRow(line, iw)
		}
		out = append(out, line)
	}
	return out
}

// mark is ✓ (green) or ✗ (red).
func mark(ok bool) string {
	if ok {
		return goodStyle.Render("✓")
	}
	return badStyle.Render("✗")
}

func (t *setupTab) oneMCPLeft(id string) bool {
	return t.scan != nil && slices.Contains(t.scan.oneMCP, id)
}

// rowLine renders a row as "icon label  extra" in iw cells.
func (t *setupTab) rowLine(m *Model, r suRow, iw int) string {
	var icon, label, extra string
	switch r.kind {
	case suBlank:
		return ""
	case suHeader:
		return boldStyle.Render(r.text)
	case suHarness:
		icon, label = dimStyle.Render("-"), r.snip.Name
		if t.scan != nil {
			icon = mark(t.scan.registered[r.snip.Harness])
		}
		if t.oneMCPLeft(r.snip.Harness) {
			extra = warnStyle.Render("! 1MCP")
		}
	case suShell:
		icon, label = dimStyle.Render("○"), r.shell
		if m.hookShell() == r.shell {
			icon = goodStyle.Render("●")
		}
		extra = dimStyle.Render(hookLine(r.shell, m.cmdName))
	case suService:
		installed := t.scan != nil && t.scan.installed
		icon, label = mark(installed), "service"
		if !installed {
			label = "service not installed"
		}
		if m.snap.Status != nil {
			extra = goodStyle.Render("● running")
		} else {
			extra = dimStyle.Render("○ stopped")
		}
	case suAdapter:
		on := m.snap.Config != nil && slices.Contains(m.snap.Config.EnabledAdapters(), r.adapter.ID)
		icon, label = mark(on), r.adapter.Display
		det := "not detected"
		if t.scan != nil && t.scan.detected[r.adapter.ID] {
			det = "detected"
		}
		extra = dimStyle.Render(det + " · " + adapterMethod(r.adapter))
	case suImport:
		icon, label = accentStyle.Render("▸"), importTitles[r.imp]
		extra = dimStyle.Render(tildify(importFrom(m, r.imp), m.opts.Home))
	}
	line := "  " + icon + " "
	avail := max(iw-width(line), 1)
	label = ellipsizeMiddle(oneLine(label), min(avail, max(avail*3/5, 12)))
	line += label
	if left := avail - width(label) - 2; extra != "" && left >= 4 {
		line += "  " + ansi.Truncate(extra, left, "…")
	}
	return line
}

// adapterMethod says how an adapter delivers skills.
func adapterMethod(a adapters.Adapter) string {
	if a.Method == adapters.MethodClaudePluginDirs {
		return "plugin dirs"
	}
	return a.ProjectDir + " in git repos"
}

const suLabelW = 10

func (t *setupTab) detailLines(m *Model, iw int) []string {
	r := t.rows[t.idx]
	vw := max(iw-suLabelW, 1)
	val := func(s string) string { return ellipsizeMiddle(oneLine(s), vw) }
	path := func(p string) string { return ellipsizeMiddle(tildify(san(p), m.opts.Home), vw) }
	var out []string
	add := func(label, value string) { out = append(out, kv(label, value, suLabelW)) }
	para := func(label, text string, style func(...string) string) {
		for i, l := range wrap(text, vw) {
			if i > 0 {
				label = ""
			}
			add(label, style(l))
		}
	}
	plain := func(s ...string) string { return strings.Join(s, " ") }
	switch r.kind {
	case suHarness:
		add("harness", val(r.snip.Name))
		switch {
		case t.scan == nil:
			add("status", dimStyle.Render("checking …"))
		case t.scan.registered[r.snip.Harness]:
			add("status", goodStyle.Render("✓ skilltri is registered"))
		default:
			add("status", badStyle.Render("✗ not registered"))
		}
		add("config", path(harnessFiles[r.snip.Harness]))
		if t.oneMCPLeft(r.snip.Harness) {
			para("1MCP", "! still registered: remove that entry, skilltri replaces it", warnStyle.Render)
		}
		add("how", val(r.snip.How))
		out = append(out, "")
		for _, l := range wrapCode(r.snip.Text, iw) {
			out = append(out, codeStyle.Render(l))
		}
		if r.snip.Note != "" {
			out = append(out, "")
			para("note", r.snip.Note, warnStyle.Render)
		}
	case suShell:
		add("shell", r.shell)
		add("add to", path(rcFile(r.shell)))
		out = append(out, "")
		for _, l := range wrapCode(hookLine(r.shell, m.cmdName), iw) {
			out = append(out, codeStyle.Render(l))
		}
		out = append(out, "")
		switch hs := m.hookShell(); hs {
		case "":
			para("hook", "SKILLTRI_HOOK is not set: the shell that started skilltri has no hook loaded", warnStyle.Render)
		default:
			para("hook", "SKILLTRI_HOOK="+oneLine(hs)+": the hook is loaded in the shell that started skilltri", goodStyle.Render)
		}
	case suService:
		switch {
		case t.scan == nil:
			add("service", dimStyle.Render("checking …"))
		case t.scan.fileErr != nil:
			para("service", t.scan.fileErr.Error(), badStyle.Render)
		default:
			add("service", mark(t.scan.installed)+" "+map[bool]string{true: "installed", false: "not installed"}[t.scan.installed])
			add("file", path(t.scan.file))
		}
		if st := m.snap.Status; st != nil {
			add("daemon", goodStyle.Render("● running")+dimStyle.Render(" pid "+itoa(st.PID)+" · "+oneLine(st.Version)))
		} else {
			add("daemon", dimStyle.Render("○ not running"))
		}
		add("binary", path(m.opts.Bin))
		out = append(out, "")
		para("", "The service runs `"+m.cmdName+" daemon --wait` at login and restarts it when it fails; harnesses start the daemon on demand without it.", dimStyle.Render)
	case suAdapter:
		a := r.adapter
		on := m.snap.Config != nil && slices.Contains(m.snap.Config.EnabledAdapters(), a.ID)
		add("adapter", val(a.Display))
		add("id", val(a.ID))
		add("enabled", mark(on)+" "+map[bool]string{true: "yes", false: "no"}[on])
		det := "not detected"
		if t.scan != nil && t.scan.detected[a.ID] {
			det = "detected"
		}
		add("detected", val(det+" (~/"+strings.Join(a.DetectPaths, ", ~/")+")"))
		add("method", val(adapterMethod(a)))
		add("command", val(a.Binary))
	case suImport:
		add("from", path(importFrom(m, r.imp)))
		out = append(out, "")
		para("", importAbout[r.imp], plain)
		out = append(out, "")
		para("", "enter shows what would be imported; nothing is written before you confirm with y.", dimStyle.Render)
	}
	return out
}

// wrapCode breaks code into lines of at most w cells, keeping indentation and spaces.
func wrapCode(s string, w int) []string {
	if w <= 0 {
		return nil
	}
	var out []string
	for _, line := range strings.Split(san(s), "\n") {
		start := len(out)
		for width(line) > w {
			cut := ansi.Truncate(line, w, "")
			if cut == "" { // a wide rune in a 1-cell column: take it whole (clamped later)
				cut = string([]rune(line)[:1])
			}
			out = append(out, cut)
			line = string([]rune(line)[len([]rune(cut)):])
		}
		if line != "" || len(out) == start { // keep empty lines, not the rest of a cut one
			out = append(out, line)
		}
	}
	return out
}

// skillsDir is where an import copies user skills (next to config.json).
func (o Options) skillsDir() string { return filepath.Join(filepath.Dir(o.configPath()), "skills") }
