// Package ui centralizes visual primitives: styles, latency color ramp,
// value formatting and a braille sparkline chart.
package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	// Base colors.
	Accent  = lipgloss.Color("62")  // soft blue
	Green   = lipgloss.Color("42")  // good latency
	Yellow  = lipgloss.Color("214") // medium latency
	Red     = lipgloss.Color("203") // high latency
	Gray    = lipgloss.Color("241") // dead / unknown
	DimText = lipgloss.Color("245")

	TitleStyle    = lipgloss.NewStyle().Bold(true).Foreground(Accent)
	TabStyle      = lipgloss.NewStyle().Foreground(DimText).Padding(0, 1)
	TabActive     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(Accent).Padding(0, 1)
	HelpStyle     = lipgloss.NewStyle().Foreground(DimText)
	ErrorStyle    = lipgloss.NewStyle().Foreground(Red)
	OKStyle       = lipgloss.NewStyle().Foreground(Green)
	SelectedStyle = lipgloss.NewStyle().Bold(true).Foreground(Accent)
	CursorStyle   = lipgloss.NewStyle().Bold(true).Foreground(Accent)
	BorderDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
)

// LatencyStyle maps a latency in ms to a style.
func LatencyStyle(ms int, alive, tested bool) lipgloss.Style {
	if !tested {
		return lipgloss.NewStyle().Foreground(Gray)
	}
	if !alive {
		return lipgloss.NewStyle().Foreground(Gray)
	}
	if ms <= 0 {
		// Alive but without a figure: a probe in flight or a record without
		// a measurement — untested, not worst-latency.
		return lipgloss.NewStyle().Foreground(Gray)
	}
	switch {
	case ms > 0 && ms < 200:
		return lipgloss.NewStyle().Foreground(Green)
	case ms >= 200 && ms < 500:
		return lipgloss.NewStyle().Foreground(Yellow)
	default:
		return lipgloss.NewStyle().Foreground(Red)
	}
}

// PadRight pads s with spaces to display width w (CJK-aware).
func PadRight(s string, w int) string {
	d := w - width(s)
	if d < 0 {
		return truncate(s, w)
	}
	return s + spaces(d)
}

// PadLeft right-aligns s in display width w (CJK-aware).
func PadLeft(s string, w int) string {
	d := w - width(s)
	if d < 0 {
		return truncate(s, w)
	}
	return spaces(d) + s
}

// Pane renders a master/detail pane: a title line, a rule under it, and the
// (already windowed) body lines. Every line is truncated to w cells so the
// pane keeps a stable width, and the block is clamped and padded to exactly
// h lines so the two panes always fill the app frame. With bar=true a
// divider column prefixes every line, accent-colored when the pane holds
// focus — the right pane's bar doubles as the divider between the two panes;
// the left pane passes false because the app frame draws that edge. Focus
// also lights the title and the rule.
func Pane(bar bool, title string, focused bool, w, h int, lines []string) string {
	if w < 4 {
		w = 4
	}
	prefix := "  "
	if bar {
		prefix = lipgloss.NewStyle().Foreground(lipgloss.Color("238")).Render("│ ")
		if focused {
			prefix = lipgloss.NewStyle().Foreground(Accent).Render("▌ ")
		}
	}
	tstyle := lipgloss.NewStyle().Foreground(DimText)
	rule := lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	if focused {
		tstyle = TitleStyle
		rule = lipgloss.NewStyle().Foreground(Accent)
	}
	avail := h - 1
	if avail < 1 {
		avail = 1
	}
	var b strings.Builder
	// The title rides inside the rule line — the same language as the home
	// page's boxed zones (TitledBox embeds it in the top border) — saving
	// the separate title row. Callers pass titles with legacy padding;
	// trim it so the spacing matches the box titles exactly.
	content := w - 2
	title = strings.TrimSpace(title)
	tt := truncate(tstyle.Render(title), max0(content-4))
	dashes := content - 3 - lipgloss.Width(tt)
	if dashes < 1 {
		dashes = 1
	}
	b.WriteString(prefix + rule.Render("─ ") + tt + rule.Render(" "+strings.Repeat("─", dashes)) + "\n")
	for i := 0; i < avail; i++ {
		l := ""
		if i < len(lines) {
			l = truncate(lines[i], w-2)
		}
		b.WriteString(prefix + l + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// BoxLines wraps lines in a rounded border — the shared look for in-pane
// confirmations and small forms. destructive=true draws the border red.
func BoxLines(destructive bool, lines ...string) []string {
	if len(lines) == 0 {
		return nil
	}
	st := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	if destructive {
		st = st.BorderForeground(Red)
	} else {
		st = st.BorderForeground(Accent)
	}
	return strings.Split(st.Render(strings.Join(lines, "\n")), "\n")
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func spaces(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = ' '
	}
	return string(b)
}
