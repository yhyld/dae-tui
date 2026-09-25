package app

import (
	"encoding/base64"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

// clipboardMsg reports whether an OSC 52 copy reached a terminal that can
// act on it.
type clipboardMsg struct{ OK bool }

// osc52CopyCmd copies text through the terminal's OSC 52 escape sequence —
// the only clipboard channel that works over a plain SSH session without
// extra tooling, which is how dae-tui reaches a remote daed. Terminals that
// ignore it simply keep their old clipboard.
func osc52CopyCmd(text string) tea.Cmd {
	return func() tea.Msg {
		if !stdoutIsTerminal() {
			// Piped output (tests, logs) must not be polluted with escape
			// sequences, and there is no clipboard to fill anyway.
			return clipboardMsg{}
		}
		// The sequence is written straight to the terminal: bubbletea's
		// renderer owns the screen cells, and OSC 52 occupies none of them.
		seq := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07"
		_, err := os.Stdout.WriteString(seq)
		return clipboardMsg{OK: err == nil}
	}
}

// stdoutIsTerminal reports whether stdout is a character device (a tty).
func stdoutIsTerminal() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
