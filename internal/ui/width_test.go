package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestWidth(t *testing.T) {
	cases := []struct {
		s    string
		want int
	}{
		{"", 0},
		{"HK-01", 5},
		{"香港01", 6},                       // CJK is two cells per character
		{"🇭🇰", 2},                         // a flag pair is two cells, not one
		{"🇭🇰HK-01", 7},                    //
		{"混搭🇭🇰节点", 10},                    //
		{"🏳️", 2},                         // emoji + variation selector
		{"🏳️节点", 6},                       //
		{"⭐", 2},                          //
		{" (手动)", 7},                      //
		{"\x1b[38;5;245m (手动)\x1b[0m", 7}, // escapes are not printable
	}
	for _, c := range cases {
		if got := width(c.s); got != c.want {
			t.Errorf("width(%q) = %d, want %d", c.s, got, c.want)
		}
	}
}

// The point of the whole exercise: padding must never leave a string wider
// than the column it was given, whatever it contains.
func TestPadNeverOverflowsColumn(t *testing.T) {
	inputs := []string{"🇭🇰HK", "香港01", "🏳️x", "plain", "🇭🇰🇯🇵🇺🇸", "\x1b[31mred\x1b[0m"}
	for _, s := range inputs {
		for w := 0; w <= 14; w++ {
			got := PadRight(s, w)
			if gw := width(got); gw > w {
				t.Errorf("PadRight(%q, %d) renders %d cells: %q", s, w, gw, got)
			}
			if width(s) <= w && width(got) != w {
				t.Errorf("PadRight(%q, %d) = %q, want exactly %d cells", s, w, got, w)
			}
			got = PadLeft(s, w)
			if gw := width(got); gw > w {
				t.Errorf("PadLeft(%q, %d) renders %d cells: %q", s, w, gw, got)
			}
			if width(s) <= w && width(got) != w {
				t.Errorf("PadLeft(%q, %d) = %q, want exactly %d cells", s, w, got, w)
			}
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("HK-01", 5); got != "HK-01" {
		t.Errorf("short string changed: %q", got)
	}
	if got := Truncate("香港节点01", 6); got != "香港…" {
		t.Errorf("Truncate = %q", got)
	}
	// The ellipsis budget counts the flag as two cells.
	if got := Truncate("🇭🇰HK-01", 6); got != "🇭🇰HK-…" {
		t.Errorf("Truncate = %q", got)
	}
	for _, s := range []string{"🇭🇰HK-01", "香港节点01", "abcdefgh"} {
		for w := 0; w <= 10; w++ {
			if got := width(Truncate(s, w)); got > w {
				t.Errorf("Truncate(%q, %d) renders %d cells", s, w, got)
			}
		}
	}
	// ANSI escapes must survive the cut.
	got := Truncate("\x1b[31mabcdef\x1b[0m", 3)
	if !strings.Contains(got, "\x1b[31m") || !strings.Contains(got, "\x1b[0m") {
		t.Errorf("Truncate dropped the escape codes: %q", got)
	}
}

// The user-visible symptom: a flag in a node name pushed the protocol and
// latency columns out of line. Every row of a name/protocol/latency table
// must render to the same width whatever the name contains.
func TestTableRowsLineUp(t *testing.T) {
	names := []string{"🇭🇰HK-01", "香港01", "🏳️x", "plain", "🇭🇰🇯🇵long-name"}
	var want int
	for i, name := range names {
		line := " " + PadRight(name, 14) + PadRight("vmess", 8) + PadLeft("120ms", 9)
		if got := width(line); i == 0 {
			want = got
		} else if got != want {
			t.Errorf("row %q renders %d cells, want %d", name, got, want)
		}
	}
}

// ui.width must agree with lipgloss.Width, which the status bar already uses
// for its gap; two different notions of width in one app cannot line up.
func TestWidthAgreesWithLipgloss(t *testing.T) {
	for _, s := range []string{"", "HK-01", "🇭🇰", "香港01", "🏳️x", "\x1b[31mred\x1b[0m"} {
		if got, want := width(s), lipgloss.Width(s); got != want {
			t.Errorf("width(%q) = %d, lipgloss.Width = %d", s, got, want)
		}
	}
}

func TestSpaceAfterFlag(t *testing.T) {
	cases := []struct{ in, want string }{
		{"🇩🇪Germany 01", "🇩🇪 Germany 01"}, // the reported case
		{"🇺🇸United States 02", "🇺🇸 United States 02"},
		{"🇩🇪 Germany 02", "🇩🇪 Germany 02"}, // already spaced
		{"Germany 01", "Germany 01"},       // no flag
		{"🇩🇪", "🇩🇪"},                       // flag only
		{"🇩🇪🇺🇸Node", "🇩🇪🇺🇸Node"},           // two flags: leave alone
		{"香港01", "香港01"},
		{"", ""},
	}
	for _, c := range cases {
		if got := SpaceAfterFlag(c.in); got != c.want {
			t.Errorf("SpaceAfterFlag(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// The inserted space must not break the column: the padded result still
	// fits the width it was given.
	for _, s := range []string{"🇩🇪Germany 01", "🇺🇸United States 02"} {
		for w := 0; w <= 16; w++ {
			if got := width(PadRight(SpaceAfterFlag(s), w)); got > w {
				t.Errorf("PadRight(SpaceAfterFlag(%q), %d) renders %d cells", s, w, got)
			}
		}
	}
}
