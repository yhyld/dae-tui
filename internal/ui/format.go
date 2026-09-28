package ui

import (
	"fmt"
	"strings"
	"time"

	"dae-tui/internal/i18n"
)

// Rate formats a bytes/sec value for display.
func Rate(bps float64) string {
	switch {
	case bps >= 1<<30:
		return fmt.Sprintf("%.1f GB/s", bps/(1<<30))
	case bps >= 1<<20:
		return fmt.Sprintf("%.1f MB/s", bps/(1<<20))
	case bps >= 1<<10:
		return fmt.Sprintf("%.1f KB/s", bps/(1<<10))
	default:
		return fmt.Sprintf("%.0f B/s", bps)
	}
}

// Bytes formats a cumulative byte count.
func Bytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.2f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// TimeAgo renders a coarse relative age like "3分钟前", translated at call
// time (T takes the format args directly — wrapping a Sprintf result would
// translate the formatted string, which is never a catalog key).
func TimeAgo(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < 30*time.Second:
		return i18n.T("刚刚")
	case d < time.Hour:
		return i18n.T("%d分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return i18n.T("%d小时前", int(d.Hours()))
	default:
		return i18n.T("%d天前", int(d.Hours()/24))
	}
}

// Span renders a duration as Chinese text for a window label ("近 3 分钟").
// TimeAgo answers "how long ago"; this answers "how wide is the window".
func Span(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	default:
		return fmt.Sprintf("%.1f 小时", d.Hours())
	}
}

// Truncate shortens s to display width w with an ellipsis (CJK-aware).
func Truncate(s string, w int) string {
	return truncate(s, w)
}

// LatencyBar renders a fixed 5-cell micro-bar for a latency in ms — 500ms
// and above fills it. Cells fill with braille dots rising from the bottom
// (the traffic chart's texture and grammar), never block glyphs: adjacent
// values shade into each other dot by dot instead of jumping between a
// full block and a thin sliver. The caller styles the result (usually the
// same color as the numeric value); callers pass ms > 0 only for alive,
// tested nodes, everything else renders blank so the column stays aligned.
func LatencyBar(ms int) string {
	const w = 5
	if ms <= 0 {
		return spaces(w)
	}
	v := float64(ms) / 500 * w
	if v > w {
		v = w
	}
	dots := int(v * 8)
	full := dots / 8
	part := dots % 8
	written := full
	var b strings.Builder
	for i := 0; i < full; i++ {
		b.WriteRune(brailleBase + 0xFF)
	}
	if full < w && part > 0 {
		var bits rune
		for _, bit := range dotFillOrder[:part] {
			bits |= bit
		}
		b.WriteRune(brailleBase + bits)
		written++
	}
	for i := 0; i < w-written; i++ {
		b.WriteString(" ")
	}
	return b.String()
}

// TruncateHead shortens s from the front with an ellipsis, keeping the tail
// (CJK-aware). For paths the tail is the identifying part, so cutting the
// end off — as Truncate does — destroys the information.
func TruncateHead(s string, w int) string {
	if w <= 1 || width(s) <= w {
		return s
	}
	rs := []rune(s)
	kept, start := 0, len(rs)
	for start > 0 {
		rw := width(string(rs[start-1]))
		if kept+rw > w-1 { // one cell reserved for the ellipsis
			break
		}
		kept += rw
		start--
	}
	return "…" + string(rs[start:])
}

// SpaceAfterFlag keeps a leading flag emoji from running into the text that
// follows it. Subscription node names routinely arrive as "🇩🇪Germany 01":
// a flag occupies two cells and its glyph ends up pressed against the
// country name, which reads as the two overlapping.
func SpaceAfterFlag(s string) string {
	rs := []rune(s)
	if len(rs) < 3 || !isRegionalIndicator(rs[0]) || !isRegionalIndicator(rs[1]) {
		return s
	}
	// Already spaced, or a second flag follows — leave both alone.
	if rs[2] == ' ' || isRegionalIndicator(rs[2]) {
		return s
	}
	return string(rs[:2]) + " " + string(rs[2:])
}

func isRegionalIndicator(r rune) bool {
	return r >= 0x1F1E6 && r <= 0x1F1FF
}
