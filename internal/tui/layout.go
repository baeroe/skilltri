package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// wideMin is the terminal width from which tabs show two panes (list + details).
const wideMin = 90

// --- width-safe string helpers ---------------------------------------------------------

// width is the display width (grapheme aware, ANSI escapes ignored).
func width(s string) int { return ansi.StringWidth(s) }

// fit truncates s to w cells (with an ellipsis) and pads it with spaces to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if width(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	if d := w - width(s); d > 0 {
		s += strings.Repeat(" ", d)
	}
	return s
}

// padLeft right-aligns s in w cells.
func padLeft(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if width(s) > w {
		return ansi.Truncate(s, w, "…")
	}
	return strings.Repeat(" ", w-width(s)) + s
}

// ellipsizeMiddle shortens plain text to at most w cells by cutting out the middle (good for
// paths). It counts display cells, so wide characters never push it past w.
func ellipsizeMiddle(s string, w int) string {
	sw := width(s)
	if sw <= w || w < 5 {
		return ansi.Truncate(s, w, "…")
	}
	keepR := (w - 1) * 2 / 3
	keepL := w - 1 - keepR
	right := []rune(ansi.TruncateLeft(s, sw-keepR, ""))
	for len(right) > 0 && width(string(right)) > keepR {
		right = right[1:] // a wide character straddled the cut
	}
	return ansi.Truncate(s, keepL, "") + "…" + string(right)
}

// tildify shows paths under home (Options.Home) with ~.
func tildify(p, home string) string {
	home = strings.TrimSuffix(home, string(filepath.Separator))
	if home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}

// san makes external text (names, paths, errors) safe to print: ANSI escapes are removed, tabs
// become spaces and other control characters are shown as "?". Newlines are kept.
func san(s string) string {
	s = ansi.Strip(s)
	if !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 && r != '\n' || r == 0x7f || (r >= 0x80 && r < 0xa0) }) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteString("    ")
		case r == '\n':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			b.WriteRune('?')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// oneLine is san plus all whitespace collapsed (for table cells).
func oneLine(s string) string { return strings.Join(strings.Fields(san(s)), " ") }

// wrap breaks plain text into lines of at most w cells (on spaces when possible).
func wrap(s string, w int) []string {
	if w <= 0 {
		return nil
	}
	s = san(s)
	var out []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			for width(word) > w {
				if line != "" {
					out = append(out, line)
					line = ""
				}
				cut := ansi.Truncate(word, w, "")
				out = append(out, cut)
				word = string([]rune(word)[len([]rune(cut)):])
			}
			switch {
			case line == "":
				line = word
			case width(line)+1+width(word) <= w:
				line += " " + word
			default:
				out = append(out, line)
				line = word
			}
		}
		out = append(out, line)
	}
	return out
}

// highlightRow renders a selected row with a background over the full width.
func highlightRow(line string, w int) string {
	line = fit(line, w)
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+selectedBG)
	line = strings.ReplaceAll(line, "\x1b[m", "\x1b[m"+selectedBG)
	return selectedBG + line + "\x1b[0m"
}

// --- time formatting ---------------------------------------------------------------------

// ago is t relative to now ("now", "30s ago", "5m ago", "3h ago", "3d ago", "never").
func ago(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	switch {
	case d < 10*time.Second:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// --- panels and overlays -------------------------------------------------------------------

// panel draws a rounded box of exactly w x h cells with title (and optional right title) in the top
// border. lines beyond h-2 are cut; each line is fitted to the inner width.
func panel(title string, lines []string, w, h int, focused bool, right string) string {
	if w < 6 || h < 2 {
		return ""
	}
	bc := lipgloss.NewStyle().Foreground(cGray)
	tc := boldStyle
	if focused {
		bc = lipgloss.NewStyle().Foreground(cAccent)
		tc = selStyle
	}
	inner := w - 2
	t := ""
	if title != "" {
		t = " " + ansi.Truncate(title, inner-4, "…") + " "
	}
	rt := ""
	if right != "" {
		rt = " " + right + " "
		if 2+width(t)+width(rt) > inner {
			rt = ""
		}
	}
	fill := max(inner-2-width(t)-width(rt), 0)
	var b strings.Builder
	b.WriteString(bc.Render("╭─") + tc.Render(t) + bc.Render(strings.Repeat("─", fill)) + rt + bc.Render("─╮") + "\n")
	for i := 0; i < h-2; i++ {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		b.WriteString(bc.Render("│") + " " + fit(l, inner-2) + " " + bc.Render("│") + "\n")
	}
	b.WriteString(bc.Render("╰" + strings.Repeat("─", inner) + "╯"))
	return b.String()
}

// hjoin puts blocks side by side (each block has fixed-width lines).
func hjoin(blocks ...string) string { return lipgloss.JoinHorizontal(lipgloss.Top, blocks...) }

// overlay draws fg centered over bg (w x h); the background is dimmed and stripped of colors.
func overlay(bg, fg string, w, h int) string {
	src := strings.Split(bg, "\n")
	plain := make([]string, h)
	out := make([]string, h)
	for i := 0; i < h; i++ {
		l := ""
		if i < len(src) {
			l = src[i]
		}
		plain[i] = fit(ansi.Strip(l), w)
		out[i] = dimStyle.Render(plain[i])
	}
	fl := strings.Split(fg, "\n")
	fw := 0
	for _, l := range fl {
		fw = max(fw, width(l))
	}
	fw = min(fw, w)
	x := max(0, (w-fw)/2)
	y := max(0, (h-len(fl))/2)
	for i, l := range fl {
		row := y + i
		if row >= h {
			break
		}
		left := fit(ansi.Truncate(plain[row], x, ""), x)
		right := padLeft(ansi.TruncateLeft(plain[row], x+fw, ""), w-x-fw)
		out[row] = dimStyle.Render(left) + fit(l, fw) + dimStyle.Render(right)
	}
	return strings.Join(out, "\n")
}

// modalBox draws a bordered box of width w for overlays.
func modalBox(title string, lines []string, w int, color lipgloss.Color) string {
	h := len(lines) + 2
	bc := lipgloss.NewStyle().Foreground(color)
	inner := w - 2
	t := " " + ansi.Truncate(title, max(inner-3, 0), "…") + " "
	fill := max(inner-1-width(t), 0)
	var b strings.Builder
	b.WriteString(bc.Render("╭─") + lipgloss.NewStyle().Bold(true).Foreground(color).Render(t) + bc.Render(strings.Repeat("─", fill)+"╮") + "\n")
	for i := 0; i < h-2; i++ {
		b.WriteString(bc.Render("│") + " " + fit(lines[i], inner-2) + " " + bc.Render("│") + "\n")
	}
	b.WriteString(bc.Render("╰" + strings.Repeat("─", inner) + "╯"))
	return b.String()
}

// keyHints renders "k desc  k desc".
func keyHints(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, keyStyle.Render(pairs[i])+" "+dimStyle.Render(pairs[i+1]))
	}
	return strings.Join(parts, "  ")
}

// kv renders a label column of width lw and a value.
func kv(label, value string, lw int) string {
	return labelStyle.Render(fit(label, lw)) + value
}

// clampLines makes s exactly h lines of at most w cells.
func clampLines(s string, w, h int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	for i, l := range lines {
		if width(l) > w {
			lines[i] = fit(l, w)
		}
	}
	return strings.Join(lines, "\n")
}
