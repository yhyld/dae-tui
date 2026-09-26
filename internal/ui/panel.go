package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// TitledBox wraps content in a rounded border with the title embedded in
// the top edge — the home page's zoning primitive (one box = one
// self-contained zone, btop-style). Every returned line is exactly w cells
// wide: content lines are truncated ANSI-aware and padded, so boxes can be
// joined into rows without ragged edges. focused lights up title and border
// for the zone that currently owns the keyboard.
func TitledBox(title string, focused bool, w int, lines []string) []string {
	if w < 12 {
		w = 12
	}
	inner := w - 4 // beside each border column sits one padding space
	border := lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	tstyle := lipgloss.NewStyle().Foreground(DimText)
	if focused {
		tstyle = TitleStyle
	}

	// ╭─ title ──────╮ — the title may have to shrink, never the frame.
	tw := lipgloss.Width(title)
	if tw > inner-4 {
		title = Truncate(title, inner-4)
		tw = lipgloss.Width(title)
	}
	top := border.Render("╭─ ") + tstyle.Render(title) + " " +
		border.Render(strings.Repeat("─", w-5-tw)+"╮")

	out := make([]string, 0, len(lines)+2)
	out = append(out, top)
	for _, l := range lines {
		out = append(out, border.Render("│ ")+PadRight(Truncate(l, inner), inner)+border.Render(" │"))
	}
	out = append(out, border.Render("╰"+strings.Repeat("─", w-2)+"╯"))
	return out
}

// JoinBoxes places two equal-height line blocks side by side with a
// one-space gutter, padding the shorter one with blank rows — the row-wise
// pairing the boxed zones compose with. Every row ends up the same width
// so stacked rows stay flush.
func JoinBoxes(left, right []string) []string {
	n := len(left)
	if len(right) > n {
		n = len(right)
	}
	lw, rw := 0, 0
	for _, l := range left {
		if w := lipgloss.Width(l); w > lw {
			lw = w
		}
	}
	for _, l := range right {
		if w := lipgloss.Width(l); w > rw {
			rw = w
		}
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		l, r := strings.Repeat(" ", lw), strings.Repeat(" ", rw)
		if i < len(left) {
			l = PadRight(left[i], lw)
		}
		if i < len(right) {
			r = PadRight(right[i], rw)
		}
		out[i] = l + " " + r
	}
	return out
}
