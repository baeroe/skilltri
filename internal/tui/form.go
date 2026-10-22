package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// fieldKind is the kind of a form field.
type fieldKind int

const (
	textField   fieldKind = iota // one line of text
	choiceField                  // one of choices, cycled with ←/→ or space
	toggleField                  // on/off, toggled with space or ←/→
	linesField                   // several lines (KEY=value lists): enter adds a line
)

// field is one form field. Errors are set by the form's owner and shown below the field.
type field struct {
	key, label, section string
	kind                fieldKind
	help                string            // dim hint below the field
	input               textinput.Model   // textField
	lines               []textinput.Model // linesField; never empty
	line                int               // focused line of a linesField
	placeholder         string
	choices             []string // choiceField
	choice              int
	on                  bool // toggleField
	err                 string
	hidden              func() bool // nil: always shown
}

// newInput is a text input with a static cursor (a blinking one would need a tick per input).
func newInput(value, placeholder string) textinput.Model {
	ti := textinput.New()
	ti.Cursor.SetMode(cursor.CursorStatic)
	ti.Prompt = ""
	ti.CharLimit = 4096
	ti.Width = 40 // set again when rendering; without a width the placeholder shows only one character
	ti.Placeholder = placeholder
	ti.SetValue(value)
	return ti
}

// value is the trimmed text of a text field, the selected choice or "true"/"false" for a toggle.
func (fl *field) value() string {
	switch fl.kind {
	case choiceField:
		return fl.choices[fl.choice]
	case toggleField:
		if fl.on {
			return "true"
		}
		return "false"
	case linesField:
		return fl.text()
	}
	return strings.TrimSpace(fl.input.Value())
}

// text is the raw content of a lines field, lines joined with "\n".
func (fl *field) text() string {
	var out []string
	for _, l := range fl.lines {
		out = append(out, l.Value())
	}
	return strings.Join(out, "\n")
}

// setLines replaces the content of a lines field.
func (fl *field) setLines(lines []string) {
	if len(lines) == 0 {
		lines = []string{""}
	}
	fl.lines = nil
	for i, l := range lines {
		ph := ""
		if i == 0 {
			ph = fl.placeholder
		}
		fl.lines = append(fl.lines, newInput(l, ph))
	}
	fl.line = min(fl.line, len(fl.lines)-1)
}

func (fl *field) visible() bool { return fl.hidden == nil || !fl.hidden() }

// form is a generic sectioned form for modals: text, choice, toggle and multiline fields with
// inline errors. Its owner (a modal) builds the fields, passes keys to key and validates on submit.
type form struct {
	title   string
	fields  []*field
	focus   int
	err     string   // form-level error (shown above the hints)
	note    string   // dim note shown when there is no error
	busy    string   // set while the owner saves ("saving …"): keys are ignored
	section string   // section of the fields added next
	hints   []string // extra footer pairs of the owner
}

func newForm(title string) *form { return &form{title: title} }

func (f *form) add(fl *field) *field {
	fl.section = f.section
	f.section = ""
	f.fields = append(f.fields, fl)
	if len(f.fields) == 1 {
		f.setFocus(0)
	}
	return fl
}

// startSection makes the next field start a new section titled name.
func (f *form) startSection(name string) { f.section = name }

func (f *form) addText(key, label, value, placeholder string) *field {
	return f.add(&field{key: key, label: label, kind: textField, input: newInput(value, placeholder)})
}

func (f *form) addChoice(key, label string, choices []string, cur string) *field {
	fl := &field{key: key, label: label, kind: choiceField, choices: choices}
	for i, c := range choices {
		if c == cur {
			fl.choice = i
		}
	}
	return f.add(fl)
}

func (f *form) addToggle(key, label string, on bool) *field {
	return f.add(&field{key: key, label: label, kind: toggleField, on: on})
}

func (f *form) addLines(key, label string, lines []string, placeholder string) *field {
	fl := &field{key: key, label: label, kind: linesField, placeholder: placeholder}
	fl.setLines(lines)
	return f.add(fl)
}

// get returns the field key (nil if there is none).
func (f *form) get(key string) *field {
	for _, fl := range f.fields {
		if fl.key == key {
			return fl
		}
	}
	return nil
}

func (f *form) current() *field { return f.fields[f.focus] }

// clearErrors removes every inline and form-level error.
func (f *form) clearErrors() {
	f.err = ""
	for _, fl := range f.fields {
		fl.err = ""
	}
}

// hasErrors reports whether a visible field has an error.
func (f *form) hasErrors() bool {
	for _, fl := range f.fields {
		if fl.err != "" && fl.visible() {
			return true
		}
	}
	return false
}

// focusFirstError moves the focus to the first visible field with an error.
func (f *form) focusFirstError() {
	for i, fl := range f.fields {
		if fl.err != "" && fl.visible() {
			f.setFocus(i)
			return
		}
	}
}

func (f *form) focusKey(key string) {
	for i, fl := range f.fields {
		if fl.key == key {
			f.setFocus(i)
		}
	}
}

func (f *form) setFocus(i int) {
	for _, fl := range f.fields {
		fl.input.Blur()
		for j := range fl.lines {
			fl.lines[j].Blur()
		}
	}
	f.focus = i
	fl := f.fields[i]
	switch fl.kind {
	case textField:
		fl.input.Focus()
		fl.input.CursorEnd()
	case linesField:
		fl.line = min(max(fl.line, 0), len(fl.lines)-1)
		fl.lines[fl.line].Focus()
		fl.lines[fl.line].CursorEnd()
	}
}

// move focuses the next (delta 1) or previous visible field, wrapping; it enters a lines field on
// its first line going down and on its last line going up.
func (f *form) move(delta int) {
	n := len(f.fields)
	i := f.focus
	for range n {
		i = (i + delta + n) % n
		if f.fields[i].visible() {
			break
		}
	}
	if fl := f.fields[i]; fl.kind == linesField {
		fl.line = 0
		if delta < 0 {
			fl.line = len(fl.lines) - 1
		}
	}
	f.setFocus(i)
}

// lastVisible reports whether the focus is on the last visible field.
func (f *form) lastVisible() bool {
	for i := f.focus + 1; i < len(f.fields); i++ {
		if f.fields[i].visible() {
			return false
		}
	}
	return true
}

// key handles a key; submit=true means the user asked to save (ctrl+s, or enter on the last field).
func (f *form) key(k tea.KeyMsg) (cmd tea.Cmd, submit bool) {
	if f.busy != "" {
		return nil, false
	}
	fl := f.current()
	switch k.String() {
	case "ctrl+s":
		return nil, true
	case "tab":
		f.move(1)
		return nil, false
	case "shift+tab":
		f.move(-1)
		return nil, false
	}
	switch fl.kind {
	case linesField:
		return f.linesKey(fl, k), false
	case choiceField, toggleField:
		switch k.String() {
		case "down", "enter":
			if k.String() == "enter" && f.lastVisible() {
				return nil, true
			}
			f.move(1)
		case "up":
			f.move(-1)
		case "left", "h":
			f.cycle(fl, -1)
		case "right", "l", " ":
			f.cycle(fl, 1)
		}
		return nil, false
	}
	switch k.String() {
	case "down":
		f.move(1)
		return nil, false
	case "up":
		f.move(-1)
		return nil, false
	case "enter":
		if f.lastVisible() {
			return nil, true
		}
		f.move(1)
		return nil, false
	}
	fl.input, cmd = fl.input.Update(k)
	return cmd, false
}

func (f *form) cycle(fl *field, delta int) {
	if fl.kind == toggleField {
		fl.on = !fl.on
		return
	}
	n := len(fl.choices)
	fl.choice = (fl.choice + delta + n) % n
}

// linesKey edits a lines field: ↑/↓ move between lines (and out of the field at its ends), enter
// inserts a line after the current one, backspace on an empty line removes it.
func (f *form) linesKey(fl *field, k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "up":
		if fl.line == 0 {
			f.move(-1)
			return nil
		}
		fl.line--
		f.setFocus(f.focus)
		return nil
	case "down":
		if fl.line == len(fl.lines)-1 {
			f.move(1)
			return nil
		}
		fl.line++
		f.setFocus(f.focus)
		return nil
	case "enter":
		fl.lines = append(fl.lines[:fl.line+1], append([]textinput.Model{newInput("", "")}, fl.lines[fl.line+1:]...)...)
		fl.line++
		f.setFocus(f.focus)
		return nil
	case "backspace":
		if fl.lines[fl.line].Value() == "" && len(fl.lines) > 1 {
			fl.lines = append(fl.lines[:fl.line], fl.lines[fl.line+1:]...)
			fl.line = max(fl.line-1, 0)
			f.setFocus(f.focus)
			return nil
		}
	}
	var cmd tea.Cmd
	fl.lines[fl.line], cmd = fl.lines[fl.line].Update(k)
	return cmd
}

// --- rendering ------------------------------------------------------------------------------

// choiceView renders the options of a choice field, the current one marked.
func choiceView(opts []string, cur int, focused bool) string {
	var parts []string
	for i, o := range opts {
		switch {
		case i == cur && focused:
			parts = append(parts, selStyle.Render("● "+o))
		case i == cur:
			parts = append(parts, boldStyle.Render("● "+o))
		default:
			parts = append(parts, dimStyle.Render("○ "+o))
		}
	}
	return strings.Join(parts, "  ")
}

// formDims are the box width, its inner width, the label width and the input width of a form
// shown in a body w cells wide.
func formDims(w int) (fw, iw, lw, inW int) {
	fw = min(w, 84)
	iw = max(fw-4, 1)
	lw = min(14, max(iw/3, 4))
	inW = max(iw-2-lw, 4)
	return fw, iw, lw, inW
}

// layout sizes the inputs for a body w cells wide; the owner calls it before passing keys (view
// does not change state, it only cuts what does not fit).
func (f *form) layout(w int) {
	_, _, _, inW := formDims(w)
	for _, fl := range f.fields {
		fl.input.Width = max(inW-1, 1)
		for j := range fl.lines {
			fl.lines[j].Width = max(inW-1, 1)
		}
	}
}

// view renders the form as a modal box of at most w x h cells; it scrolls to keep the focused field
// visible.
func (f *form) view(w, h int) string {
	fw, iw, lw, inW := formDims(w)
	indent := strings.Repeat(" ", lw+2)
	var lines []string
	focusLine, focusEnd := 0, 0
	first := true
	for i, fl := range f.fields {
		if !fl.visible() {
			continue
		}
		if fl.section != "" {
			if !first {
				lines = append(lines, "")
			}
			lines = append(lines, accentStyle.Bold(true).Render(fit(fl.section, iw)))
		}
		first = false
		focused := i == f.focus
		label, mark := labelStyle.Render(fit(fl.label, lw)), "  "
		if focused {
			label, mark = selStyle.Render(fit(fl.label, lw)), selStyle.Render("▸ ")
			focusLine = len(lines)
		}
		switch fl.kind {
		case choiceField:
			lines = append(lines, mark+label+ansi.Truncate(choiceView(fl.choices, fl.choice, focused), inW, "…"))
		case toggleField:
			box := "[ ] no"
			if fl.on {
				box = "[x] yes"
			}
			if focused {
				box = selStyle.Render(box)
			}
			lines = append(lines, mark+label+box)
		case linesField:
			for j := range fl.lines {
				v := ansi.Truncate(fl.lines[j].View(), inW, "")
				if j == 0 {
					lines = append(lines, mark+label+v)
					continue
				}
				if focused && j == fl.line {
					v = selStyle.Render("▸") + " " + ansi.Truncate(fl.lines[j].View(), max(inW-2, 1), "")
					lines = append(lines, strings.Repeat(" ", lw)+"  "+v)
					continue
				}
				lines = append(lines, indent+v)
			}
		default:
			lines = append(lines, mark+label+ansi.Truncate(fl.input.View(), inW, ""))
		}
		if fl.err != "" {
			for _, l := range wrap("✗ "+fl.err, inW) {
				lines = append(lines, indent+badStyle.Render(l))
			}
		} else if fl.help != "" && focused {
			for _, l := range wrap(fl.help, inW) {
				lines = append(lines, indent+dimStyle.Render(l))
			}
		}
		if focused {
			focusEnd = len(lines) - 1
		}
	}
	foot := []string{""}
	switch {
	case f.busy != "":
		foot = append(foot, warnStyle.Render(fit(f.busy, iw)))
	case f.err != "":
		for _, l := range wrap("✗ "+f.err, iw) {
			foot = append(foot, badStyle.Render(l))
		}
	case f.note != "":
		for _, l := range wrap(f.note, iw) {
			foot = append(foot, dimStyle.Render(l))
		}
	}
	room := h - 2 - len(foot)
	if room < 1 { // tiny terminal: keep the fields, drop the footer
		foot, room = nil, max(h-2, 1)
	}
	if len(lines) > room {
		start := max(0, focusLine-room/2)
		if focusEnd >= start+room { // keep the focused field's error visible
			start = min(focusEnd-room+1, focusLine)
		}
		start = min(start, len(lines)-room)
		lines = lines[start : start+room]
	}
	return modalBox(f.title, append(lines, foot...), fw, cAccent)
}
