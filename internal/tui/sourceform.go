package tui

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/baeroe/skilltri/internal/actions"
	"github.com/baeroe/skilltri/internal/config"
)

// --- source form ----------------------------------------------------------------------------

// sourceForm adds (orig == "") or edits the skill source orig. Sources cannot be renamed, so the
// name field is hidden when editing.
type sourceForm struct {
	orig string
	form *form
}

// sourceSavedMsg is the result of saving f.
type sourceSavedMsg struct {
	f    *sourceForm
	name string
	add  bool
	err  error
}

func newSourceForm(name string, s *config.Source, home string) *sourceForm {
	sf := &sourceForm{orig: name}
	title := "New source"
	if s == nil {
		s = &config.Source{Type: "local"}
	} else {
		title = "Edit source " + oneLine(name)
	}
	f := newForm(title)
	sf.form = f
	f.startSection("Source")
	nf := f.addText("name", "Name", name, "letters, digits, '-'")
	nf.hidden = func() bool { return sf.orig != "" }
	typ := f.addChoice("type", "Type", []string{"local", "git", "marketplace"}, s.Type)
	is := func(types ...string) func() bool {
		return func() bool {
			for _, t := range types {
				if typ.value() == t {
					return false
				}
			}
			return true
		}
	}
	p := f.addPath("path", "Path", tildify(s.Path, home), "absolute folder (or ~/…)", home)
	p.hidden = is("local", "marketplace")
	p.help = "tab completes folders; marketplace: a path or a url"
	u := f.addText("url", "URL", s.URL, "owner/repo or a git URL")
	u.hidden = is("git", "marketplace")
	u.help = "marketplace: a path or a url"
	f.startSection("Checkout")
	r := f.addText("ref", "Ref", s.Ref, "branch, tag or commit (empty: default branch)")
	r.hidden = is("git", "marketplace")
	sub := f.addText("subpath", "Subpath", s.Subpath, "folder inside the checkout")
	sub.hidden = is("git", "marketplace")
	ev := f.addText("every", "Update every", s.UpdateEvery, "1h (0: manual only)")
	ev.hidden = is("git", "marketplace")
	ev.help = "a duration like 30m or 6h; 0 updates only with u/U"
	cur := "enabled"
	if s.PluginsDefault == config.PluginsDisabled {
		cur = "disabled"
	}
	pl := f.addChoice("plugins", "Plugins", []string{"enabled", "disabled"}, cur)
	pl.hidden = is("marketplace")
	pl.help = "whether plugins are on unless set one by one (space in the tree)"
	return sf
}

func (sf *sourceForm) formOf() *form                  { return sf.form }
func (sf *sourceForm) view(m *Model, w, h int) string { return sf.form.view(w, h) }
func (sf *sourceForm) hints() []string {
	return []string{"ctrl+s", "save", "tab", "next field", "←→", "choose", "esc", "cancel"}
}

// workKey is the busy key (Model.begin) of saving this form.
func (sf *sourceForm) workKey() string { return fmt.Sprintf("sourceform:%p", sf) }

func (sf *sourceForm) update(m *Model, msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if sf.form.busy != "" {
			return nil, false
		}
		sf.form.layout(m.width)
		cmd, submit := sf.form.key(msg)
		if submit {
			return sf.save(m), false
		}
		return cmd, false
	case sourceSavedMsg:
		if msg.f != sf {
			return nil, false
		}
		sf.form.busy = ""
		switch {
		case msg.err == nil:
			return nil, true
		case errors.Is(msg.err, actions.ErrExists):
			sf.form.get("name").err = msg.err.Error()
			sf.form.focusKey("name")
		case errors.Is(msg.err, actions.ErrNotFound):
			sf.form.err = "source " + sf.orig + " no longer exists (esc discards the form)"
		default:
			sf.form.err = msg.err.Error()
		}
	}
	return nil, false
}

// absPath expands ~ and reports whether p is absolute afterwards.
func absPath(p, home string) (string, bool) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		p = filepath.Join(home, p[1:])
	}
	if !filepath.IsAbs(p) {
		return p, false
	}
	return filepath.Clean(p), true
}

// build validates the fields (inline errors) and returns the source; ok=false when invalid.
func (sf *sourceForm) build(home string) (name string, s config.Source, ok bool) {
	f := sf.form
	f.clearErrors()
	name = sf.orig
	if name == "" {
		if name = f.get("name").value(); !config.ValidServerName(name) {
			f.get("name").err = "use letters, digits and '-' (max 32 characters, starting with a letter or digit)"
		}
	}
	s.Type = f.get("type").value()
	path, url := f.get("path").value(), f.get("url").value()
	switch s.Type {
	case "local":
		if path == "" {
			f.get("path").err = "required for local sources"
		}
		url = ""
	case "git":
		if url == "" {
			f.get("url").err = "required for git sources"
		}
		path = ""
	case "marketplace":
		switch {
		case path != "" && url != "":
			f.get("url").err = "a path or a url, not both"
		case path == "" && url == "":
			f.get("url").err = "enter a path or a url"
		}
	}
	if path != "" {
		p, abs := absPath(path, home)
		if !abs {
			f.get("path").err = "must be an absolute path (or start with ~/)"
		}
		s.Path = p
	}
	s.URL = url
	if s.Type != "local" {
		s.Ref = f.get("ref").value()
		if s.Subpath = f.get("subpath").value(); s.Subpath != "" && !filepath.IsLocal(filepath.FromSlash(s.Subpath)) {
			f.get("subpath").err = "a relative folder inside the checkout"
		}
		if s.UpdateEvery = f.get("every").value(); s.UpdateEvery != "" && s.UpdateEvery != "0" {
			if d, err := time.ParseDuration(s.UpdateEvery); err != nil || d < 0 {
				f.get("every").err = "a duration like 30m or 6h (0: manual updates only)"
			}
		}
	}
	if s.Type == "marketplace" && f.get("plugins").value() == "disabled" {
		s.PluginsDefault = config.PluginsDisabled
	}
	if f.hasErrors() {
		f.focusFirstError()
		return "", s, false
	}
	check := config.Config{Version: 1, Sources: map[string]*config.Source{name: &s}}
	if err := check.Validate(); err != nil {
		f.err = err.Error()
		return "", s, false
	}
	return name, s, true
}

// save validates and, when valid, writes the source in a command; the result is a sourceSavedMsg.
func (sf *sourceForm) save(m *Model) tea.Cmd {
	name, s, ok := sf.build(m.opts.Home)
	if !ok {
		return nil
	}
	env, orig := m.opts.Env, sf.orig
	if !m.begin(sf.workKey(), "saving source "+name+" …") {
		return nil
	}
	sf.form.busy = "saving …"
	return func() tea.Msg {
		msg := sourceSavedMsg{f: sf, name: name, add: orig == ""}
		if orig == "" {
			msg.err = actions.SourceAdd(env, name, s)
		} else {
			msg.err = actions.SourceUpdate(env, name, s)
		}
		return msg
	}
}

// --- name prompt ----------------------------------------------------------------------------

// namePrompt asks for one name (new skill, server name of a plugin server). check returns an
// inline error for an invalid name; run starts the work for a valid one in a command that answers
// with a promptDoneMsg for this prompt. The prompt stays open (showing the error at the field)
// until the work succeeded; while it runs, esc is ignored so its result is not lost.
type namePrompt struct {
	form  *form
	busy  string
	check func(name string) string
	run   func(name string) tea.Cmd
}

// promptDoneMsg is the result of a namePrompt's work: err is shown at the field; on success the
// prompt closes and then (if set) runs.
type promptDoneMsg struct {
	p    *namePrompt
	err  error
	then func(m *Model) tea.Cmd
}

func newNamePrompt(title, label, value, placeholder, busy string) *namePrompt {
	f := newForm(title)
	f.addText("name", label, value, placeholder)
	return &namePrompt{form: f, busy: busy}
}

func (p *namePrompt) formOf() *form                  { return p.form }
func (p *namePrompt) view(m *Model, w, h int) string { return p.form.view(w, h) }
func (p *namePrompt) hints() []string                { return []string{"enter", "ok", "esc", "cancel"} }
func (p *namePrompt) ownsEsc() bool                  { return p.form.busy != "" }
func (p *namePrompt) workKey() string                { return fmt.Sprintf("prompt:%p", p) }

func (p *namePrompt) update(m *Model, msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		f := p.form
		if f.busy != "" {
			return nil, false
		}
		f.layout(m.width)
		cmd, submit := f.key(msg)
		if !submit {
			return cmd, false
		}
		f.clearErrors()
		name := f.get("name").value()
		if e := p.check(name); e != "" {
			f.get("name").err = e
			return nil, false
		}
		if !m.begin(p.workKey(), p.busy) {
			return nil, false
		}
		f.busy = p.busy
		return p.run(name), false
	case promptDoneMsg:
		if msg.p != p {
			return nil, false
		}
		m.end(p.workKey())
		p.form.busy = ""
		if msg.err != nil {
			p.form.get("name").err = msg.err.Error()
			return nil, false
		}
		if msg.then != nil {
			return msg.then(m), true
		}
		return nil, true
	}
	return nil, false
}
