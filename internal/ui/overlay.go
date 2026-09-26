package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Overlay stacks box over base, centered horizontally and biased toward the
// upper third vertically (that is where a dialog reads best) inside a w×h
// cell area. The base lines under the box are cut apart with ANSI-aware
// truncation, so the page around the floating window stays visible; the
// result is exactly h lines.
//
// Known cosmetic limit: TruncateLeft drops escape sequences in the skipped
// prefix, so the base sliver to the right of the box may lose an open color
// and render in the default color. The text stays correct, and with the box
// centered the slivers are narrow.
func Overlay(base, box string, w, h int) string {
	baseLines := strings.Split(base, "\n")
	boxLines := strings.Split(box, "\n")
	boxW := 0
	for _, l := range boxLines {
		if lw := width(l); lw > boxW {
			boxW = lw
		}
	}
	// Defensive: a box wider than the area is cut to the area rather than
	// spilling over the frame (callers size their boxes, but Overlay must
	// hold the w×h contract on its own).
	if boxW > w {
		boxW = w
		for i, l := range boxLines {
			boxLines[i] = truncate(l, w)
		}
	}
	boxH := len(boxLines)
	x0 := (w - boxW) / 2
	if x0 < 0 {
		x0 = 0
	}
	y0 := (h - boxH) / 3
	if y0 < 0 {
		y0 = 0
	}

	out := make([]string, h)
	for i := 0; i < h; i++ {
		line := ""
		if i < len(baseLines) {
			line = baseLines[i]
		}
		if i < y0 || i >= y0+boxH {
			out[i] = line
			continue
		}
		left := ansi.Truncate(line, x0, "")
		if gap := x0 - width(left); gap > 0 {
			// Reset before the filler: a style opened in base would paint
			// the gap and bleed under the box.
			left += "\x1b[0m" + spaces(gap)
		}
		right := ansi.TruncateLeft(line, x0+boxW, "")
		out[i] = left + boxLines[i-y0] + right
	}
	// Hold the w×h contract: untouched base rows may be wider than the
	// area (the root clamps later, but Overlay guarantees it alone).
	for i := range out {
		out[i] = truncate(out[i], w)
	}
	return strings.Join(out, "\n")
}
