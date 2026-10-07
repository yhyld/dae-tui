package app

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/yhyld/dae-tui/internal/i18n"
	"github.com/yhyld/dae-tui/internal/ui"
)

func strongEnough(p string) bool {
	if len([]rune(p)) < 6 {
		return false
	}
	var hasLetter, hasDigit bool
	for _, r := range p {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}
	return hasLetter && hasDigit
}

type loginForm struct {
	setup    bool
	username textinput.Model
	password textinput.Model
	confirm  textinput.Model
	focus    int
	busy     bool
	err      string
}

func newLoginForm(setup bool) loginForm {
	u := textinput.New()
	u.Placeholder = i18n.T("用户名")
	u.CharLimit = 64
	u.Width = 36
	u.Focus()

	p := textinput.New()
	p.Placeholder = i18n.T("密码 (至少6位, 含字母和数字)")
	p.EchoMode = textinput.EchoPassword
	p.EchoCharacter = '•'
	p.CharLimit = 128
	p.Width = 36

	f := loginForm{setup: setup, username: u, password: p}
	if setup {
		c := textinput.New()
		c.Placeholder = i18n.T("确认密码")
		c.EchoMode = textinput.EchoPassword
		c.EchoCharacter = '•'
		c.CharLimit = 128
		c.Width = 36
		f.confirm = c
	}
	return f
}

func (f loginForm) init() tea.Cmd {
	return textinput.Blink
}

func (f loginForm) Update(msg tea.Msg) (loginForm, tea.Cmd) {
	var cmds []tea.Cmd
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "tab", "shift+tab", "up", "down":
			delta := 1
			if k.String() == "shift+tab" || k.String() == "up" {
				delta = -1
			}
			n := 2
			if f.setup {
				n = 3
			}
			f.focus = (f.focus + delta + n) % n
			f.setFocus()
			return f, nil
		}
	}
	var cmd tea.Cmd
	switch f.focus {
	case 0:
		f.username, cmd = f.username.Update(msg)
	case 1:
		f.password, cmd = f.password.Update(msg)
	case 2:
		f.confirm, cmd = f.confirm.Update(msg)
	}
	cmds = append(cmds, cmd)
	return f, tea.Batch(cmds...)
}

func (f *loginForm) setFocus() {
	f.username.Blur()
	f.password.Blur()
	if f.setup {
		f.confirm.Blur()
	}
	switch f.focus {
	case 0:
		f.username.Focus()
	case 1:
		f.password.Focus()
	case 2:
		f.confirm.Focus()
	}
}

func (f *loginForm) values() (string, string, bool) {
	u := strings.TrimSpace(f.username.Value())
	p := f.password.Value()
	if u == "" || p == "" {
		f.err = i18n.T("用户名和密码不能为空")
		return "", "", false
	}
	if f.setup && p != f.confirm.Value() {
		f.err = i18n.T("两次输入的密码不一致")
		return "", "", false
	}
	if f.setup && !strongEnough(p) {
		f.err = i18n.T("密码至少 6 位，且需包含字母和数字")
		return "", "", false
	}
	return u, p, true
}

func (f loginForm) View(endpoint string, setup bool, w, h int) string {
	title := i18n.T("登录 daed")
	if setup {
		title = i18n.T("初始化 daed 账号（尚无用户）")
	}
	lines := []string{
		ui.HelpStyle.Render(endpoint),
		"",
		field(i18n.T("用户名"), f.focus == 0, f.username.View()),
		field(i18n.T("密码"), f.focus == 1, f.password.View()),
	}
	if setup {
		lines = append(lines, field(i18n.T("确认"), f.focus == 2, f.confirm.View()))
	}
	if f.busy {
		lines = append(lines, "", ui.HelpStyle.Render(i18n.T("正在验证…")))
	}
	if f.err != "" {
		lines = append(lines, ui.ErrorStyle.Render("✗ "+f.err))
	}
	lines = append(lines, "", ui.HelpStyle.Render(i18n.T(" Tab 切换焦点  Enter 提交  ")+submitHint(setup)))

	bw := 46
	for _, l := range lines {
		if lw := lipgloss.Width(l) + 4; lw > bw {
			bw = lw
		}
	}
	if w > 0 && bw > w-2 {
		bw = w - 2
	}
	box := ui.TitledBox(title, false, bw, lines)
	return lipgloss.Place(max0(w), max0(h), lipgloss.Center, lipgloss.Center, strings.Join(box, "\n"))
}

func submitHint(setup bool) string {
	if setup {
		return i18n.T("esc 返回")
	}
	return i18n.T("esc 退出")
}

func field(label string, focused bool, input string) string {
	style := ui.HelpStyle
	if focused {
		style = ui.SelectedStyle
	}
	return style.Render(" "+ui.PadRight(label, 6)) + " " + input
}
