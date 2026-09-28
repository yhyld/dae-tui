package app

import (
	"encoding/base64"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"dae-tui/internal/i18n"
)

type clipboardMsg struct {
	OK   bool
	What string
	Err  error
}

// osc52MaxPayload caps what we send in one OSC 52 sequence. Terminals
// enforce their own limits (commonly tens of KB); a paste above any of
// them is dropped silently, so refusing up front with a visible error
// beats a copy that looks like it worked.
const osc52MaxPayload = 64 << 10 // 64 KiB raw, ~85 KiB after base64

func osc52CopyCmd(text, what string) tea.Cmd {
	return func() tea.Msg {
		if !stdoutIsTerminal() {

			return clipboardMsg{What: what}
		}
		if len(text) > osc52MaxPayload {
			return clipboardMsg{OK: false, What: what,
				Err: fmt.Errorf(i18n.T("内容 %d KB 超出终端剪贴板上限"), len(text)>>10)}
		}

		// One WriteString call per sequence: the terminal's line discipline
		// serializes whole write(2) calls, and Bubble Tea's renderer also
		// flushes each frame as one buffered write — so a single-call write
		// here cannot be split mid-frame by the renderer's output. Keep this
		// a single call; splitting it is how sequences get corrupted.
		seq := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07"
		_, err := os.Stdout.WriteString(seq)
		return clipboardMsg{OK: err == nil, What: what}
	}
}

func stdoutIsTerminal() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
