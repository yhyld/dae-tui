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
	origAccent, origBorder, origDim := Accent, BorderCol, DimText
	origTitle, origTab, origTabDim, origHelp := TitleStyle, TabActive, TabStyle, HelpStyle
	origBorderDim, origSel, origCur, origBG := BorderDim, SelectedStyle, CursorStyle, SelBG
	t.Cleanup(func() {
		Accent, BorderCol, DimText = origAccent, origBorder, origDim
		TitleStyle, TabActive, TabStyle, HelpStyle = origTitle, origTab, origTabDim, origHelp
		BorderDim, SelectedStyle, CursorStyle, SelBG = origBorderDim, origSel, origCur, origBG
	})

	ApplyTheme("203", "", "")
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
	// The active tab stays text-on-a-line: underline, not a filled chip.
	// Assert on the style itself — the test profile strips SGR at render.
	if !TabActive.GetUnderline() {
		t.Fatalf("TabActive carries no underline")
	}
	if TabActive.GetForeground() != Accent {
		t.Fatalf("TabActive foreground = %v, want the accent %v", TabActive.GetForeground(), Accent)
	}
	// An empty border/dim keeps the defaults.
	if BorderCol != lipgloss.Color("238") || DimText != lipgloss.Color("245") {
		t.Fatalf("border/dim = %v/%v, want the defaults 238/245", BorderCol, DimText)
	}

	ApplyTheme("#FF00AA", "250", "240")
	if Accent != lipgloss.Color("#ff00aa") {
		t.Fatalf("Accent = %v, want #ff00aa", Accent)
	}
	if BorderCol != lipgloss.Color("250") || DimText != lipgloss.Color("240") {
		t.Fatalf("border/dim = %v/%v, want 250/240", BorderCol, DimText)
	}
	// Border-derived styles follow the new border color.
	if BorderDim.GetForeground() != BorderCol {
		t.Fatalf("BorderDim foreground = %v, want %v", BorderDim.GetForeground(), BorderCol)
	}
	// Help text follows the new dim color.
	if HelpStyle.GetForeground() != DimText {
		t.Fatalf("HelpStyle foreground = %v, want %v", HelpStyle.GetForeground(), DimText)
	}
	if SelBG != selBGFromAccent(Accent) {
		t.Fatalf("SelBG = %v after hex accent, want %v", SelBG, selBGFromAccent(Accent))
	}

	// Invalid values keep the previous theme, per color.
	for _, bad := range []string{"", "   ", "999", "-1", "xyz", "#12345", "#gggggg"} {
		ApplyTheme(bad, bad, bad)
	}
	if Accent != lipgloss.Color("#ff00aa") || BorderCol != lipgloss.Color("250") || DimText != lipgloss.Color("240") {
		t.Fatalf("theme = %v/%v/%v after invalid input, want #ff00aa/250/240",
			Accent, BorderCol, DimText)
	}
}

// ApplyPalette is the full-surface entry: every slot applies (or keeps the
// current value when empty/invalid), the derived styles follow, SelBG
// derives from the accent when unset and applies verbatim when set, and the
// legacy ApplyTheme wrapper retunes only the neutrals.
func TestApplyPalette(t *testing.T) {
	orig := Palette{
		Accent: string(Accent), Border: string(BorderCol), Dim: string(DimText),
		OK: string(OK), Warn: string(Warn), Err: string(Err), Gray: string(Gray), SelBG: string(SelBG),
	}
	t.Cleanup(func() { ApplyPalette(orig) })

	ApplyPalette(Palette{Accent: "201", Border: "250", Dim: "240", OK: "82", Warn: "220", Err: "196", Gray: "245"})
	if OK != lipgloss.Color("82") || Warn != lipgloss.Color("220") || Err != lipgloss.Color("196") || Gray != lipgloss.Color("245") {
		t.Fatalf("semantic trio/gray = %v/%v/%v/%v", OK, Warn, Err, Gray)
	}
	if ErrorStyle.GetForeground() != Err || OKStyle.GetForeground() != OK || WarnStyle.GetForeground() != Warn {
		t.Fatal("semantic styles not rebuilt from the palette")
	}
	if got := LatencyStyle(50, true, true); got.GetForeground() != OK {
		t.Fatalf("latency ramp did not pick up the themed OK: %v", got.GetForeground())
	}
	// Inactive tabs ride the dim color (TabDim was folded away).
	if TabStyle.GetForeground() != DimText {
		t.Fatalf("TabStyle foreground = %v, want the dim %v", TabStyle.GetForeground(), DimText)
	}
	// SelBG derives from the accent while unset…
	if SelBG != selBGFromAccent(Accent) {
		t.Fatalf("SelBG = %v, want the accent-derived %v", SelBG, selBGFromAccent(Accent))
	}
	// …and applies verbatim when set (light themes want a light bar).
	ApplyPalette(Palette{SelBG: "#d0d0d0"})
	if SelBG != lipgloss.Color("#d0d0d0") {
		t.Fatalf("explicit SelBG = %v", SelBG)
	}
	// The compat wrapper retunes the neutrals only — it must not wipe the
	// semantic trio.
	ApplyTheme("62", "238", "245")
	if OK != lipgloss.Color("82") || Warn != lipgloss.Color("220") || Err != lipgloss.Color("196") {
		t.Fatalf("ApplyTheme wiped the semantic trio: %v/%v/%v", OK, Warn, Err)
	}
	// Invalid values keep the current theme, per color — SelBG included
	// (an unparsable sel_bg must not silently re-derive and change the bar;
	// ApplyTheme above already re-derived it from accent 62).
	before := [5]lipgloss.Color{OK, Warn, Err, Gray, SelBG}
	for _, bad := range []string{"999", "-1", "xyz", "#12345", "#gggggg"} {
		ApplyPalette(Palette{OK: bad, Warn: bad, Err: bad, Gray: bad, SelBG: bad})
	}
	if OK != before[0] || Warn != before[1] || Err != before[2] || Gray != before[3] || SelBG != before[4] {
		t.Fatalf("invalid input must keep the current values: %v/%v/%v/%v/%v (was %v/%v/%v/%v/%v)",
			OK, Warn, Err, Gray, SelBG, before[0], before[1], before[2], before[3], before[4])
	}
}

// parseColor accepts ANSI-256 indexes and hex, rejects everything else.
func TestParseColor(t *testing.T) {
	for _, ok := range []string{"0", "62", "255", "#ff00aa", "#AABBCC"} {
		if _, valid := parseColor(ok); !valid {
			t.Errorf("parseColor(%q) rejected a valid color", ok)
		}
	}
	for _, bad := range []string{"", " ", "256", "-1", "62x", "#fff", "#gggggg"} {
		if c, valid := parseColor(bad); valid {
			t.Errorf("parseColor(%q) = %v, want rejected", bad, c)
		}
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
