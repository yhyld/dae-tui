package app

import (
	"strings"
	"testing"
)

// This file is the About-window logo's design record: six candidates drawn
// on a dot grid at braille resolution (each cell packs 2x4 dots), plus the
// regression test that keeps the committed logo one of them. The dot maps
// in the candidate comments are the human-readable spec of each design —
// read them top to bottom, '#' is a lit dot.
//
// Designs were iterated by rendering each candidate into the real About
// overlay; the framed traffic bars won — the mark miniaturizes the home
// page's signature braille chart, so the About window and the traffic view
// speak the same visual language. It is also the only two-layer candidate:
// the frame and axis form one layer rendered in the accent at normal
// weight, the bars another rendered bold — a cell holding any bar dot
// takes the bold style, so the two never share a cell except on the axis
// row.

// dotCanvas is a boolean grid at braille-dot resolution.
type dotCanvas struct {
	w, h int
	dots [][]bool
}

func newDotCanvas(w, h int) *dotCanvas {
	d := make([][]bool, h)
	for i := range d {
		d[i] = make([]bool, w)
	}
	return &dotCanvas{w: w, h: h, dots: d}
}

// blank returns a builder for an empty layer of the given size — the frame
// layer of every single-tone candidate.
func blank(w, h int) func() *dotCanvas {
	return func() *dotCanvas { return newDotCanvas(w, h) }
}

func (c *dotCanvas) set(x, y int) {
	if x >= 0 && x < c.w && y >= 0 && y < c.h {
		c.dots[y][x] = true
	}
}

func (c *dotCanvas) hline(x0, x1, y int) {
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	for x := x0; x <= x1; x++ {
		c.set(x, y)
	}
}

func (c *dotCanvas) vline(y0, y1, x int) {
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	for y := y0; y <= y1; y++ {
		c.set(x, y)
	}
}

// line draws a Bresenham line; chaining segments is what keeps curves
// continuous (isolated sample points leave gaps at braille resolution).
func (c *dotCanvas) line(x0, y0, x1, y1 int) {
	dx, dy := x1-x0, y1-y0
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	sx, sy := 1, 1
	if x0 > x1 {
		sx = -1
	}
	if y0 > y1 {
		sy = -1
	}
	err := dx - dy
	for {
		c.set(x0, y0)
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * err
		if e2 > -dy {
			err -= dy
			x0 += sx
		}
		if e2 < dx {
			err += dx
			y0 += sy
		}
	}
}

// hexagon draws a flat-top hexagon (flat top/bottom edges, pointy
// left/right) — the two-dot-wide flat edges read steadier in braille than
// a single-dot pointy vertex.
func (c *dotCanvas) hexagon(cx, cy, rx, ry int) {
	pts := [6][2]int{
		{cx + rx/2, cy - ry},
		{cx - rx/2, cy - ry},
		{cx - rx, cy},
		{cx - rx/2, cy + ry},
		{cx + rx/2, cy + ry},
		{cx + rx, cy},
	}
	for i := 0; i < 6; i++ {
		a, b := pts[i], pts[(i+1)%6]
		c.line(a[0], a[1], b[0], b[1])
	}
}

func (c *dotCanvas) block(x0, y0, x1, y1 int) {
	for y := y0; y <= y1; y++ {
		c.hline(x0, x1, y)
	}
}

// brailleBits maps (row, col) inside a 4x2 braille cell to its dot bit,
// matching ui.chart's brailleBit: left column top-down is dots 1,2,3,7 and
// the right column is 4,5,6,8.
var brailleBits = [4][2]int{{0x01, 0x08}, {0x02, 0x10}, {0x04, 0x20}, {0x40, 0x80}}

func (c *dotCanvas) pack() []string {
	var lines []string
	for cy := 0; cy < c.h; cy += 4 {
		var sb strings.Builder
		for cx := 0; cx < c.w; cx += 2 {
			mask := 0
			for row := 0; row < 4; row++ {
				for col := 0; col < 2; col++ {
					if c.dots[cy+row][cx+col] {
						mask |= brailleBits[row][col]
					}
				}
			}
			sb.WriteRune(rune(0x2800 + mask))
		}
		lines = append(lines, sb.String())
	}
	return lines
}

// decodeBraille is pack's inverse; the canvas size comes from the input so
// candidates may differ in size. Used to check the committed logo file
// against these designs without eyeballing codepoints.
func decodeBraille(lines []string) *dotCanvas {
	if len(lines) == 0 {
		return newDotCanvas(0, 0)
	}
	w := len([]rune(lines[0])) * 2
	c := newDotCanvas(w, len(lines)*4)
	for cy, line := range lines {
		for cx, r := range []rune(line) {
			mask := int(r) - 0x2800
			for row := 0; row < 4; row++ {
				for col := 0; col < 2; col++ {
					if mask&brailleBits[row][col] != 0 {
						c.set(2*cx+col, 4*cy+row)
					}
				}
			}
		}
	}
	return c
}

func sameDots(a, b *dotCanvas) bool {
	if a.w != b.w || a.h != b.h {
		return false
	}
	for y := 0; y < a.h; y++ {
		for x := 0; x < a.w; x++ {
			if a.dots[y][x] != b.dots[y][x] {
				return false
			}
		}
	}
	return true
}

// canvasShield: a guard outline with a two-stroke check — "the proxy
// works". The check's long arm stops one dot short of the right edge so it
// never merges into the shield's side.
func canvasShield() *dotCanvas {
	c := newDotCanvas(24, 16)
	c.hline(5, 18, 1)
	c.vline(1, 6, 5)
	c.vline(1, 6, 18)
	c.line(5, 6, 11, 13)
	c.line(18, 6, 12, 13)
	c.line(7, 4, 11, 8)
	c.line(8, 4, 12, 8)
	c.line(11, 8, 16, 3)
	c.line(12, 8, 17, 3)
	return c
}

// canvasHexagon: outer hexagon, inner hexagon, solid core — a kernel-level
// chip for an eBPF proxy, in the sparkline's dots.
func canvasHexagon() *dotCanvas {
	c := newDotCanvas(24, 16)
	c.hexagon(11, 7, 8, 7)
	c.hexagon(11, 7, 5, 4)
	c.block(11, 7, 12, 8)
	return c
}

// canvasTrafficFrame is the frame layer of the chosen mark on a 30x20
// grid: a rounded frame (3-dot-radius corner arcs, echoing the app's
// rounded boxes) plus the dotted axis one row above the bottom edge.
// Rendered in the accent at normal weight.
func canvasTrafficFrame() *dotCanvas {
	c := newDotCanvas(30, 20)
	c.hline(4, 25, 2)  // top edge, corner arc end dots included
	c.hline(4, 25, 17) // bottom edge
	c.vline(4, 15, 2)  // left side
	c.vline(4, 15, 27) // right side
	c.set(3, 3)        // corner arc diagonals
	c.set(26, 3)
	c.set(3, 16)
	c.set(26, 16)
	c.hline(4, 25, 15) // axis
	return c
}

// canvasTrafficBars is the bars layer: six 2-dot columns with 2-dot gaps
// (each column is exactly one braille cell wide, so the two styles never
// share a cell except on the axis row), bottoms one dot row above the axis
// — the sparkline's grammar. Profile 5,9,11,10,9,5: symmetric mass with a
// single dominant peak. Rendered bold in the accent.
func canvasTrafficBars() *dotCanvas {
	c := newDotCanvas(30, 20)
	xs := []int{4, 8, 12, 16, 20, 24}
	hs := []int{5, 9, 11, 10, 9, 5}
	for i, x := range xs {
		c.vline(14-hs[i]+1, 14, x)
		c.vline(14-hs[i]+1, 14, x+1)
	}
	return c
}

// canvasWave: the traffic idea as a smooth curve — the trace the sparkline
// would draw if its bars were connected.
func canvasWave() *dotCanvas {
	c := newDotCanvas(24, 16)
	pts := [][2]int{
		{6, 13}, {7, 10}, {8, 7}, {9, 5}, {10, 3}, {11, 2},
		{12, 2}, {13, 3}, {14, 5}, {15, 7}, {16, 10}, {17, 13},
	}
	for i := 0; i+1 < len(pts); i++ {
		c.line(pts[i][0], pts[i][1], pts[i+1][0], pts[i+1][1])
	}
	c.hline(6, 17, 14)
	return c
}

// canvasNodes: three solid nodes wired into a triangle — groups, nodes and
// the links between them. Shifted up one dot row so the mark fills all
// four braille rows instead of leaving the top one blank.
func canvasNodes() *dotCanvas {
	c := newDotCanvas(24, 16)
	c.block(6, 3, 7, 4)
	c.block(17, 3, 18, 4)
	c.block(11, 11, 12, 12)
	c.hline(8, 16, 3)
	c.line(7, 4, 11, 11)
	c.line(16, 4, 12, 11)
	return c
}

// canvasGem: a diamond outline with its table facet — the conservative
// alternative, refined from the original placeholder.
func canvasGem() *dotCanvas {
	c := newDotCanvas(24, 16)
	c.line(11, 1, 3, 7)
	c.line(3, 8, 11, 14)
	c.line(12, 1, 20, 7)
	c.line(20, 8, 12, 14)
	c.hline(8, 15, 4)
	return c
}

// logoCatalog is every designed candidate as a (frame, bars) layer pair.
// aboutLogo must be one of them.
var logoCatalog = []struct {
	name        string
	frame, bars func() *dotCanvas
}{
	{"shield", blank(24, 16), canvasShield},
	{"hexagon", blank(24, 16), canvasHexagon},
	{"traffic", canvasTrafficFrame, canvasTrafficBars},
	{"wave", blank(24, 16), canvasWave},
	{"nodes", blank(24, 16), canvasNodes},
	{"gem", blank(24, 16), canvasGem},
}

// The About window's mark must stay a designed candidate: this catches a
// half-pasted logo (a mangled braille glyph decodes to stray dots and
// matches nothing) the same way the DSL round-trip tests catch a broken
// preset template. Both layers are checked, so a swapped or transposed
// pair fails too.
func TestAboutLogoIsKnownCandidate(t *testing.T) {
	frame, bars := decodeBraille(aboutLogoFrame), decodeBraille(aboutLogoBars)
	for _, cand := range logoCatalog {
		if sameDots(frame, cand.frame()) && sameDots(bars, cand.bars()) {
			return
		}
	}
	var b strings.Builder
	b.WriteString("aboutLogo matches no catalog candidate (merged map, '#' bars / '+' frame):\n")
	for y := 0; y < frame.h || y < bars.h; y++ {
		for x := 0; x < frame.w || x < bars.w; x++ {
			switch {
			case y < bars.h && x < bars.w && bars.dots[y][x]:
				b.WriteByte('#')
			case y < frame.h && x < frame.w && frame.dots[y][x]:
				b.WriteByte('+')
			default:
				b.WriteByte('.')
			}
		}
		b.WriteByte('\n')
	}
	t.Fatal(b.String())
}
