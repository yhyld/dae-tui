package app

import (
	"encoding/base64"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

type clipboardMsg struct {
	OK   bool
	What string
}

func osc52CopyCmd(text, what string) tea.Cmd {
	return func() tea.Msg {
		if !stdoutIsTerminal() {

			return clipboardMsg{What: what}
		}

		seq := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07"
		_, err := os.Stdout.WriteString(seq)
		return clipboardMsg{OK: err == nil, What: what}
	}
}

func stdoutIsTerminal() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
