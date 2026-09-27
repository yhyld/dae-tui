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
	border := lipgloss.NewStyle().Foreground(BorderCol)
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
// windowed; H is the box's outer height (0 pairs with the other side's
// height; when neither side pins one, the taller side's content sizes the
// row, so both boxes share their borders).
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

// renderPane wraps one pane's lines in its TitledBox. h > 0 is the box's
// outer height: content beyond h-2 rows is clamped (a pane that outgrows
// its box loses its last row, never pushes the box past the page height,
// which would eat a chrome row) and short content is padded so boxes
// joined into a grid share their borders. h <= 0 sizes the box to the
// content.
func renderPane(s PaneSpec, h int) []string {
	lines := s.Lines
	if h > 0 {
		if h < 2 {
			// A box is borders plus content; h==1 would slice lines[:-1]
			// and panic. Today every caller passes h >= 2 (minTermH keeps
			// pages well above it), but titledBox already guards w < 12 —
			// keep the two axes symmetric.
			h = 2
		}
		if len(lines) > h-2 {
			lines = lines[:h-2]
		}
	}
	n := len(lines)
	if h > 0 && h-2 > n {
		n = h - 2
	}
	padded := make([]string, n)
	copy(padded, lines)
	return titledBox(strings.TrimSpace(s.Title), "", s.Footer, s.Focused, s.W, padded)
}

// PaneRow builds a page's master/detail pair: both sides padded to the
// same height so their borders align, wrapped in TitledBoxes and joined
// with a one-space gutter — the same grid language as the home page's
// paired zones, so the two boxes always share their top and bottom
// borders. When neither side pins H the taller side's content sizes the
// row: padding the short side afterwards (JoinBoxes' blank rows) would
// leave its bottom edge floating above its partner's, and a zone grid
// whose boxes don't close on the same line reads as broken layout — the
// dead space belongs inside the box.
func PaneRow(left, right PaneSpec) []string {
	h := max(left.H, right.H)
	if h <= 0 {
		h = max(len(left.Lines), len(right.Lines)) + 2 // borders included
	}
	return JoinBoxes(renderPane(left, h), renderPane(right, h))
}

// PaneRowColumn is a master/detail row whose detail side stacks two boxes:
// a small info pane on top and a content pane filling the rest of the
// column. left.H is the whole column's height; top.H sizes the info box
// (0 sizes it to its content); the bottom box takes the remaining rows.
// All three boxes share their edges, so the column reads as one flush
// grid, and the same clamp rule as PaneRow applies: a pane that outgrows
// its box loses its last row, never pushes the column past left.H.
func PaneRowColumn(left, top, bottom PaneSpec) []string {
	total := left.H
	th := top.H
	if th <= 0 {
		th = len(top.Lines) + 2
	}
	bh := 0
	if total > 0 {
		if th > total-2 {
			th = total - 2
		}
		if th < 2 {
			th = 2
		}
		bh = total - th
	}
	column := append(renderPane(top, th), renderPane(bottom, bh)...)
	return JoinBoxes(renderPane(left, total), column)
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

// WithScrollbar appends a one-cell scrollbar column to a windowed list's
// rows: each row is truncated/padded to w-1 so the indicator rides the
// box's right content edge (titledBox then pads nothing — rows come back
// exactly w cells wide). Nothing is drawn until the list overflows its
// window — a scrollbar on content that already fits is noise. total is the
// full list's row count and start the window's first row; apply this to
// the window rows only, before appending trailing lines (errors, prompts).
func WithScrollbar(rows []string, w, total, start int, focused bool) []string {
	if w < 2 || len(rows) == 0 || total <= len(rows) {
		return rows
	}
	win := len(rows)
	thumbH := win * win / total
	if thumbH < 1 {
		thumbH = 1
	}
	thumbAt := start * win / total
	if thumbAt > win-thumbH {
		thumbAt = win - thumbH
	}
	if thumbAt < 0 {
		thumbAt = 0
	}
	st := BorderDim
	if focused {
		st = lipgloss.NewStyle().Foreground(Accent)
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		bar := " "
		if i >= thumbAt && i < thumbAt+thumbH {
			bar = st.Render("▐")
		}
		out[i] = PadRight(Truncate(r, w-1), w-1) + bar
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
	dim := lipgloss.NewStyle().Foreground(BorderCol)
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
