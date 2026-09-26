package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// TitledBox wraps content in a rounded border with the title embedded in
// the top edge — the shared zoning primitive (one box = one self-contained
// zone, btop-style). Every returned line is exactly w cells wide: content
// lines are truncated ANSI-aware and padded, so boxes can be joined into
// rows without ragged edges. focused lights up title and border for the
// zone that currently owns the keyboard.
func TitledBox(title string, focused bool, w int, lines []string) []string {
	return titledBox(title, "", "", focused, w, lines)
}

// TitledBoxRight is TitledBox with a second, right-aligned segment in the
// top edge — the header box rides the latency-test progress there. The
// segment is dropped when the edge has no room for it; an edge must never
// wrap onto a second row.
func TitledBoxRight(title, right string, focused bool, w int, lines []string) []string {
	return titledBox(title, right, "", focused, w, lines)
}

// TitledBoxFooter is TitledBox with key hints embedded in the bottom edge:
// a box's own keys ride its own border (the same law that puts the page
// keys on the app frame's bottom edge), instead of stealing a content row.
func TitledBoxFooter(title, footer string, focused bool, w int, lines []string) []string {
	return titledBox(title, "", footer, focused, w, lines)
}

func titledBox(title, right, footer string, focused bool, w int, lines []string) []string {
	if w < 12 {
		w = 12
	}
	inner := w - 4 // beside each border column sits one padding space
	border := lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	tstyle := lipgloss.NewStyle().Foreground(DimText)
	if focused {
		// The focused box lights title and border alike: with every zone
		// boxed, the border is the strongest "this one owns the keyboard"
		// signal there is.
		border = lipgloss.NewStyle().Foreground(Accent)
		tstyle = TitleStyle
	}

	// ╭─ title ──────╮ — the title may have to shrink, never the frame.
	tw := lipgloss.Width(title)
	if tw > inner-4 {
		title = Truncate(title, inner-4)
		tw = lipgloss.Width(title)
	}
	top := border.Render("╭─ ") + tstyle.Render(title) + " "
	if dashes := w - 8 - tw - lipgloss.Width(right); right != "" && dashes >= 1 {
		top += border.Render(strings.Repeat("─", dashes)+" ") + right + border.Render(" ─╮")
	} else {
		top += border.Render(strings.Repeat("─", w-5-tw) + "╮")
	}

	out := make([]string, 0, len(lines)+2)
	out = append(out, top)
	for _, l := range lines {
		out = append(out, border.Render("│ ")+PadRight(Truncate(l, inner), inner)+border.Render(" │"))
	}
	// ╰─ footer ────╯ — the footer may shrink, the edge never wraps.
	bottom := border.Render("╰" + strings.Repeat("─", w-2) + "╯")
	if footer != "" {
		footer = Truncate(footer, w-6)
		if fw := lipgloss.Width(footer); fw > 0 {
			bottom = border.Render("╰─ ") + HelpStyle.Render(footer) +
				border.Render(" "+strings.Repeat("─", w-5-fw)+"╯")
		}
	}
	out = append(out, bottom)
	return out
}

// PaneSpec describes one pane of a master/detail page for PaneRow: a box
// W cells wide (borders included) whose content the page has already
// windowed; H is the box's outer height (0 sizes the box to its content,
// pairing it only with the other side's height).
type PaneSpec struct {
	Title   string
	Lines   []string
	Focused bool
	W       int
	H       int
	// Footer embeds the pane's own key hints in its bottom edge: keys that
	// only work while this pane holds focus belong here, not in the app
	// frame's edge (which carries page-wide and global keys only).
	Footer string
}

// PaneRow builds a page's master/detail pair: both sides padded to the
// same content height, wrapped in TitledBoxes and joined with a one-space
// gutter — the same grid language as the home page's paired zones, so the
// two boxes always share their top and bottom borders. Content beyond
// H-2 rows is clamped: a pane that outgrows its box loses its last row,
// never pushes the box past the page height (which would eat a chrome
// row).
func PaneRow(left, right PaneSpec) []string {
	clamp := func(s PaneSpec) []string {
		if s.H > 0 && len(s.Lines) > s.H-2 {
			return s.Lines[:s.H-2]
		}
		return s.Lines
	}
	ll, rl := clamp(left), clamp(right)
	n := len(ll)
	if len(rl) > n {
		n = len(rl)
	}
	if h := max(left.H, right.H); h > 0 {
		if want := h - 2; want > n {
			n = want
		}
	}
	box := func(s PaneSpec, lines []string) []string {
		padded := make([]string, n)
		copy(padded, lines)
		return titledBox(strings.TrimSpace(s.Title), "", s.Footer, s.Focused, s.W, padded)
	}
	return JoinBoxes(box(left, ll), box(right, rl))
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

// AppFrame renders the application's outer rounded frame — exactly w×h
// cells, every inner line padded and truncated to w-2 — with the help
// keys embedded in the bottom edge, the same text-in-border language as
// TitledBox's titles. keys yields before hint: the trailing call-to-action
// must survive on narrow terminals, and the edge never wraps.
func AppFrame(w, h int, keys, hint string, lines []string) string {
	if w < 8 {
		w = 8
	}
	inner := w - 2
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	var b strings.Builder
	b.WriteString(dim.Render("╭"+strings.Repeat("─", max0(inner))+"╮") + "\n")
	for i := 0; i < max0(h-2); i++ {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		b.WriteString(dim.Render("│") + PadRight(Truncate(l, inner), inner) + dim.Render("│") + "\n")
	}
	if hw := lipgloss.Width(hint); hint == "" || hw+9 > w {
		b.WriteString(dim.Render("╰" + strings.Repeat("─", max0(inner)) + "╯"))
		return b.String()
	}
	// ╰─ keys ─…─ hint ─╯
	if dashes := w - 8 - lipgloss.Width(keys) - lipgloss.Width(hint); dashes < 1 {
		keys = Truncate(keys, max0(w-8-lipgloss.Width(hint)-1))
	}
	dashes := w - 8 - lipgloss.Width(keys) - lipgloss.Width(hint)
	b.WriteString(dim.Render("╰─ ") + keys + dim.Render(" "+strings.Repeat("─", dashes)+" ") +
		hint + dim.Render(" ─╯"))
	return b.String()
}
