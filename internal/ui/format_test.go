package ui

import "testing"

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
