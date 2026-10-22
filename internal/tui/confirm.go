package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// confirmModal asks before an action: y (or enter, unless onlyY) runs it, any other key cancels.
type confirmModal struct {
	title string
	body  []string // already styled lines; long lines are cut to the box
	yes   string   // what y does ("remove", "accept")
	onlyY bool     // only y confirms (destructive actions), not enter
	color lipgloss.Color
	run   func(m *Model) tea.Cmd
}

// confirmDanger is a confirmation of a destructive action (red box).
func confirmDanger(title string, body []string, yes string, run func(m *Model) tea.Cmd) *confirmModal {
	return &confirmModal{title: title, body: body, yes: yes, onlyY: true, color: cRed, run: run}
}

func (c *confirmModal) update(m *Model, msg tea.Msg) (tea.Cmd, bool) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil, false
	}
	switch k.String() {
	case "y", "Y":
		return c.run(m), true
	case "enter":
		if !c.onlyY {
			return c.run(m), true
		}
	}
	return nil, true
}

func (c *confirmModal) hints() []string { return []string{"y", c.yes, "any other key", "cancel"} }

func (c *confirmModal) view(m *Model, w, h int) string {
	bw := min(w, 72)
	lines := append([]string(nil), c.body...)
	if room := h - 4; len(lines) > room && room > 0 { // keep the key line
		lines = append(lines[:max(room-1, 0)], dimStyle.Render("…"))
	}
	lines = append(lines, "", keyHints("y", c.yes, "any other key", "cancel"))
	if len(lines)+2 > h {
		lines = lines[len(lines)+2-max(h, 3):]
	}
	return modalBox(c.title, lines, bw, c.color)
}
