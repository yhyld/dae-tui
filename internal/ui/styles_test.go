package ui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// A record without a figure (probe in flight) is untested, not
// worst-latency: it shares the dead/untested gray and never red.
func TestLatencyStylePendingIsGray(t *testing.T) {
	gray := lipgloss.NewStyle().Foreground(Gray).Render("123ms")
	if got := LatencyStyle(0, true, true).Render("123ms"); got != gray {
		t.Fatalf("pending latency styled %q, want gray", got)
	}
}
