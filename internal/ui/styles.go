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
	// TabActive recolors only — same padding/width as TabStyle so tabs never
	// shift and the click-span math in the app stays trivial.
	TabActive     = lipgloss.NewStyle().Bold(true).Foreground(Accent).Padding(0, 1)
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
