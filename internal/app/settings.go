package app

import (
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"dae-tui/internal/driver"
	"dae-tui/internal/ui"
)

// settings is the global settings window (P): a menu floating over any page.
// Account management moved here from the home page (P used to open it
// directly); the other entries (theme / language / keys / about) are slots
// that fill in as they land — disabled ones are skipped by the cursor and
// rendered dim with a 即将支持 note.
type settings struct {
	open bool
	cur  int

	// The account sub-window, the old homePage.acct state machine: 1 menu,
	// 2 password form, 3 logout confirm. 0 shows the settings menu itself.
	acct    int
	acctCur int
	user    string
	pwFocus int // 0 current, 1 new, 2 confirm
	pwCur   textinput.Model
	pwNew   textinput.Model
	pwRepeat textinput.Model
	pwErr   string
}

// settingsItem is one settings menu row; disabled rows render dim and are
// skipped by the cursor.
type settingsItem struct {
	label   string
	enabled bool
}

var settingsItems = []settingsItem{
	{"账户", true},
	{"主题", false},
	{"语言", false},
	{"快捷键", false},
	{"关于", false},
}

func newSettings() settings {
	s := settings{}
	s.pwCur = newPasswordInput("当前密码")
	s.pwNew = newPasswordInput("新密码 (至少6位, 含字母和数字)")
	s.pwRepeat = newPasswordInput("确认新密码")
	return s
}

func newPasswordInput(placeholder string) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.EchoMode = textinput.EchoPassword
	ti.EchoCharacter = '•'
	ti.CharLimit = 128
	ti.Width = 28
	return ti
}

// key routes the settings window's keys. The account sub-window keeps its
// own state machine; the menu itself only needs j/k/enter/esc.
func (s *settings) key(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	if s.acct != 0 {
		return s.acctKey(msg, d)
	}
	switch msg.String() {
	case "esc":
		s.open = false
	case "j", "down":
		for i := s.cur + 1; i < len(settingsItems); i++ {
			if settingsItems[i].enabled {
				s.cur = i
				break
			}
		}
	case "k", "up":
		for i := s.cur - 1; i >= 0; i-- {
			if settingsItems[i].enabled {
				s.cur = i
				break
			}
		}
	case "enter":
		if s.cur < len(settingsItems) && settingsItems[s.cur].enabled {
			switch s.cur {
			case 0: // account
				s.acct, s.acctCur = 1, 0
			}
		}
	}
	return nil
}

// acctKey drives the account menu, the password form and the logout
// confirmation. esc from the account menu returns to the settings menu
// (s.acct = 0), esc from the settings menu itself closes it.
func (s *settings) acctKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	switch s.acct {
	case 1: // menu
		switch msg.String() {
		case "esc":
			s.acct = 0
		case "j", "down":
			if s.acctCur < 1 {
				s.acctCur++
			}
		case "k", "up":
			if s.acctCur > 0 {
				s.acctCur--
			}
		case "enter":
			if s.acctCur == 0 {
				s.acct = 2
				s.pwFocus = 0
				s.pwErr = ""
				s.pwCur.SetValue("")
				s.pwNew.SetValue("")
				s.pwRepeat.SetValue("")
				s.pwCur.Focus()
				s.pwNew.Blur()
				s.pwRepeat.Blur()
				return textinput.Blink
			}
			s.acct = 3
		}
		return nil

	case 2: // password form
		switch msg.String() {
		case "esc":
			s.acct = 1
			s.pwCur.Blur()
			s.pwNew.Blur()
			s.pwRepeat.Blur()
			return nil
		case "tab", "shift+tab", "up", "down":
			delta := 1
			if msg.String() == "shift+tab" || msg.String() == "up" {
				delta = -1
			}
			s.pwFocus = (s.pwFocus + delta + 3) % 3
			s.setPwFocus()
			return textinput.Blink
		case "enter":
			cur := s.pwCur.Value()
			nw := s.pwNew.Value()
			if cur == "" || nw == "" {
				s.pwErr = "当前密码和新密码不能为空"
				return nil
			}
			if nw != s.pwRepeat.Value() {
				s.pwErr = "两次输入的新密码不一致"
				return nil
			}
			if !strongEnough(nw) {
				s.pwErr = "新密码至少 6 位，且需包含字母和数字"
				return nil
			}
			s.pwErr = ""
			s.acct = 0
			s.pwCur.Blur()
			s.pwNew.Blur()
			s.pwRepeat.Blur()
			return passwordCmd(d, cur, nw)
		}
		var cmd tea.Cmd
		switch s.pwFocus {
		case 0:
			s.pwCur, cmd = s.pwCur.Update(msg)
		case 1:
			s.pwNew, cmd = s.pwNew.Update(msg)
		case 2:
			s.pwRepeat, cmd = s.pwRepeat.Update(msg)
		}
		return cmd

	case 3: // logout confirm
		switch msg.String() {
		case "y":
			s.acct = 0
			return func() tea.Msg { return logoutMsg{} }
		case "n", "esc":
			s.acct = 1
		}
		return nil
	}
	return nil
}

func (s *settings) setPwFocus() {
	s.pwCur.Blur()
	s.pwNew.Blur()
	s.pwRepeat.Blur()
	switch s.pwFocus {
	case 0:
		s.pwCur.Focus()
	case 1:
		s.pwNew.Focus()
	case 2:
		s.pwRepeat.Focus()
	}
}

// overlay renders the settings menu and the account windows. They float over
// any page, so pageOverlay checks this before the page's own overlays.
func (s settings) overlay() *overlaySpec {
	switch {
	case s.open && s.acct == 0:
		lines := []string{ui.TitleStyle.Render(" 设置"), "",
			ui.HelpStyle.Render(" 当前用户  " + s.user), ""}
		for i, it := range settingsItems {
			mark, style := "  ", ui.HelpStyle
			if i == s.cur {
				mark, style = "❯ ", ui.CursorStyle
			}
			label := it.label
			if !it.enabled {
				label += "（即将支持）"
			}
			lines = append(lines, style.Render(mark+label))
		}
		return &overlaySpec{lines: append(lines,
			"", ui.HelpStyle.Render(" j/k 选择  Enter 确认  esc 关闭"))}
	case s.acct == 1:
		lines := []string{ui.TitleStyle.Render(" 账户"), "",
			ui.HelpStyle.Render(" 当前用户  " + s.user), ""}
		items := []string{"修改密码", "退出登录"}
		for i, it := range items {
			mark, style := "  ", ui.HelpStyle
			if i == s.acctCur {
				mark, style = "❯ ", ui.CursorStyle
			}
			lines = append(lines, style.Render(mark+it))
		}
		return &overlaySpec{lines: append(lines,
			ui.HelpStyle.Render(" j/k 选择  Enter 确认  esc 返回"))}
	case s.acct == 2:
		lines := []string{ui.TitleStyle.Render(" 修改密码"), ""}
		for i, f := range []struct {
			label string
			input textinput.Model
		}{
			{"当前密码", s.pwCur}, {"新密码", s.pwNew}, {"确认新密码", s.pwRepeat},
		} {
			style := ui.HelpStyle
			if i == s.pwFocus {
				style = ui.SelectedStyle
			}
			lines = append(lines, style.Render(" "+ui.PadRight(f.label, 10))+" "+f.input.View())
		}
		if s.pwErr != "" {
			lines = append(lines, "", ui.ErrorStyle.Render(" ✗ "+s.pwErr))
		}
		return &overlaySpec{lines: append(lines, "",
			ui.HelpStyle.Render(" Tab 切换  Enter 提交  esc 返回"))}
	case s.acct == 3:
		return &overlaySpec{destructive: true, lines: []string{
			"确认退出登录?",
			"将清除本机保存的密码与 token",
			"",
			ui.OKStyle.Render(" y 确认") + "    " + ui.ErrorStyle.Render("n / esc 取消"),
		}}
	}
	return nil
}
