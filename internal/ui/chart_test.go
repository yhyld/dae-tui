package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func testStyle() lipgloss.Style { return lipgloss.NewStyle() }

func TestSparklineAllZeroDrawsBaseline(t *testing.T) {
	// An idle window (every sample zero) must still draw a baseline row;
	// a fully blank grid reads as "no data" rather than "zero".
	got := Sparkline(make([]float64, 41), 20, 3, testStyle())
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 chart rows, got %d:\n%s", len(lines), got)
	}
	if lines[0] != strings.Repeat("⠀", 20) || lines[1] != strings.Repeat("⠀", 20) {
		t.Fatalf("baseline should light only the bottom row:\n%s", got)
	}
	if !strings.Contains(lines[2], "⣀") {
		t.Fatalf("bottom row missing the baseline dots:\n%s", got)
	}
}

func TestSparklineShortSeriesPlaceholder(t *testing.T) {
	got := Sparkline(nil, 10, 3, testStyle())
	if !strings.Contains(got, "·") {
		t.Fatalf("short series should render the dotted placeholder:\n%s", got)
	}
}

func TestSparklineSpikeTallerThanBaseline(t *testing.T) {
	series := make([]float64, 40)
	series[20] = 100
	got := Sparkline(series, 20, 3, testStyle())
	flat := Sparkline(make([]float64, 40), 20, 3, testStyle())
	if got == flat {
		t.Fatalf("spiked series should differ from the flat-zero baseline:\n%s", got)
	}
}

func TestSparklineAxisPersistsUnderCurve(t *testing.T) {
	// The zero axis is permanent: even with traffic on screen, every column
	// of the bottom row keeps at least the axis dot (curve dots may merge
	// with it, so assert on blanks, not on a specific braille char).
	series := make([]float64, 40)
	for i := range series {
		series[i] = float64(i % 7)
	}
	got := Sparkline(series, 20, 3, testStyle())
	lines := strings.Split(got, "\n")
	if strings.Contains(lines[len(lines)-1], "⠀") {
		t.Fatalf("axis line has gaps under an active curve:\n%s", got)
	}
}
