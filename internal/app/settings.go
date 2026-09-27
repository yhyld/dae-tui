package app

import (
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"dae-tui/internal/config"
	"dae-tui/internal/driver"
	"dae-tui/internal/ui"
)

// settings is the global settings window (P): a menu floating over any page.
// Each menu entry opens a sub-window (sub* below); account management moved
// here from the home page (P used to open it directly), the rest fill in as
// they land — disabled entries render dim and are skipped by the cursor.
type settings struct {
	open bool
	cur  int

	// Which sub-window is up: subMenu shows the menu itself.
	sub int

	// account sub-window state (the old homePage.acct machine).
	acctCur int
	user    string
	pwFocus int // 0 current, 1 new, 2 confirm
	pwCur   textinput.Model
	pwNew   textinput.Model
	pwRepeat textinput.Model
	pwErr   string

	// theme picker state: the candidate list is resolved (concrete colors,
	// builtins then user files) when the picker opens, so new files show up
	// without a restart. themeActive is the config's current theme name —
	// the ● marker; esc restores what was on screen when the picker opened.
	themes     []config.Theme
	themeNotes []string
	themeCur   int
	themeActive string

	// version rides in from main (ldflags / build info) for the about window.
	version string
}

// Settings menu entries.
const (
	itemAccount = iota
	itemTheme
	itemLang
	itemKeys
	itemAbout
)

// Settings sub-windows.
const (
	subMenu = iota
	subAcctMenu
	subPwForm
	subLogoutConfirm
	subTheme
	subAbout
)

// aboutLogo is the placeholder dot-matrix emblem (a diamond, braille)
// rendered in the accent color. Deliberately easy to swap once a real logo
// is designed: it is one string slice in one place.
var aboutLogo = []string{
	"⠀⠀⠀⣀⣴⣦⣀⠀⠀⠀",
	"⠀⣴⣾⣿⣿⣿⣿⣷⣦⠀",
	"⠀⠈⠙⠿⣿⣿⠿⠋⠁⠀",
	"⠀⠀⠀⠀⠈⠁⠀⠀⠀⠀",
}

// Version is stamped by main from the build (ldflags -X / build info) and
// shown in the about window.
var Version = "dev"

// settingsItem is one settings menu row; disabled rows render dim and are
// skipped by the cursor.
type settingsItem struct {
	label   string
	enabled bool
}

var settingsItems = []settingsItem{
	{"账户", true},
	{"主题", true},
	{"语言", false},
	{"快捷键", false},
	{"关于", true},
}

func newSettings() settings {
	s := settings{version: Version}
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

// key routes the settings window's keys. Sub-windows keep their own state
// machines; the menu itself only needs j/k/enter/esc.
func (s *settings) key(m *Model, msg tea.KeyMsg) tea.Cmd {
	switch s.sub {
	case subAcctMenu, subPwForm, subLogoutConfirm:
		return s.acctKey(msg, m.drv)
	case subTheme:
		return m.themeKey(msg)
	case subAbout:
		if msg.String() == "esc" || msg.String() == "enter" {
			s.sub = subMenu
		}
		return nil
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
			case itemAccount:
				s.sub = subAcctMenu
				s.acctCur = 0
			case itemTheme:
				m.openThemePicker()
			case itemAbout:
				s.sub = subAbout
			}
		}
	}
	return nil
}

// cfgThemeDir returns the theme directory; ok=false when the OS config path
// is unavailable (the picker then just shows the built-ins).
func (m *Model) cfgThemeDir() (string, bool) {
	dir, err := config.ThemeDir()
	return dir, err == nil
}

// openThemePicker rebuilds the candidate list from the built-ins and the
// theme dir, every entry resolved to concrete colors over the config's
// inline values (what a partial theme file inherits). It parks the cursor
// on the theme config.toml currently names.
func (m *Model) openThemePicker() {
	s := &m.settings
	s.sub = subTheme
	s.themes = nil
	s.themeNotes = nil
	var user []config.Theme
	if dir, ok := m.cfgThemeDir(); ok {
		var notes []string
		var err error
		user, notes, err = config.LoadThemes(dir)
		if err != nil {
			notes = append(notes, "读取主题目录失败: "+err.Error())
		}
		s.themeNotes = notes
	}
	for _, t := range append(config.BuiltinThemes(), user...) {
		s.themes = append(s.themes, t.Resolved(m.cfg.Accent, m.cfg.Border, m.cfg.Dim))
	}
	s.themeActive = m.cfg.Theme
	if s.themeActive == "" {
		s.themeActive = "默认"
	}
	s.themeCur = 0
	for i, t := range s.themes {
		if t.Name == s.themeActive {
			s.themeCur = i
			break
		}
	}
}

// themeKey drives the theme picker: j/k previews immediately (ApplyTheme
// rebuilds every derived style and is cheap), enter persists the choice to
// config.toml, esc restores the theme that was active when the picker
// opened. The whole UI repaints under the preview on the next frame — the
// fastest color swatch there is.
func (m *Model) themeKey(msg tea.KeyMsg) tea.Cmd {
	s := &m.settings
	switch msg.String() {
	case "esc":
		ui.ApplyTheme(m.theme.Accent, m.theme.Border, m.theme.Dim)
		s.sub = subMenu
	case "j", "down":
		if s.themeCur < len(s.themes)-1 {
			s.themeCur++
			m.previewTheme()
		}
	case "k", "up":
		if s.themeCur > 0 {
			s.themeCur--
			m.previewTheme()
		}
	case "enter":
		if s.themeCur < 0 || s.themeCur >= len(s.themes) {
			return nil
		}
		t := s.themes[s.themeCur]
		m.cfg.Theme = t.Name
		if err := m.cfg.Save(m.cfgPath); err != nil {
			m.showErrToast("✗ 保存主题: " + shortErr(err))
			return nil
		}
		m.theme = t
		m.showToast("✓ 主题: " + t.Name)
		s.sub = subMenu
	}
	return nil
}

func (m *Model) previewTheme() {
	t := m.settings.themes[m.settings.themeCur]
	ui.ApplyTheme(t.Accent, t.Border, t.Dim)
}

// acctKey drives the account menu, the password form and the logout
// confirmation. esc from the account menu returns to the settings menu
// (s.sub = subMenu), esc from the settings menu itself closes it.
func (s *settings) acctKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	switch s.sub {
	case subAcctMenu: // menu
		switch msg.String() {
		case "esc":
			s.sub = subMenu
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
				s.sub = subPwForm
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
			s.sub = subLogoutConfirm
		}
		return nil

	case subPwForm: // password form
		switch msg.String() {
		case "esc":
			s.sub = subAcctMenu
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
			s.sub = subMenu
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

	case subLogoutConfirm: // logout confirm
		switch msg.String() {
		case "y":
			s.sub = subMenu
			return func() tea.Msg { return logoutMsg{} }
		case "n", "esc":
			s.sub = subAcctMenu
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

// overlay renders the settings menu and its sub-windows. They float over
// any page, so pageOverlay checks this before the page's own overlays.
func (s settings) overlay() *overlaySpec {
	switch s.sub {
	case subMenu:
		if !s.open {
			return nil
		}
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
	case subTheme:
		return s.themeOverlay()
	case subAbout:
		return s.aboutOverlay()
	case subAcctMenu:
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
	case subPwForm:
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
	case subLogoutConfirm:
		return &overlaySpec{destructive: true, lines: []string{
			"确认退出登录?",
			"将清除本机保存的密码与 token",
			"",
			ui.OKStyle.Render(" y 确认") + "    " + ui.ErrorStyle.Render("n / esc 取消"),
		}}
	}
	return nil
}

// aboutOverlay is the about window: the emblem, version and a short
// orientation of what the tool is and where its config lives.
func (s settings) aboutOverlay() *overlaySpec {
	lines := []string{""}
	for _, l := range aboutLogo {
		lines = append(lines, "    "+ui.TitleStyle.Render(l))
	}
	lines = append(lines, "",
		"    "+ui.SelectedStyle.Render("dae-tui "+s.version),
		"    "+ui.HelpStyle.Render("dae 网络代理的终端管理界面"),
		"",
		"    "+ui.HelpStyle.Render("后端  daed GraphQL API（schema 冻结）"),
		"    "+ui.HelpStyle.Render("配置  ~/.config/dae-tui/config.toml"),
		"    "+ui.HelpStyle.Render("架构  可插拔 driver，可扩展其他后端"),
	)
	return &overlaySpec{lines: append(lines, "",
		ui.HelpStyle.Render(" esc 返回"))}
}

// themeOverlay is the theme picker: one row per theme with the ● marker on
// the theme config.toml currently names and three color swatches per row —
// the preview repaints the whole UI anyway, the swatches say which row is
// which at a glance. Unreadable files show up as dim notes below the list.
func (s settings) themeOverlay() *overlaySpec {
	lines := []string{ui.TitleStyle.Render(" 主题"), ""}
	for i, t := range s.themes {
		mark, style := "  ", ui.HelpStyle
		if i == s.themeCur {
			mark, style = "❯ ", ui.CursorStyle
		}
		row := style.Render(mark+t.Name)
		if t.Name == s.themeActive {
			row += ui.OKStyle.Render(" ●")
		}
		row += "  " + themeSwatches(t)
		lines = append(lines, row)
	}
	for _, n := range s.themeNotes {
		lines = append(lines, "  "+ui.HelpStyle.Render("⚠ "+n))
	}
	return &overlaySpec{lines: append(lines, "",
		ui.HelpStyle.Render(" j/k 预览  Enter 使用  esc 取消"))}
}

// themeSwatches renders the three theme colors as small blocks (accent,
// border, dim), so the list reads even on terminals that make the subtle
// frame-color difference hard to see.
func themeSwatches(t config.Theme) string {
	block := func(c string) string {
		if c == "" {
			return ui.HelpStyle.Render("··")
		}
		return lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Render("██")
	}
	return block(t.Accent) + block(t.Border) + block(t.Dim)
}
