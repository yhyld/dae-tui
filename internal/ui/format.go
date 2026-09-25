package ui

import (
	"fmt"
	"time"
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

// TimeAgo renders a coarse relative age like "3m ago".
func TimeAgo(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < 30*time.Second:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// Truncate shortens s to display width w with an ellipsis (CJK-aware).
func Truncate(s string, w int) string {
	return truncate(s, w)
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
