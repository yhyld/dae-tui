package ui

import (
	"strconv"
	"strings"
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

// HiRow off is a pass-through; on, the bar spans the full content width —
// CJK rows included — and survives the row's own styles.
func TestHiRow(t *testing.T) {
	row := "普通行"
	if got := HiRow(row, 20, false); got != row {
		t.Fatalf("HiRow off changed the row: %q", got)
	}
	if got := HiRow("中文", 6, true); width(got) != 6 {
		t.Fatalf("HiRow bar width %d, want 6", width(got))
	}
	styled := SelectedStyle.Render("节点") + HelpStyle.Render("尾")
	if got := HiRow(styled, 20, true); width(got) != 20 {
		t.Fatalf("HiRow styled-row width %d, want 20", width(got))
	}
	if w := HiRow("abc", 2, true); width(w) != 2 {
		t.Fatalf("HiRow over-wide row width %d, want 2", width(w))
	}
}

// The background must re-arm after every embedded ESC[0m — the bar dies at
// the first inner style otherwise.
func TestReassertBG(t *testing.T) {
	open := "\x1b[48;5;236m"
	row := "\x1b[31m红\x1b[0m尾"
	want := open + "\x1b[31m红\x1b[0m" + open + "尾\x1b[0m"
	if got := reassertBG(row, open); got != want {
		t.Fatalf("reassertBG = %q, want %q", got, want)
	}
	if got := reassertBG(row, ""); got != row {
		t.Fatalf("empty open must pass through, got %q", got)
	}
	if got := reassertBG("无样式", open); got != open+"无样式\x1b[0m" {
		t.Fatalf("plain row = %q", got)
	}
}

func TestApplyTheme(t *testing.T) {
	origAccent, origTitle, origTab, origSel, origCur, origBG := Accent, TitleStyle, TabActive, SelectedStyle, CursorStyle, SelBG
	t.Cleanup(func() {
		Accent, TitleStyle, TabActive, SelectedStyle, CursorStyle, SelBG = origAccent, origTitle, origTab, origSel, origCur, origBG
	})

	ApplyTheme("203")
	if Accent != lipgloss.Color("203") {
		t.Fatalf("Accent = %v, want 203", Accent)
	}
	if got := SelectedStyle.Render("x"); !strings.Contains(got, strconv.Itoa(203)) && got != "x" {
		t.Fatalf("SelectedStyle not rebuilt for 203: %q", got)
	}
	// The selection bar follows the accent.
	if SelBG != selBGFromAccent(Accent) {
		t.Fatalf("SelBG = %v, want the accent-derived %v", SelBG, selBGFromAccent(Accent))
	}

	ApplyTheme("#FF00AA")
	if Accent != lipgloss.Color("#ff00aa") {
		t.Fatalf("Accent = %v, want #ff00aa", Accent)
	}
	if SelBG != selBGFromAccent(Accent) {
		t.Fatalf("SelBG = %v after hex accent, want %v", SelBG, selBGFromAccent(Accent))
	}

	// Invalid values keep the previous accent.
	for _, bad := range []string{"", "  ", "999", "-1", "xyz", "#12345", "#gggggg"} {
		ApplyTheme(bad)
	}
	if Accent != lipgloss.Color("#ff00aa") {
		t.Fatalf("Accent = %v after invalid input, want #ff00aa", Accent)
	}
}

// The selection bar is the accent blended into the dark base — faint theme
// tint, not a flat gray and not a solid block.
func TestSelBGFromAccent(t *testing.T) {
	// 62 = #5f5fd7 blended 40% into #303030 → #434373.
	if got := selBGFromAccent(lipgloss.Color("62")); got != lipgloss.Color("#434373") {
		t.Fatalf("selBGFromAccent(62) = %v, want #434373", got)
	}
	if got := selBGFromAccent(lipgloss.Color("#ff0000")); got != lipgloss.Color("#831d1d") {
		t.Fatalf("selBGFromAccent(#ff0000) = %v, want #831d1d", got)
	}
	// Unparseable colors fall back to the neutral base.
	if got := selBGFromAccent(lipgloss.Color("junk")); got != lipgloss.Color("#303030") {
		t.Fatalf("selBGFromAccent(junk) = %v, want the #303030 base", got)
	}
}
