package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// A box must be exactly w cells on every line — titles, content and
// borders — or the row-wise joins and the frame clamp would go ragged.
func TestTitledBoxExactWidth(t *testing.T) {
	cases := []struct {
		title string
		lines []string
	}{
		{"流量", []string{"short", strings.Repeat("宽", 80), "", "ansi \x1b[32mgreen\x1b[0m tail"}},
		{strings.Repeat("标", 40), []string{"x"}},
		{"各组当前节点", nil},
	}
	for _, c := range cases {
		for _, focused := range []bool{false, true} {
			box := TitledBox(c.title, focused, 44, c.lines)
			if len(box) != len(c.lines)+2 {
				t.Fatalf("%q: %d lines, want %d", c.title, len(box), len(c.lines)+2)
			}
			for i, l := range box {
				if w := lipgloss.Width(l); w != 44 {
					t.Fatalf("%q focused=%v: line %d is %d cells, want 44", c.title, focused, i, w)
				}
			}
		}
	}
	// The title survives in the top edge, long titles shrink instead of
	// breaking the frame.
	if top := TitledBox("流量", false, 44, nil)[0]; !strings.Contains(top, "流量") {
		t.Fatalf("title missing from top edge: %q", top)
	}
	if top := TitledBox(strings.Repeat("标", 40), false, 44, nil)[0]; !strings.Contains(top, "…") {
		t.Fatalf("overlong title not truncated: %q", top)
	}
}

func TestJoinBoxesPadsShorterSide(t *testing.T) {
	left := []string{"aaaa", "bbbb", "cccc"}
	right := []string{"x"}
	joined := JoinBoxes(left, right)
	if len(joined) != 3 {
		t.Fatalf("got %d rows, want 3", len(joined))
	}
	for i, l := range joined {
		if w := lipgloss.Width(l); w != 6 {
			t.Fatalf("row %d is %d cells, want 6 (4 + gutter + 1): %q", i, w, l)
		}
	}
}
