package tui

import (
	"context"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/baeroe/skilltri/internal/doctor"
)

// doctorWork is the busy key of a doctor run.
const doctorWork = "doctor:run"

// doctorMsg is the result of a doctor run; seq tells cancelled and superseded runs apart.
type doctorMsg struct {
	seq      int
	findings []doctor.Finding
}

// doctorTab runs the health checks (on its first activation and with r) and lists the findings by
// severity; enter on a finding about a server or a source opens it.
type doctorTab struct {
	seq      int // newest started run
	ran      bool
	findings []doctor.Finding
	at       time.Time // when the shown findings arrived
	cancel   context.CancelFunc
	idx      int // selected finding
}

func (t *doctorTab) title() string { return "Doctor" }

func (t *doctorTab) hints() []string {
	if t.cancel != nil {
		return []string{"esc", "cancel"}
	}
	return []string{"r", "run again", "enter", "jump to"}
}

func (t *doctorTab) activated(m *Model) tea.Cmd {
	if t.ran {
		return nil
	}
	return t.run(m)
}

func (t *doctorTab) closed(m *Model) { t.stop() }

func (t *doctorTab) stop() {
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
}

// doctorEnv is where the checks look, from the Options.
func (o Options) doctorEnv() doctor.Env {
	return doctor.Env{
		Home:       o.Home,
		ConfigPath: o.configPath(),
		Socket:     o.socket(),
		CacheDir:   filepath.Dir(o.catalogPath()),
		StatePath:  o.StatePath,
		Environ:    append(append([]string(nil), o.Environ...), "PWD="+o.Cwd), // later entries win
	}
}

// run starts the checks in a command (a running one is superseded).
func (t *doctorTab) run(m *Model) tea.Cmd {
	t.stop()
	t.ran = true
	t.seq++
	m.end(doctorWork)
	m.begin(doctorWork, "running the doctor …")
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	seq, run, env := t.seq, m.opts.Doctor, m.opts.doctorEnv()
	return func() tea.Msg {
		return doctorMsg{seq: seq, findings: run(ctx, env)}
	}
}

func (t *doctorTab) update(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case doctorMsg:
		if msg.seq != t.seq {
			return nil // cancelled or superseded
		}
		t.stop()
		m.end(doctorWork)
		t.findings, t.at = msg.findings, m.opts.Now()
		t.idx = min(t.idx, max(len(t.findings)-1, 0))
		m.flash("doctor: "+doctorSummary(t.findings), doctor.HasError(t.findings))
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			t.idx = min(t.idx+1, max(len(t.findings)-1, 0))
		case "k", "up":
			t.idx = max(t.idx-1, 0)
		case "r":
			return t.run(m)
		case "esc":
			if t.cancel != nil {
				t.stop()
				t.seq++
				m.end(doctorWork)
				m.flash("doctor cancelled", false)
			}
		case "enter":
			t.jump(m)
		}
	}
	return nil
}

// doctorSummary counts the findings ("2 errors, 1 warning, 3 infos" or "no problems found").
func doctorSummary(fs []doctor.Finding) string {
	if len(fs) == 0 {
		return "no problems found"
	}
	n := map[string]int{}
	for _, f := range fs {
		n[f.Severity]++
	}
	var parts []string
	for _, s := range []struct{ sev, one, many string }{
		{doctor.Error, "error", "errors"}, {doctor.Warning, "warning", "warnings"}, {doctor.Info, "hint", "hints"},
	} {
		switch c := n[s.sev]; {
		case c == 1:
			parts = append(parts, "1 "+s.one)
		case c > 1:
			parts = append(parts, itoa(c)+" "+s.many)
		}
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += ", " + p
	}
	return out
}

// jump opens the server or source the selected finding is about.
func (t *doctorTab) jump(m *Model) {
	if t.idx >= len(t.findings) {
		return
	}
	f := t.findings[t.idx]
	cfg := m.snap.Config
	isSource := cfg != nil && cfg.Sources[f.Subject] != nil
	isServer := cfg != nil && cfg.Servers[f.Subject] != nil
	switch {
	case isSource && (f.Check == "sources" || !isServer):
		if m.tabs[TabSkills].(*skillsTab).selectSource(m, f.Subject) {
			m.switchTab(TabSkills)
			return
		}
	case isServer:
		if m.tabs[TabServers].(*serversTab).selectServer(m, f.Subject) {
			m.switchTab(TabServers)
			return
		}
	}
	m.flash(oneLine(f.Subject)+" is not a server or source", true)
}

// --- view ----------------------------------------------------------------------------------

// sevIcon is the list icon of a severity.
func sevIcon(sev string) string {
	switch sev {
	case doctor.Error:
		return badStyle.Render("✗")
	case doctor.Warning:
		return warnStyle.Render("!")
	}
	return blueStyle.Render("i")
}

var sevTitles = map[string]string{doctor.Error: "Errors", doctor.Warning: "Warnings", doctor.Info: "Hints"}

func (t *doctorTab) view(m *Model, w, h int) string {
	right := ""
	switch {
	case t.cancel != nil:
		right = warnStyle.Render("running …")
	case t.ran && !t.at.IsZero():
		right = dimStyle.Render("ran " + ago(t.at, m.opts.Now()))
	}
	if w >= wideMin {
		lw := listPaneWidth(w)
		return hjoin(panel("Doctor", t.listLines(m, lw-4, h-2), lw, h, true, right),
			panel("Details", t.detailLines(m, w-lw-4), w-lw, h, false, ""))
	}
	dh := 0
	if h >= 16 {
		dh = h * 2 / 5
	}
	out := panel("Doctor", t.listLines(m, w-4, h-dh-2), w, h-dh, true, right)
	if dh > 0 {
		out += "\n" + panel("Details", t.detailLines(m, w-4), w, dh, false, "")
	}
	return out
}

// listLines are the findings grouped by severity (each group under a title), the selection kept
// in view.
func (t *doctorTab) listLines(m *Model, iw, ih int) []string {
	if len(t.findings) == 0 {
		switch {
		case t.cancel != nil:
			return []string{dimStyle.Render("running the checks …")}
		case !t.ran:
			return []string{dimStyle.Render("press r to run the checks")}
		}
		return []string{goodStyle.Render("✓ no problems found")}
	}
	var lines []string
	sel := 0
	for i, f := range t.findings {
		if i == 0 || t.findings[i-1].Severity != f.Severity {
			if i > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, boldStyle.Render(sevTitles[f.Severity]))
		}
		subject := ellipsizeMiddle(oneLine(tildeText(f.Subject, m.opts.Home)), max(min(iw-2, max(iw*2/5, 12)), 1))
		line := sevIcon(f.Severity) + " " + subject
		if left := iw - width(line) - 2; left >= 4 {
			line += "  " + dimStyle.Render(ellipsizeMiddle(oneLine(tildeText(f.Message, m.opts.Home)), left))
		}
		if i == t.idx {
			sel = len(lines)
			line = highlightRow(line, iw)
		}
		lines = append(lines, line)
	}
	top := min(max(sel-ih/2, 0), max(len(lines)-ih, 0))
	return lines[top:min(top+ih, len(lines))]
}

func (t *doctorTab) detailLines(m *Model, iw int) []string {
	if t.idx >= len(t.findings) {
		return []string{dimStyle.Render("nothing selected")}
	}
	f := t.findings[t.idx]
	const lw = 9
	vw := max(iw-lw, 1)
	var out []string
	para := func(label, text string, style func(...string) string) {
		for i, l := range wrap(tildeText(text, m.opts.Home), vw) {
			if i > 0 {
				label = ""
			}
			out = append(out, kv(label, style(l), lw))
		}
	}
	plain := func(s ...string) string { return s[0] }
	out = append(out, kv("severity", sevIcon(f.Severity)+" "+f.Severity, lw))
	para("check", f.Check, plain)
	para("subject", f.Subject, plain)
	out = append(out, "")
	para("message", f.Message, plain)
	if f.Fix != "" {
		out = append(out, "")
		para("fix:", f.Fix, codeStyle.Render)
	}
	return out
}
