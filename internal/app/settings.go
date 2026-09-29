package app

import (
	"fmt"
	"strings"

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

	// keys viewer: window start, catalog cursor, pending-rebind flag and
	// the last validation error (rendered inline above the catalog).
	keysScroll int
	keysCursor int
	keysEdit   bool
	keysErr    string

	// autoReload mirrors cfg.AutoReload for the menu row's state suffix;
	// the root model keeps it in step (enterMain + the toggle's msg).
	autoReload bool
}

const (
	itemAccount = iota
	itemTheme
	itemLang
	itemAutoReload
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

// aboutLogo is the About window's mark: the home page's traffic chart
// miniaturized on a 30x20 dot grid (15 cells x 5 rows) — six bar columns
// over a dotted axis inside a rounded frame. Frame and axis take the
// accent at normal weight and the columns the accent bold: the mark reads
// in the theme's brand color end to end — a dim gray frame washed out to
// near-invisible on real terminals — while the data still leads through
// weight and density. Six candidates were designed (catalog with dot maps
// in logo_test.go); to swap, paste another candidate's braille —
// single-tone candidates go entirely in aboutLogoBars with an all-blank
// aboutLogoFrame — TestAboutLogoIsKnownCandidate keeps the file honest.
var (
	// aboutLogoFrame is the furniture layer: frame edges, 3-dot-radius
	// corner arcs and the dotted axis, rendered in the accent at normal
	// weight.
	aboutLogoFrame = []string{
		"⠀⢀⠤⠤⠤⠤⠤⠤⠤⠤⠤⠤⠤⡀⠀",
		"⠀⡇⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⢸⠀",
		"⠀⡇⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⢸⠀",
		"⠀⡇⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⣀⢸⠀",
		"⠀⠈⠒⠒⠒⠒⠒⠒⠒⠒⠒⠒⠒⠁⠀",
	}
	// aboutLogoBars is the data layer: the six columns, bottoms one dot
	// row above the axis — the sparkline's grammar, bars rise from just
	// over the reference line — rendered bold in the accent.
	aboutLogoBars = []string{
		"⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀",
		"⠀⠀⠀⠀⣤⠀⣿⠀⣶⠀⣤⠀⠀⠀⠀",
		"⠀⠀⣤⠀⣿⠀⣿⠀⣿⠀⣿⠀⣤⠀⠀",
		"⠀⠀⠿⠀⠿⠀⠿⠀⠿⠀⠿⠀⠿⠀⠀",
		"⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀",
	}
)

// brailleBase is U+2800, the all-dots-off braille cell.
const brailleBase = 0x2800

// aboutLogoLines renders the two logo layers as indented overlay lines. A
// cell holding any bar dot is emitted whole in the bold accent style with
// both layers' dots merged — bars sit on the axis, so those cells carry
// furniture dots too and the bar has to win; every other lit cell takes
// the accent at normal weight. Styles are read at render time so the
// theme picker's live preview repaints the mark with the new accent.
func aboutLogoLines() []string {
	lines := make([]string, 0, len(aboutLogoBars))
	for i, bars := range aboutLogoBars {
		var frame string
		if i < len(aboutLogoFrame) {
			frame = aboutLogoFrame[i]
		}
		lines = append(lines, "    "+mergeLogoLayers(frame, bars))
	}
	return lines
}

// mergeLogoLayers overlays the data layer onto the furniture layer, cell
// by cell. The layers are otherwise dot-disjoint; only the axis row
// shares cells with the bar bottoms.
func mergeLogoLayers(dim, accent string) string {
	plain := lipgloss.NewStyle().Foreground(ui.Accent)
	d, a := []rune(dim), []rune(accent)
	var sb strings.Builder
	for i := range a {
		dm := 0
		if i < len(d) {
			dm = int(d[i]) - brailleBase
		}
		am := int(a[i]) - brailleBase
		switch {
		case am != 0:
			sb.WriteString(ui.TitleStyle.Render(string(rune(brailleBase + (dm | am)))))
		case dm != 0:
			sb.WriteString(plain.Render(string(rune(brailleBase + dm))))
		default:
			sb.WriteRune(brailleBase)
		}
	}
	return sb.String()
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
	{"自动重载", true},
	{"快捷键", true},
	{"关于", true},
}

func newSettings() settings {
	s := settings{version: Version}
	s.pwCur = newPasswordInput()
	s.pwNew = newPasswordInput()
	s.pwRepeat = newPasswordInput()
	return s
}

// newPasswordInput builds a password field without baking in a translated
// placeholder — the sub-password form re-translates it at render time, so a
// settings-window language switch shows through immediately.
func newPasswordInput() textinput.Model {
	ti := textinput.New()
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
			case itemAutoReload:
				return s.toggleAutoReload(m)
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

// toggleAutoReload flips the auto-reload preference and persists it through
// the locked setter (the whole-file save races the background token
// refresh). The outcome returns as autoReloadToggledMsg so the root model —
// not the page — decides what the menu shows on failure.
func (s *settings) toggleAutoReload(m *Model) tea.Cmd {
	want, path, cfgr := !m.cfg.AutoReload, m.cfgPath, m.cfg
	return func() tea.Msg {
		if err := cfgr.UpdateAutoReload(path, want); err != nil {
			return autoReloadToggledMsg{Err: err}
		}
		return autoReloadToggledMsg{On: want}
	}
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
		// The builtin's name is the literal "默认" — translating here would
		// break the t.Name comparison below in English mode (no theme would
		// ever match its own stored name).
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
		if err := m.cfg.UpdateTheme(m.cfgPath, t.Name); err != nil {
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
			if i == itemAutoReload {
				state := i18n.T("关闭")
				if s.autoReload {
					state = i18n.T("开启")
				}
				label += ui.HelpStyle.Render("  " + state)
			}
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
		// Placeholders re-translate per render (see newPasswordInput).
		s.pwCur.Placeholder = i18n.T("当前密码")
		s.pwNew.Placeholder = i18n.T("新密码 (至少6位, 含字母和数字)")
		s.pwRepeat.Placeholder = i18n.T("确认新密码")
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
	lines = append(lines, aboutLogoLines()...)
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
		if err := m.cfg.UpdateLang(m.cfgPath, e.code); err != nil {
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

// keysViewerWin is the entry rows the keys overlay window shows.
const keysViewerWin = 18

// keysKey drives the keymap editor: j/k move the cursor over the catalog,
// Enter starts a rebind (the next press becomes the new key), d restores
// the default, r re-reads keys.toml (edits made outside still land), esc
// returns to the settings menu — or cancels a pending rebind first.
func (m *Model) keysKey(msg tea.KeyMsg) tea.Cmd {
	s := &m.settings
	if s.keysEdit {
		switch msg.String() {
		case "esc":
			s.keysEdit, s.keysErr = false, ""
			return nil
		}
		return m.applyKeyBinding(msg.String())
	}
	switch msg.String() {
	case "esc":
		s.sub = subMenu
	case "j", "down":
		if s.keysCursor < len(keyCatalog)-1 {
			s.keysCursor++
		}
	case "k", "up":
		if s.keysCursor > 0 {
			s.keysCursor--
		}
	case "g":
		s.keysCursor = 0
	case "G":
		s.keysCursor = len(keyCatalog) - 1
	case "enter", "e":
		s.keysEdit, s.keysErr = true, ""
	case "d":
		return m.applyKeyDefault()
	case "r":
		m.reloadKeys()
	}
	s.keysFollow()
	return nil
}

// keysFollow keeps the window on the cursor: the entry's line index comes
// from the same layout pass the renderer uses, so the two cannot drift.
func (s *settings) keysFollow() {
	_, entryLine, total := keysLayout(s.keysErr)
	if len(entryLine) == 0 {
		return
	}
	line := entryLine[s.clampKeysCursor()]
	win := keysViewerWin
	if line < s.keysScroll {
		s.keysScroll = line
	}
	if line >= s.keysScroll+win {
		s.keysScroll = line - win + 1
	}
	if max := max0(total - win); s.keysScroll > max {
		s.keysScroll = max
	}
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

func (s *settings) clampKeysCursor() int {
	if s.keysCursor < 0 {
		return 0
	}
	if s.keysCursor >= len(keyCatalog) {
		return len(keyCatalog) - 1
	}
	return s.keysCursor
}

// applyKeyBinding commits a pressed key as the cursor entry's new binding.
// Validation rejects here (with an inline message, the user is mid-flow)
// rather than at load time: fixed keys, and a conflict with another live
// binding — same scope, or any global key, which every page dispatches
// through first and would therefore shadow a page action.
func (m *Model) applyKeyBinding(newKey string) tea.Cmd {
	s := &m.settings
	e := keyCatalog[s.clampKeysCursor()]
	s.keysEdit = false
	if keymap.Fixed[newKey] {
		s.keysErr = newKey + i18n.T("：固定键不能作为快捷键")
		return nil
	}
	if newKey == e.def {
		return m.applyKeyDefault() // bound to its own default: a reset
	}
	for _, o := range keyCatalog {
		if o.scope == e.scope && o.def == e.def {
			continue
		}
		clashScope := o.scope == e.scope || o.scope == keymap.Global || e.scope == keymap.Global
		if !clashScope {
			continue
		}
		if K(o.scope, o.def) == newKey {
			s.keysErr = newKey + i18n.T(" 已被占用：") + scopeTitle(o.scope) + " / " + i18n.T(o.desc)
			return nil
		}
	}
	// A hand-written binding the catalog does not know still occupies the
	// key — say so instead of silently shadowing it. The global scope must
	// be checked too: its dispatch runs ahead of every page's, so a global
	// override on the key would shadow the page action being created here.
	if def, ok := appKeys.BoundTo(e.scope, newKey); ok && def != e.def {
		s.keysErr = newKey + i18n.T(" 已被键位文件中的 ") + e.scope + "/" + def + i18n.T(" 占用")
		return nil
	}
	if e.scope != keymap.Global {
		if def, ok := appKeys.BoundTo(keymap.Global, newKey); ok {
			s.keysErr = newKey + i18n.T(" 已被键位文件中的 ") + keymap.Global + "/" + def + i18n.T(" 占用")
			return nil
		}
	}
	if err := appKeys.Set(keymap.Path(m.cfgPath), e.scope, e.def, newKey); err != nil {
		s.keysErr = i18n.T("保存失败: ") + shortErr(err)
		return nil
	}
	s.keysErr = ""
	m.showToast("✓ " + i18n.T(e.desc) + ": " + e.def + " → " + newKey)
	return nil
}

// applyKeyDefault restores the cursor entry's default key and drops the
// override from keys.toml.
func (m *Model) applyKeyDefault() tea.Cmd {
	s := &m.settings
	e := keyCatalog[s.clampKeysCursor()]
	s.keysEdit = false
	if K(e.scope, e.def) == e.def {
		s.keysErr = ""
		return nil
	}
	if err := appKeys.Set(keymap.Path(m.cfgPath), e.scope, e.def, e.def); err != nil {
		s.keysErr = i18n.T("保存失败: ") + shortErr(err)
		return nil
	}
	s.keysErr = ""
	m.showToast("✓ " + i18n.T(e.desc) + i18n.T(" 已恢复默认 ") + e.def)
	return nil
}

// keysLayout builds the overlay's pinned head block (title, load notes,
// the validation error — always on screen) and the flat body line index of
// every catalog entry, so the key handler's follow math and the renderer
// share one source of truth. entryLine indexes are into the scrolling body.
func keysLayout(keysErr string) (head []string, entryLine []int, total int) {
	head = []string{ui.TitleStyle.Render(" " + i18n.T("快捷键")), ""}
	for _, n := range appKeys.Notes() {
		head = append(head, "  "+ui.HelpStyle.Render("⚠ "+ui.Truncate(n, 56)))
	}
	if keysErr != "" {
		head = append(head, "  "+ui.ErrorStyle.Render("✗ "+ui.Truncate(keysErr, 56)))
	}
	lastScope := ""
	for _, e := range keyCatalog {
		if e.scope != lastScope {
			lastScope = e.scope
			total += 2 // blank line + scope header
		}
		entryLine = append(entryLine, total)
		total++
	}
	return head, entryLine, total
}

// keysRowW is the selection bar width in the keys viewer: wide enough for
// the longest row (cursor mark + 12-col key column + description) and
// deliberately constant, so the overlay's content-sized box does not
// breathe as the cursor moves between rows.
const keysRowW = 58

// keysOverlay renders the catalog grouped by scope with a cursor: the live
// key column (what actually works right now), the action description, and
// the rebind state riding on the cursor row.
func (s settings) keysOverlay() *overlaySpec {
	head, entryLine, total := keysLayout(s.keysErr)
	cur := s.clampKeysCursor()
	curLine := -1
	if cur >= 0 && cur < len(entryLine) {
		curLine = entryLine[cur]
	}
	start := s.keysScroll
	if max := max0(total - keysViewerWin); start > max {
		start = max
	}
	if curLine >= 0 && curLine < start {
		start = curLine
	}
	if curLine >= start+keysViewerWin {
		start = curLine - keysViewerWin + 1
	}
	var body []string
	lastScope := ""
	for i, e := range keyCatalog {
		if e.scope != lastScope {
			lastScope = e.scope
			body = append(body, "", "  "+ui.SelectedStyle.Render(scopeTitle(e.scope)))
		}
		mark, style := "  ", ui.HelpStyle
		if i == cur {
			mark, style = "❯ ", ui.CursorStyle
		}
		row := " " + mark + style.Render(ui.PadRight(K(e.scope, e.def), 12)) +
			ui.HelpStyle.Render(i18n.T(e.desc))
		if i == cur {
			row = ui.HiRow(row, keysRowW, true)
			if s.keysEdit {
				row += ui.SelectedStyle.Render(i18n.T("  ← 按新键…"))
			}
		}
		body = append(body, row)
	}
	end := start + keysViewerWin
	if end > len(body) {
		end = len(body)
	}
	footer := i18n.T(" j/k 移动  Enter 改键  d 恢复默认  r 重载  esc 返回")
	if s.keysEdit {
		footer = i18n.T(" 按下新键  esc 取消")
	}
	lines := append(append([]string(nil), head...), body[start:end]...)
	lines = append(lines, "",
		ui.HelpStyle.Render(footer)+
			ui.HelpStyle.Render(fmt.Sprintf("  %d-%d/%d", start+1, end, len(body))))
	return &overlaySpec{lines: lines}
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
