package ui

import (
	"github.com/charmbracelet/x/ansi"
)

// All cell-width math in this package goes through ansi.StringWidth so that
// it agrees with lipgloss.Width — which the status bar already uses for its
// gap — and with what a terminal actually draws.
//
// The previous implementation used go-runewidth, whose StringWidth takes the
// width of the first non-zero rune of each grapheme cluster. That
// under-counts two shapes every terminal renders two cells wide — a
// regional-indicator flag pair and an emoji followed by a variation
// selector — so the padding came up one cell short and the flag overwrote
// the column after it. It also counted ANSI escape sequences as printable
// text, which inflated every styled string passed to PadRight.

// width returns the number of terminal cells s occupies. ANSI escapes are
// ignored, CJK characters count as two cells, and so do flags and emoji.
func width(s string) int {
	return ansi.StringWidth(s)
}

// truncate shortens s to at most w cells, appending an ellipsis when it has
// to cut. ANSI escapes survive the cut.
func truncate(s string, w int) string {
	return ansi.Truncate(s, w, "…")
}
