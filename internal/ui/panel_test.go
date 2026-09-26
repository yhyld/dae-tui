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

// A dual-box page row: both boxes exactly H rows tall with their borders
// aligned, every row W+1+RW cells wide, overflow clamped to the box — a
// pane that outgrows its height must lose its last row, never push the
// box past the page.
func TestPaneRowGrid(t *testing.T) {
	row := PaneRow(
		PaneSpec{Title: "列表", Lines: []string{"a", "b", "c"}, W: 20, H: 8},
		PaneSpec{Title: "详情", Lines: make([]string, 30), Focused: true, W: 30, H: 8},
	)
	if len(row) != 8 {
		t.Fatalf("got %d rows, want 8 (H)", len(row))
	}
	for i, l := range row {
		if w := lipgloss.Width(l); w != 20+1+30 {
			t.Fatalf("row %d is %d cells, want 51: %q", i, w, l)
		}
	}
	if !strings.Contains(row[0], "列表") || !strings.Contains(row[0], "详情") {
		t.Fatalf("both titles should ride the top edge: %q", row[0])
	}
	if !strings.Contains(row[7], "╰") {
		t.Fatalf("row 7 should be the boxes' bottom edge: %q", row[7])
	}
}

// PaneRow with H=0 sizes both boxes to the taller side's content (the
// home page's content-driven zones).
func TestPaneRowContentSized(t *testing.T) {
	row := PaneRow(
		PaneSpec{Title: "a", Lines: []string{"1"}, W: 12},
		PaneSpec{Title: "b", Lines: []string{"1", "2", "3"}, W: 12},
	)
	if len(row) != 5 { // 3 content rows + 2 borders
		t.Fatalf("got %d rows, want 5", len(row))
	}
}

// TitledBoxRight: the right-aligned segment rides the top edge when it
// fits and is dropped (never wrapped) when it does not.
func TestTitledBoxRightSegment(t *testing.T) {
	box := TitledBoxRight("tabs", "suffix", false, 40, []string{"x"})
	if !strings.Contains(box[0], "tabs") || !strings.Contains(box[0], "suffix") {
		t.Fatalf("both segments should ride the top edge: %q", box[0])
	}
	if w := lipgloss.Width(box[0]); w != 40 {
		t.Fatalf("top edge is %d cells, want 40", w)
	}
	narrow := TitledBoxRight("tabs", "a much too long suffix", false, 20, []string{"x"})
	if strings.Contains(narrow[0], "suffix") {
		t.Fatalf("the segment should be dropped when it does not fit: %q", narrow[0])
	}
	if w := lipgloss.Width(narrow[0]); w != 20 {
		t.Fatalf("narrow top edge is %d cells, want 20", w)
	}
}

// AppFrame: exactly w×h cells, the help hint pinned to the bottom edge's
// right end, keys truncated before it — and a bare edge when even the
// hint cannot fit.
func TestAppFrameHelpEdge(t *testing.T) {
	f := AppFrame(60, 10, "j/k 移动  A 应用  r 刷新", "? 帮助", make([]string, 8))
	lines := strings.Split(f, "\n")
	if len(lines) != 10 {
		t.Fatalf("got %d rows, want 10", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != 60 {
			t.Fatalf("row %d is %d cells, want 60", i, w)
		}
	}
	bottom := lines[len(lines)-1]
	if !strings.Contains(bottom, "? 帮助") {
		t.Fatalf("hint missing from the bottom edge: %q", bottom)
	}
	if !strings.HasPrefix(bottom, "╰─") {
		t.Fatalf("bottom edge should start with the corner: %q", bottom)
	}

	long := AppFrame(60, 6, strings.Repeat("键", 60), "? 帮助", nil)
	bl := strings.Split(long, "\n")[5]
	if !strings.Contains(bl, "? 帮助") {
		t.Fatalf("keys must yield before the hint: %q", bl)
	}
	if w := lipgloss.Width(bl); w != 60 {
		t.Fatalf("bottom edge is %d cells, want 60", w)
	}

	tiny := AppFrame(12, 4, "keys", "? 帮助", nil)
	if w := lipgloss.Width(strings.Split(tiny, "\n")[3]); w != 12 {
		t.Fatalf("tiny frame bottom is not 12 cells: %q", tiny)
	}
}
