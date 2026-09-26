package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Sparkline renders series as a braille-dot chart, width x height cells
// (each cell packs 2x4 dots). Empty series renders a dim placeholder.
func Sparkline(series []float64, width, height int, style lipgloss.Style, emptyLabel string) string {
	if height <= 0 {
		return ""
	}
	if len(series) < 2 || width < 3 {
		return style.Foreground(Gray).Render(strings.Repeat("·", width))
	}

	// Downsample (or stretch) the series to width*2 points.
	const dotsPerCellX = 2
	const dotsPerCellY = 4
	n := width * dotsPerCellX
	pts := resample(series, n)

	max := 0.0
	for _, v := range pts {
		if v > max {
			max = v
		}
	}
	// Reserve the top dot row so peaks don't touch the border, and the
	// bottom one exclusively for the zero axis — curve dots merging into the
	// axis row produce lopsided braille cells that read as rendering garbage.
	dotRows := height*dotsPerCellY - 2

	// grid[row][col] of braille bitmasks; row 0 is the top.
	grid := make([][]rune, height)
	for r := range grid {
		grid[r] = []rune(strings.Repeat("⠀", width))
	}
	// The zero axis: a dotted line along the bottom dot row, always present —
	// it marks zero while idle and stays put under the curve while traffic
	// flows, so the chart keeps its reference edge in both states.
	for i := range pts {
		col := i / dotsPerCellX
		grid[height-1][col] = rune(brailleBase +
			int(grid[height-1][col]-brailleBase) +
			brailleBit(dotsPerCellY-1, i%dotsPerCellX))
	}
	if max > 0 {
		for i, v := range pts {
			if v <= 0 {
				continue
			}
			h := int(v / max * float64(dotRows))
			if h < 1 {
				h = 1
			}
			if h > dotRows {
				h = dotRows
			}
			colDot := i % dotsPerCellX
			col := i / dotsPerCellX
			for y := 0; y < h; y++ {
				// y counts up from the first dot row above the axis (row 0
				// belongs to the axis alone): bars rise from just over the
				// reference line without ever touching it.
				row := y + 1
				cellRow := height - 1 - row/dotsPerCellY
				bit := brailleBit(dotsPerCellY-1-row%dotsPerCellY, colDot)
				if cellRow >= 0 && cellRow < height && col >= 0 && col < width {
					grid[cellRow][col] = rune(brailleBase + int(grid[cellRow][col]-brailleBase) + bit)
				}
			}
		}
	}
	lines := make([]string, height)
	for r := range grid {
		lines[r] = string(grid[r])
	}
	return style.Render(strings.Join(lines, "\n"))
}

const brailleBase = 0x2800

// brailleBit maps (row, col) within a 4x2 braille cell to its dot bit.
// Layout: left column rows 0..3 = dots 1,2,3,7 (0x01,0x02,0x04,0x40);
// right column rows 0..3 = dots 4,5,6,8 (0x08,0x10,0x20,0x80).
func brailleBit(row, col int) int {
	if col == 0 {
		return []int{0x01, 0x02, 0x04, 0x40}[row]
	}
	return []int{0x08, 0x10, 0x20, 0x80}[row]
}

// resample linearly maps len(in) points onto exactly n points.
func resample(in []float64, n int) []float64 {
	if n <= 0 {
		return nil
	}
	if len(in) == n {
		return in
	}
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		pos := float64(i) * float64(len(in)-1) / float64(n-1)
		lo := int(pos)
		hi := lo + 1
		if hi >= len(in) {
			out[i] = in[len(in)-1]
			continue
		}
		frac := pos - float64(lo)
		out[i] = in[lo]*(1-frac) + in[hi]*frac
	}
	return out
}
