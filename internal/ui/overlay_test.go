package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestOverlay(t *testing.T) {
	// A 20x10 base with a marker on every line, CJK and styled text included.
	mk := func(n int) string {
		lines := make([]string, n)
		for i := range lines {
			lines[i] = ErrorStyle.Render(strings.Repeat("基", 3)) + " line" + strings.Repeat("0123456789", 2)
		}
		return strings.Join(lines, "\n")
	}
	base := mk(10)
	box := strings.Join(BoxLines(false, strings.Repeat("box", 3)), "\n") // 9 cells wide

	got := Overlay(base, box, 20, 10)
	lines := strings.Split(got, "\n")
	if len(lines) != 10 {
		t.Fatalf("Overlay produced %d lines, want 10", len(lines))
	}
	// Rows above and below the box survive untouched.
	if !strings.Contains(lines[0], "line") {
		t.Fatalf("top row lost:\n%s", got)
	}
	// The box is centered (x0 = (20-9)/2 = 5) and rows under it carry base
	// on both sides.
	mid := lines[3]
	if !strings.Contains(mid, "box") {
		t.Fatalf("box missing from a covered row:\n%s", got)
	}
	// The covered row keeps base content on both sides: the CJK marker on
	// the left of the cut and digits to the right of the box.
	if !strings.Contains(mid, "基") {
		t.Fatalf("covered row lost the left base marker:\n%s", got)
	}
	if !strings.Contains(mid, "567") {
		t.Fatalf("covered row lost the right base marker:\n%s", got)
	}
	// Every line stays within the area width.
	for i, l := range lines {
		if w := lipgloss.Width(l); w > 20 {
			t.Fatalf("line %d is %d cells wide, want <= 20:\n%s", i, w, got)
		}
	}
}

func TestOverlayClamps(t *testing.T) {
	// A box wider/taller than the area still renders without panicking or
	// spilling: it pins to the top-left and the result is exactly h lines.
	base := "aaaa\nbbbb\ncccc"
	box := strings.Repeat("X", 40) + "\n" + strings.Repeat("Y", 40) + "\n" + strings.Repeat("Z", 40)
	got := Overlay(base, box, 10, 2)
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > 10 {
			t.Fatalf("line %d is %d cells wide, want <= 10", i, w)
		}
	}
}
