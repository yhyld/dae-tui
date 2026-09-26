package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// Every row of a pane block — the title included — must fit exactly w
// cells. A wider row (the title used to be capped at w on top of a 2-cell
// prefix) shifts the right pane and the app frame.
func TestPaneRowsFitWidth(t *testing.T) {
	for _, bar := range []bool{false, true} {
		out := Pane(bar, strings.Repeat("宽", 60), true, 40, 8,
			[]string{"x", strings.Repeat("y", 100)})
		lines := strings.Split(out, "\n")
		if len(lines) != 8 {
			t.Fatalf("bar=%v: %d lines, want 8", bar, len(lines))
		}
		for i, l := range lines {
			if w := lipgloss.Width(l); w > 40 {
				t.Fatalf("bar=%v: line %d is %d cells wide, want <= 40", bar, i, w)
			}
		}
	}
}

// A record without a figure (probe in flight) is untested, not
// worst-latency: it shares the dead/untested gray and never red.
func TestLatencyStylePendingIsGray(t *testing.T) {
	gray := lipgloss.NewStyle().Foreground(Gray).Render("123ms")
	if got := LatencyStyle(0, true, true).Render("123ms"); got != gray {
		t.Fatalf("pending latency styled %q, want gray", got)
	}
}
