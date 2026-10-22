package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Shared helpers of scrollable dialogs: logical lines that wrap when rendered, a body that scrolls
// between a fixed head and foot, and infoModal, a scrollable text.

// pvLine is one logical line of a dialog, wrapped when rendered.
type pvLine struct {
	head  bool   // a bold section title
	icon  string // styled one-cell icon ("" = indent)
	text  string
	style lipgloss.Style
}

func renderPvLines(ls []pvLine, w int) []string {
	var out []string
	for _, l := range ls {
		switch {
		case l.head:
			out = append(out, boldStyle.Render(oneLine(l.text)))
			continue
		case l.text == "":
			out = append(out, "")
			continue
		}
		for i, part := range wrap(l.text, max(w-2, 1)) {
			prefix := "  "
			if i == 0 && l.icon != "" {
				prefix = l.icon + " "
			}
			out = append(out, prefix+l.style.Render(part))
		}
	}
	return out
}

// bodyRoom is how many body lines fit into room lines next to head and foot lines; the head is
// dropped (keepHead false) when not even one body line fits next to it.
func bodyRoom(nHead, nFoot, room int) (avail int, keepHead bool) {
	if avail = room - nHead - nFoot; avail >= 1 {
		return avail, true
	}
	return room - nFoot, false
}

// maxScroll is the largest useful scroll offset of nBody lines in avail lines (the last visible
// line is then the "more" marker's place).
func maxScroll(nBody, avail int) int {
	if nBody <= avail || avail < 1 {
		return 0
	}
	return nBody - avail + 1
}

// scrollBody fits head, the body scrolled by scroll and foot into room lines (the body gives way
// first). It does not change scroll (views do not mutate state; update clamps it).
func scrollBody(head, body, foot []string, room, scroll int) []string {
	avail, keep := bodyRoom(len(head), len(foot), room)
	if !keep {
		head = nil
	}
	if avail < 1 {
		return foot[max(len(foot)-max(room, 1), 0):]
	}
	if len(body) > avail {
		scroll = min(max(scroll, 0), maxScroll(len(body), avail))
		end := min(scroll+avail-1, len(body))
		body = append(append([]string(nil), body[scroll:end]...), dimStyle.Render("… ↑↓ scroll ("+itoa(len(body)-end)+" more)"))
	}
	return append(append(append([]string(nil), head...), body...), foot...)
}

// modalRoom is the body size the Model gives modals (as in Model.View).
func modalRoom(m *Model) (w, h int) {
	w, h = m.width, m.height
	if w <= 0 || h <= 0 {
		w, h = 100, 30
	}
	return w, max(h-2, 1)
}

// infoModal shows a scrollable text (e.g. an import result with its next steps); enter or esc
// closes it.
type infoModal struct {
	title  string
	body   []pvLine
	scroll int
}

func (d *infoModal) update(m *Model, msg tea.Msg) (tea.Cmd, bool) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil, false
	}
	switch k.String() {
	case "enter", "q":
		return nil, true
	case "j", "down":
		w, h := modalRoom(m)
		avail, _ := bodyRoom(0, 2, h-2)
		d.scroll = min(d.scroll+1, maxScroll(len(renderPvLines(d.body, max(min(w, 80)-4, 1))), avail))
	case "k", "up":
		d.scroll = max(d.scroll-1, 0)
	}
	return nil, false
}

func (d *infoModal) hints() []string { return []string{"↑↓", "scroll", "enter esc", "close"} }

func (d *infoModal) view(m *Model, w, h int) string {
	bw := min(w, 80)
	body := renderPvLines(d.body, max(bw-4, 1))
	foot := []string{"", keyHints(d.hints()...)}
	return modalBox(d.title, scrollBody(nil, body, foot, h-2, d.scroll), bw, cAccent)
}
