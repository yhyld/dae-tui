package ui

import (
	"testing"
	"time"
)

func TestLatencyBar(t *testing.T) {
	cases := []struct {
		ms   int
		want string
	}{
		{0, "     "},
		{-1, "     "}, // dead/untested render blank, not a bar
		{60, "⣤    "},
		{88, "⣷    "},
		{100, "⣿    "},
		{300, "⣿⣿⣿  "},
		{420, "⣿⣿⣿⣿⡀"},
		{500, "⣿⣿⣿⣿⣿"},
		{9999, "⣿⣿⣿⣿⣿"},
	}
	for _, c := range cases {
		if got := LatencyBar(c.ms); got != c.want {
			t.Errorf("LatencyBar(%d) = %q, want %q", c.ms, got, c.want)
		}
	}
}

// The UI is all-Chinese, so relative ages are too: an English "3m ago"
// beside Chinese labels reads like a rendering bug.
func TestTimeAgoIsChinese(t *testing.T) {
	cases := []struct {
		age  time.Duration
		want string
	}{
		{-time.Minute, "刚刚"},
		{5 * time.Second, "刚刚"},
		{29 * time.Second, "刚刚"},
		{90 * time.Second, "1分钟前"},
		{42 * time.Minute, "42分钟前"},
		{3 * time.Hour, "3小时前"},
		{30 * time.Hour, "1天前"},
		{80 * time.Hour, "3天前"},
	}
	for _, c := range cases {
		if got := TimeAgo(time.Now().Add(-c.age)); got != c.want {
			t.Errorf("TimeAgo(-%v) = %q, want %q", c.age, got, c.want)
		}
	}
	if got := TimeAgo(time.Time{}); got != "-" {
		t.Errorf("TimeAgo(zero) = %q, want -", got)
	}
}
