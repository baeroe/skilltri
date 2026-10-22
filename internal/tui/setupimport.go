package tui

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/baeroe/skilltri/internal/config"
	harnesscfg "github.com/baeroe/skilltri/internal/harness"
	"github.com/baeroe/skilltri/internal/importer"
)

// Import kinds.
const (
	importOneMCP = "1mcp"
	importClaude = "claude"
)

var importTitles = map[string]string{importOneMCP: "from 1MCP", importClaude: "from Claude Code"}

var importAbout = map[string]string{
	importOneMCP: "Copies the MCP servers of the 1MCP config into skilltri; header values and token-like env values go to the keychain. Existing servers are kept unless you choose to overwrite them.",
	importClaude: "Adds Claude Code's plugin marketplaces as sources (only its enabled plugins on) and copies its user skills into a local source. Plugins are not uninstalled: the next steps say what to remove by hand.",
}

// importFrom is the file or folder an import reads.
func importFrom(m *Model, kind string) string {
	if kind == importOneMCP {
		return filepath.Join(m.opts.Home, ".config", "1mcp", "mcp.json")
	}
	return filepath.Join(m.opts.Home, ".claude")
}

// importWork is the busy key of an import (preview and apply).
func importWork(kind string) string { return "setup:import:" + kind }

// forceHint ends the importer's warnings about skipped existing items; the preview lists those.
const forceHint = "already exists (use --force to overwrite)"

// importPreviewMsg is the result of reading an import's source: the previews without and with
// overwriting existing items.
type importPreviewMsg struct {
	seq    int // setupTab.readSeq of the read
	kind   string
	err    error
	from   string
	one    importer.Result
	claude importer.ClaudeResult
	prev   [2][]pvLine // [force]
	empty  [2]bool     // nothing would be imported
	repl   [2]int      // items that would be overwritten
	steps  []string    // 1MCP: next steps after the import
}

// importDoneMsg is the result of applying an import.
type importDoneMsg struct {
	kind  string
	text  string
	body  []pvLine
	steps int
	err   error
}

// startImport reads the import's source in a command and opens the preview.
func (t *setupTab) startImport(m *Model, kind string) tea.Cmd {
	// one import at a time: two would race on the config (and share the dialog slot)
	if m.busy(importWork(importOneMCP)) || m.busy(importWork(importClaude)) {
		m.flash("another import is running", true)
		return nil
	}
	m.begin(importWork(kind), "reading "+importTitles[kind]+" …")
	t.readSeq++
	seq, from, home, cfgPath, skillsDir := t.readSeq, importFrom(m, kind), m.opts.Home, m.opts.configPath(), m.opts.skillsDir()
	return func() tea.Msg {
		msg := importPreviewMsg{seq: seq, kind: kind, from: from}
		cfg, err := config.Load(cfgPath)
		if err != nil {
			msg.err = err
			return msg
		}
		if kind == importOneMCP {
			data, err := os.ReadFile(from)
			if err == nil {
				msg.one, err = importer.From1MCP(data)
			}
			if err != nil {
				msg.err = err
				return msg
			}
			msg.steps = oneMCPSteps(home)
			for i, force := range []bool{false, true} {
				a := importer.Preview1MCP(cfg, msg.one, force)
				msg.prev[i], msg.empty[i], msg.repl[i] = preview1MCPLines(msg.one, a, msg.steps), len(a.Imported) == 0, len(a.Replaced)
			}
			return msg
		}
		if msg.claude, err = importer.FromClaude(home); err != nil {
			msg.err = err
			return msg
		}
		for i, force := range []bool{false, true} {
			rep := importer.PreviewClaude(cfg, skillsDir, msg.claude, force)
			msg.prev[i] = previewClaudeLines(msg.claude, rep, skillsDir, home)
			msg.empty[i] = len(rep.Sources)+len(rep.Skills) == 0 && !rep.LocalAdded
			msg.repl[i] = len(rep.Replaced) + len(rep.ReplacedSkills)
		}
		return msg
	}
}

// oneMCPSteps are the manual follow-ups of a 1MCP import: drop 1MCP from the harnesses that still
// use it and register skilltri where it is missing.
func oneMCPSteps(home string) []string {
	var steps []string
	for _, id := range harnesscfg.OneMCPRegistered(home) {
		steps = append(steps, "remove the 1MCP entry from "+harnessNames[id]+" ("+harnessFiles[id]+")")
	}
	reg := harnesscfg.Registered(home)
	var missing []string
	for _, id := range harnesscfg.IDs {
		if !reg[id] {
			missing = append(missing, harnessNames[id])
		}
	}
	if len(missing) > 0 {
		steps = append(steps, "register skilltri in "+strings.Join(missing, ", ")+" (Setup tab, Harnesses: c copies the snippet)")
	}
	return steps
}

// state lines of an item that is imported (+), overwritten (!) or skipped (-).
func itemLine(name, what string, imported, replaced bool) pvLine {
	switch {
	case replaced:
		return pvLine{icon: badStyle.Render("!"), text: name + ": overwrites the existing one", style: badStyle}
	case imported:
		return pvLine{icon: goodStyle.Render("+"), text: name + ": " + what, style: lipgloss.NewStyle()}
	}
	return pvLine{icon: dimStyle.Render("-"), text: name + ": exists, skipped (f overwrites)", style: dimStyle}
}

func warnLines(ws []string) []pvLine {
	var out []pvLine
	for _, w := range ws {
		if !strings.HasSuffix(w, forceHint) {
			out = append(out, pvLine{icon: warnStyle.Render("!"), text: w, style: warnStyle})
		}
	}
	if len(out) > 0 {
		out = append([]pvLine{{}, {head: true, text: "Warnings"}}, out...)
	}
	return out
}

func stepLines(title string, steps []string) []pvLine {
	if len(steps) == 0 {
		return nil
	}
	out := []pvLine{{}, {head: true, text: title}}
	for _, s := range steps {
		out = append(out, pvLine{icon: accentStyle.Render("-"), text: s, style: lipgloss.NewStyle()})
	}
	return out
}

func preview1MCPLines(res importer.Result, a importer.Applied1MCP, steps []string) []pvLine {
	names := make([]string, 0, len(res.Servers))
	for n := range res.Servers {
		names = append(names, n)
	}
	sort.Strings(names)
	out := []pvLine{{head: true, text: "Servers (" + itoa(len(a.Imported)) + " of " + itoa(len(names)) + " imported)"}}
	secretsN := 0
	for _, n := range names {
		s := res.Servers[n]
		what := s.Type + " " + s.URL
		if s.Type == "stdio" {
			what = "stdio " + strings.Join(append([]string{s.Command}, s.Args...), " ")
		}
		if s.Disabled {
			what += " (disabled)"
		}
		imported := slices.Contains(a.Imported, n)
		if imported {
			secretsN += len(res.SecretsByServer[n])
		}
		out = append(out, itemLine(n, what, imported, slices.Contains(a.Replaced, n)))
	}
	if len(names) == 0 {
		out = append(out, pvLine{text: "no servers in the file", style: dimStyle})
	}
	if secretsN > 0 {
		out = append(out, pvLine{icon: blueStyle.Render("i"), text: itoa(secretsN) + " secret(s) go to the keychain", style: dimStyle})
	}
	out = append(out, warnLines(a.Warnings)...)
	return append(out, stepLines("Next steps after the import (not run by skilltri)", steps)...)
}

func previewClaudeLines(r importer.ClaudeResult, rep importer.ClaudeReport, skillsDir, home string) []pvLine {
	names := make([]string, 0, len(r.Sources))
	for n := range r.Sources {
		names = append(names, n)
	}
	sort.Strings(names)
	out := []pvLine{{head: true, text: "Sources (" + itoa(len(rep.Sources)) + " of " + itoa(len(names)) + " imported)"}}
	for _, n := range names {
		s := r.Sources[n]
		what := "marketplace " + s.URL + tildify(s.Path, home)
		var on []string
		for p, node := range s.Plugins {
			if node == nil || !node.Disabled {
				on = append(on, p)
			}
		}
		sort.Strings(on)
		if len(on) > 0 {
			what += " · enabled plugins: " + strings.Join(on, ", ")
		} else {
			what += " · no plugins enabled"
		}
		out = append(out, itemLine(n, what, slices.Contains(rep.Sources, n), slices.Contains(rep.Replaced, n)))
	}
	if rep.LocalAdded {
		out = append(out, pvLine{icon: goodStyle.Render("+"), text: "local: local source for the copied skills (" + tildify(skillsDir, home) + ")", style: lipgloss.NewStyle()})
	}
	if len(names) == 0 {
		out = append(out, pvLine{text: "no marketplaces found", style: dimStyle})
	}
	out = append(out, pvLine{}, pvLine{head: true, text: "Skills to copy (" + itoa(len(rep.Skills)) + " of " + itoa(len(r.UserSkills)) + ")"})
	for _, dir := range r.UserSkills {
		n := filepath.Base(dir)
		out = append(out, itemLine(n, "→ "+tildify(filepath.Join(skillsDir, n), home), slices.Contains(rep.Skills, n), slices.Contains(rep.ReplacedSkills, n)))
	}
	if len(r.UserSkills) == 0 {
		out = append(out, pvLine{text: "no user skills found", style: dimStyle})
	}
	out = append(out, warnLines(rep.Warnings)...)
	return append(out, stepLines("Next steps after the import (not run by skilltri)", tildeAll(rep.Steps, home))...)
}

func tildeAll(steps []string, home string) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = tildeText(s, home)
	}
	return out
}

// importUpdate handles the import messages for the Setup tab.
func (t *setupTab) importUpdate(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case importPreviewMsg:
		if msg.seq != t.readSeq {
			// cancelled with esc (which ended the busy key) or superseded by a newer read (which
			// owns the key now): drop it without touching the key
			return nil
		}
		m.end(importWork(msg.kind))
		if msg.err != nil {
			m.flash("import "+importTitles[msg.kind]+": "+msg.err.Error(), true)
			return nil
		}
		t.imp = &importModal{tab: t, pv: msg}
		m.openModal(t.imp)
	case importDoneMsg:
		m.end(importWork(msg.kind))
		open := t.imp != nil && !t.imp.gone && t.imp.kind() == msg.kind
		if open {
			m.removeModal(t.imp)
		}
		t.imp = nil
		if msg.err != nil {
			m.flash("import "+importTitles[msg.kind]+": "+msg.err.Error(), true)
		} else {
			text := msg.text
			if !open && msg.steps > 0 {
				text += " (" + itoa(msg.steps) + " next steps not shown: the dialog was closed)"
			}
			m.flash(text, false)
		}
		if open {
			title := "Imported " + importTitles[msg.kind]
			if msg.err != nil {
				title = "Import " + importTitles[msg.kind] + " failed"
			}
			m.openModal(&infoModal{title: title, body: msg.body})
		}
		return m.reload()
	}
	return nil
}

// importModal is the preview of an import: y applies it (in a command), f toggles overwriting
// existing items. esc closes it; during the apply that only stops waiting for the result, which
// then lands in the status line.
type importModal struct {
	tab      *setupTab
	pv       importPreviewMsg
	force    bool
	scroll   int
	applying bool
	gone     bool // left the stack
}

func (d *importModal) kind() string { return d.pv.kind }

func (d *importModal) fi() int {
	if d.force {
		return 1
	}
	return 0
}

func (d *importModal) closed(m *Model) { d.gone = true }

func (d *importModal) update(m *Model, msg tea.Msg) (tea.Cmd, bool) {
	k, ok := msg.(tea.KeyMsg)
	if !ok || d.applying {
		return nil, false
	}
	switch k.String() {
	case "y", "Y":
		if d.pv.empty[d.fi()] {
			m.flash("nothing to import "+importTitles[d.kind()], false)
			return nil, true
		}
		return d.apply(m), false
	case "f":
		d.force = !d.force
		d.scroll = 0
	case "j", "down":
		d.scroll = min(d.scroll+1, d.maxScroll(m))
	case "k", "up":
		d.scroll = max(d.scroll-1, 0)
	}
	return nil, false
}

// parts are the head, body and foot lines of the dialog in a box of width bw.
func (d *importModal) parts(m *Model, bw int) (head, body, foot []string) {
	head = []string{dimStyle.Render(ellipsizeMiddle("from "+tildify(san(d.pv.from), m.opts.Home), max(bw-4, 1))), ""}
	body = renderPvLines(d.pv.prev[d.fi()], max(bw-4, 1))
	foot = []string{"", keyHints(d.hints()...)}
	if d.applying {
		foot[1] = warnStyle.Render(spinFrames[m.spinStep%len(spinFrames)]+" importing …") + "  " + keyHints("esc", "stop waiting")
	}
	return head, body, foot
}

func (d *importModal) maxScroll(m *Model) int {
	w, h := modalRoom(m)
	head, body, foot := d.parts(m, min(w, 80))
	avail, _ := bodyRoom(len(head), len(foot), h-2)
	return maxScroll(len(body), avail)
}

// apply runs the import in a command.
func (d *importModal) apply(m *Model) tea.Cmd {
	kind := d.kind()
	if !m.begin(importWork(kind), "importing "+importTitles[kind]+" …") {
		return nil
	}
	d.applying = true
	cfgPath, skillsDir, env, force, pv, home := m.opts.configPath(), m.opts.skillsDir(), m.opts.Env, d.force, d.pv, m.opts.Home
	return func() tea.Msg {
		done := importDoneMsg{kind: kind}
		if kind == importOneMCP {
			a, err := importer.Apply1MCP(cfgPath, pv.one, force)
			done.err = err
			done.text = "imported " + itoa(len(a.Imported)) + " server(s) " + importTitles[kind]
			var body []pvLine
			for _, n := range a.Imported {
				body = append(body, pvLine{icon: goodStyle.Render("+"), text: "imported server " + n, style: lipgloss.NewStyle()})
			}
			if len(body) == 0 {
				body = append(body, pvLine{text: "no servers imported", style: dimStyle})
			}
			body = append(body, warnLines(a.Warnings)...)
			if err == nil {
				body = append(body, stepLines("Next steps (not run by skilltri)", pv.steps)...)
				done.steps = len(pv.steps)
			}
			done.body = body
			return done
		}
		rep, err := importer.ImportClaude(cfgPath, skillsDir, pv.claude, force)
		if err == nil {
			if rerr := env.RefreshCatalog(); rerr != nil {
				rep.Warnings = append(rep.Warnings, rerr.Error())
			}
		}
		done.err = err
		done.text = "imported " + itoa(len(rep.Sources)) + " source(s) and " + itoa(len(rep.Skills)) + " skill(s) " + importTitles[kind]
		var body []pvLine
		for _, n := range rep.Sources {
			body = append(body, pvLine{icon: goodStyle.Render("+"), text: "imported source " + n, style: lipgloss.NewStyle()})
		}
		if rep.LocalAdded {
			body = append(body, pvLine{icon: goodStyle.Render("+"), text: "added the local source local", style: lipgloss.NewStyle()})
		}
		for _, n := range rep.Skills {
			body = append(body, pvLine{icon: goodStyle.Render("+"), text: "copied skill " + n, style: lipgloss.NewStyle()})
		}
		if len(body) == 0 {
			body = append(body, pvLine{text: "nothing imported", style: dimStyle})
		}
		body = append(body, warnLines(rep.Warnings)...)
		body = append(body, stepLines("Next steps (not run by skilltri)", tildeAll(rep.Steps, home))...)
		done.steps = len(rep.Steps)
		done.body = body
		return done
	}
}

func (d *importModal) hints() []string {
	if d.applying {
		return []string{"esc", "stop waiting"}
	}
	f := "overwrite existing"
	if d.force {
		f = "keep existing"
	}
	return []string{"y", "import", "f", f, "↑↓", "scroll", "esc", "cancel"}
}

func (d *importModal) view(m *Model, w, h int) string {
	bw := min(w, 80)
	color := cAccent
	if d.force && d.pv.repl[1] > 0 {
		color = cRed
	}
	head, body, foot := d.parts(m, bw)
	return modalBox("Import "+importTitles[d.kind()], scrollBody(head, body, foot, h-2, d.scroll), bw, color)
}
