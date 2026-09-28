package app

import (
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"dae-tui/internal/i18n"
)

// testDoneNotifyCmd rings the terminal once when latency testing ends —
// every batch reported in or its window expired. OSC 9 first (terminals
// with desktop notifications surface the text, the rest consume and ignore
// an unknown OSC), then BEL, whose volume/mute is the terminal's own
// setting — that is the user's off-switch, so there is no config toggle
// here. Same single-write discipline as OSC 52: the renderer frames whole
// writes, one call cannot be split mid-sequence.
func testDoneNotifyCmd() tea.Cmd {
	return func() tea.Msg {
		if !stdoutIsTerminal() {
			return nil
		}
		_, _ = os.Stdout.WriteString("\x1b]9;" + i18n.T("测速完成") + "\x07\a")
		return nil
	}
}
