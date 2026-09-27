package app

import (
	"fmt"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"dae-tui/internal/config"
	"dae-tui/internal/driver"
	"dae-tui/internal/i18n"
	"dae-tui/internal/keymap"
	"dae-tui/internal/ui"
)

type settings struct {
	open bool
	cur  int

	sub int

	acctCur  int
	user     string
	pwFocus  int
	pwCur    textinput.Model
	pwNew    textinput.Model
	pwRepeat textinput.Model
	pwErr    string

	themes      []config.Theme
	themeNotes  []string
	themeCur    int
	themeActive string

	version string

	// language picker cursor.
	langCur int

	// keys viewer scroll offset (the catalog outgrows the window).
	keysScroll int
}

const (
	itemAccount = iota
	itemTheme
	itemLang
	itemKeys
	itemAbout
)

const (
	subMenu = iota
	subAcctMenu
	subPwForm
	subLogoutConfirm
	subTheme
	subAbout
	subLang
	subKeys
)

// langEntry is one language picker row. The name is written in the language
// itself and never translated — that is how users recognize their language.
var langEntries = []struct{ name, code string }{
	{"中文", "zh"},
	{"English", "en"},
}

var aboutLogo = []string{
	"⠀⠀⠀⣀⣴⣦⣀⠀⠀⠀",
	"⠀⣴⣾⣿⣿⣿⣿⣷⣦⠀",
	"⠀⠈⠙⠿⣿⣿⠿⠋⠁⠀",
	"⠀⠀⠀⠀⠈⠁⠀⠀⠀⠀",
}

var Version = "dev"

type settingsItem struct {
	label   string
	enabled bool
}

var settingsItems = []settingsItem{
	{"账户", true},
	{"主题", true},
	{"语言", true},
	{"快捷键", true},
	{"关于", true},
}

func newSettings() settings {
	s := settings{version: Version}
	s.pwCur = newPasswordInput(i18n.T("当前密码"))
	s.pwNew = newPasswordInput(i18n.T("新密码 (至少6位, 含字母和数字)"))
	s.pwRepeat = newPasswordInput(i18n.T("确认新密码"))
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
	case subLang:
		return m.langKey(msg)
	case subKeys:
		return m.keysKey(msg)
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
			case itemLang:
				s.sub = subLang
				s.langCur = 0
				for i, e := range langEntries {
					if e.code == i18n.Lang() {
						s.langCur = i
					}
				}
			case itemKeys:
				s.sub = subKeys
				s.keysScroll = 0
			case itemAbout:
				s.sub = subAbout
			}
		}
	}
	return nil
}

func (m *Model) cfgThemeDir() (string, bool) {
	dir, err := config.ThemeDir()
	return dir, err == nil
}

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
			notes = append(notes, i18n.T("读取主题目录失败: ")+err.Error())
		}
		s.themeNotes = notes
	}
	for _, t := range append(config.BuiltinThemes(), user...) {
		s.themes = append(s.themes, t.Resolved(m.cfg.Accent, m.cfg.Border, m.cfg.Dim))
	}
	s.themeActive = m.cfg.Theme
	if s.themeActive == "" {
		s.themeActive = i18n.T("默认")
	}
	s.themeCur = 0
	for i, t := range s.themes {
		if t.Name == s.themeActive {
			s.themeCur = i
			break
		}
	}
}

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
			m.showErrToast(i18n.T("✗ 保存主题: ") + shortErr(err))
			return nil
		}
		m.theme = t
		m.showToast(i18n.T("✓ 主题: ") + t.Name)
		s.sub = subMenu
	}
	return nil
}

func (m *Model) previewTheme() {
	t := m.settings.themes[m.settings.themeCur]
	ui.ApplyTheme(t.Accent, t.Border, t.Dim)
}

func (s *settings) acctKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	switch s.sub {
	case subAcctMenu:
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

	case subPwForm:
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
				s.pwErr = i18n.T("当前密码和新密码不能为空")
				return nil
			}
			if nw != s.pwRepeat.Value() {
				s.pwErr = i18n.T("两次输入的新密码不一致")
				return nil
			}
			if !strongEnough(nw) {
				s.pwErr = i18n.T("新密码至少 6 位，且需包含字母和数字")
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

	case subLogoutConfirm:
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

func (s settings) overlay() *overlaySpec {
	switch s.sub {
	case subMenu:
		if !s.open {
			return nil
		}
		lines := []string{ui.TitleStyle.Render(i18n.T(" 设置")), "",
			ui.HelpStyle.Render(i18n.T(" 当前用户  ") + s.user), ""}
		for i, it := range settingsItems {
			mark, style := "  ", ui.HelpStyle
			if i == s.cur {
				mark, style = "❯ ", ui.CursorStyle
			}
			label := i18n.T(it.label)
			if !it.enabled {
				label += i18n.T("（即将支持）")
			}
			lines = append(lines, style.Render(mark+label))
		}
		return &overlaySpec{lines: append(lines,
			"", ui.HelpStyle.Render(i18n.T(" j/k 选择  Enter 确认  esc 关闭")))}
	case subTheme:
		return s.themeOverlay()
	case subLang:
		return s.langOverlay()
	case subKeys:
		return s.keysOverlay()
	case subAbout:
		return s.aboutOverlay()
	case subAcctMenu:
		lines := []string{ui.TitleStyle.Render(i18n.T(" 账户")), "",
			ui.HelpStyle.Render(i18n.T(" 当前用户  ") + s.user), ""}
		items := []string{i18n.T("修改密码"), i18n.T("退出登录")}
		for i, it := range items {
			mark, style := "  ", ui.HelpStyle
			if i == s.acctCur {
				mark, style = "❯ ", ui.CursorStyle
			}
			lines = append(lines, style.Render(mark+it))
		}
		return &overlaySpec{lines: append(lines,
			ui.HelpStyle.Render(i18n.T(" j/k 选择  Enter 确认  esc 返回")))}
	case subPwForm:
		lines := []string{ui.TitleStyle.Render(i18n.T(" 修改密码")), ""}
		for i, f := range []struct {
			label string
			input textinput.Model
		}{
			{i18n.T("当前密码"), s.pwCur}, {i18n.T("新密码"), s.pwNew}, {i18n.T("确认新密码"), s.pwRepeat},
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
			ui.HelpStyle.Render(i18n.T(" Tab 切换  Enter 提交  esc 返回")))}
	case subLogoutConfirm:
		return &overlaySpec{destructive: true, lines: []string{
			i18n.T("确认退出登录?"),
			i18n.T("将清除本机保存的密码与 token"),
			"",
			ui.OKStyle.Render(i18n.T(" y 确认")) + "    " + ui.ErrorStyle.Render(i18n.T("n / esc 取消")),
		}}
	}
	return nil
}

func (s settings) aboutOverlay() *overlaySpec {
	lines := []string{""}
	for _, l := range aboutLogo {
		lines = append(lines, "    "+ui.TitleStyle.Render(l))
	}
	lines = append(lines, "",
		"    "+ui.SelectedStyle.Render("dae-tui "+s.version),
		"    "+ui.HelpStyle.Render(i18n.T("dae 网络代理的终端管理界面")),
		"",
		"    "+ui.HelpStyle.Render(i18n.T("后端  daed GraphQL API（schema 冻结）")),
		"    "+ui.HelpStyle.Render(i18n.T("配置  ~/.config/dae-tui/config.toml")),
		"    "+ui.HelpStyle.Render(i18n.T("架构  可插拔 driver，可扩展其他后端")),
	)
	return &overlaySpec{lines: append(lines, "",
		ui.HelpStyle.Render(i18n.T(" esc 返回")))}
}

func (s settings) themeOverlay() *overlaySpec {
	lines := []string{ui.TitleStyle.Render(i18n.T(" 主题")), ""}
	for i, t := range s.themes {
		mark, style := "  ", ui.HelpStyle
		if i == s.themeCur {
			mark, style = "❯ ", ui.CursorStyle
		}
		row := style.Render(mark + t.Name)
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
		ui.HelpStyle.Render(i18n.T(" j/k 预览  Enter 使用  esc 取消")))}
}

// langKey drives the language picker: enter switches immediately (every
// render-time i18n.T repaints translated on the next frame) and persists
// the choice to config.toml.
func (m *Model) langKey(msg tea.KeyMsg) tea.Cmd {
	s := &m.settings
	switch msg.String() {
	case "esc":
		s.sub = subMenu
	case "j", "down":
		if s.langCur < len(langEntries)-1 {
			s.langCur++
		}
	case "k", "up":
		if s.langCur > 0 {
			s.langCur--
		}
	case "enter":
		if s.langCur < 0 || s.langCur >= len(langEntries) {
			return nil
		}
		e := langEntries[s.langCur]
		i18n.SetLang(e.code)
		m.cfg.Lang = e.code
		if err := m.cfg.Save(m.cfgPath); err != nil {
			m.showErrToast("✗ " + i18n.T("保存语言: ") + shortErr(err))
			return nil
		}
		m.showToast("✓ " + i18n.T("语言") + ": " + e.name)
		s.sub = subMenu
	}
	return nil
}

// langOverlay lists the languages with the ● marker on the active one.
func (s settings) langOverlay() *overlaySpec {
	lines := []string{ui.TitleStyle.Render(i18n.T(" 语言")), ""}
	for i, e := range langEntries {
		mark, style := "  ", ui.HelpStyle
		if i == s.langCur {
			mark, style = "❯ ", ui.CursorStyle
		}
		row := style.Render(mark + e.name)
		if e.code == i18n.Lang() {
			row += ui.OKStyle.Render(" ●")
		}
		lines = append(lines, row)
	}
	return &overlaySpec{lines: append(lines, "",
		ui.HelpStyle.Render(i18n.T(" j/k 选择  Enter 确认  esc 返回")))}
}

// keysKey drives the read-only keymap viewer: j/k/g/G scroll the catalog,
// r re-reads keys.toml (the file is the editor — the viewer names it), esc
// returns to the settings menu.
func (m *Model) keysKey(msg tea.KeyMsg) tea.Cmd {
	s := &m.settings
	switch tk(keymap.Global, msg.String()) {
	case "esc":
		s.sub = subMenu
	case "j", "down":
		s.keysScroll++
	case "k", "up":
		s.keysScroll--
	case "g":
		s.keysScroll = 0
	case "G":
		s.keysScroll = len(keyCatalog)
	case "r":
		m.reloadKeys()
	}
	s.keysScroll = max0(s.keysScroll)
	return nil
}

// reloadKeys re-reads keys.toml; the toast reports warnings rather than
// losing them — a bad binding silently falling back to its default would
// read as "remap does not work".
func (m *Model) reloadKeys() {
	appKeys.Reload(keymap.Path(m.cfgPath))
	if notes := appKeys.Notes(); len(notes) > 0 {
		m.showErrToast("✗ " + i18n.T("键位已重载，但有警告: ") + notes[0])
		return
	}
	m.showToast("✓ " + i18n.T("键位已重载"))
}

// keysOverlay renders the catalog grouped by scope: the live key column
// (what actually works right now) and the action description. Load notes
// ride on top — they are why a binding might not have applied.
func (s settings) keysOverlay() *overlaySpec {
	const winH = 22
	lines := []string{ui.TitleStyle.Render(" " + i18n.T("快捷键")), ""}
	for _, n := range appKeys.Notes() {
		lines = append(lines, "  "+ui.HelpStyle.Render("⚠ "+ui.Truncate(n, 56)))
	}
	lastScope := ""
	for _, e := range keyCatalog {
		if e.scope != lastScope {
			lastScope = e.scope
			lines = append(lines, "", "  "+ui.SelectedStyle.Render(scopeTitle(e.scope)))
		}
		row := "  " + ui.CursorStyle.Render(ui.PadRight(K(e.scope, e.def), 12)) +
			ui.HelpStyle.Render(i18n.T(e.desc))
		lines = append(lines, row)
	}
	total := len(lines)
	start := s.keysScroll
	if start > total-winH {
		start = max0(total - winH)
	}
	end := start + winH
	if end > total {
		end = total
	}
	body := lines[start:end]
	body = append(body, "",
		ui.HelpStyle.Render(i18n.T(" j/k 滚动  r 重载 keys.toml  esc 返回"))+
			ui.HelpStyle.Render(fmt.Sprintf("  %d-%d/%d", start+1, end, total)))
	return &overlaySpec{lines: body}
}

func themeSwatches(t config.Theme) string {
	block := func(c string) string {
		if c == "" {
			return ui.HelpStyle.Render("··")
		}
		return lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Render("██")
	}
	return block(t.Accent) + block(t.Border) + block(t.Dim)
}
