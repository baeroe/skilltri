package tui

import tea "github.com/charmbracelet/bubbletea"

// globalKeys are the keys of every tab (pairs: key, description).
var globalKeys = []string{
	"tab ⇧tab", "next / previous tab",
	"1-5", "jump to a tab",
	"↑↓ j k", "move",
	"R", "reload config, catalog and status",
	"?", "this help",
	"esc", "close the dialog; on its own tab: cancel an update, doctor run or import read",
	"q ctrl+c", "quit",
}

// helpModal is the "?" overlay: global keys and the active tab's keys, scrollable.
type helpModal struct{ scroll int }

func (h *helpModal) update(m *Model, msg tea.Msg) (tea.Cmd, bool) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil, false
	}
	switch k.String() {
	case "?", "esc", "q":
		return nil, true
	case "down", "j":
		h.scroll++
	case "up", "k":
		h.scroll = max(h.scroll-1, 0)
	}
	return nil, false
}

func (h *helpModal) hints() []string { return []string{"↑↓", "scroll", "esc", "close"} }

func (h *helpModal) view(m *Model, w, ht int) string {
	bw := min(w, 64)
	kw := min(14, max(bw/3, 4))
	section := func(title string, pairs []string) []string {
		l := []string{boldStyle.Render(title)}
		for i := 0; i+1 < len(pairs); i += 2 {
			l = append(l, keyStyle.Render(fit(pairs[i], kw))+dimStyle.Render(pairs[i+1]))
		}
		return l
	}
	lines := section("Everywhere", globalKeys)
	if th := m.tabs[m.tab].hints(); len(th) > 0 {
		lines = append(lines, "")
		lines = append(lines, section(m.tabs[m.tab].title(), th)...)
	}
	title := "Keys"
	if room := ht - 2; len(lines) > room && room > 0 {
		h.scroll = min(max(h.scroll, 0), len(lines)-room)
		lines = lines[h.scroll : h.scroll+room]
		title = "Keys (↑↓ scroll)"
	}
	return modalBox(title, lines, bw, cAccent)
}
