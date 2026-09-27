package app

import (
	"context"
	"errors"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"dae-tui/internal/config"
	"dae-tui/internal/driver"
	"dae-tui/internal/i18n"
	"dae-tui/internal/ui"
)

const (
	pageHome = iota
	pageTree
	pageSubs
	pageNodes
	pageConfigs
)

const (
	phaseBoot = iota
	phaseLogin
	phaseSetup
	phaseMain
	phaseFatal
)

type Model struct {
	drv     driver.Driver
	cfg     *config.Config
	cfgPath string

	theme config.Theme

	caps driver.Caps

	latHist *latHistory

	phase  int
	fatal  error
	status driver.Status

	page    int
	home    homePage
	groups  groupsPage
	subs    subsPage
	nodes   nodesPage
	configs configsPage

	helpOpen   bool
	helpScroll int

	settings settings

	login loginForm

	width, height int
	toast         string
	toastAt       time.Time
	toastDur      time.Duration

	lastClickPage int
	lastClickX    int
	lastClickY    int
	lastClickAt   time.Time
	ticks         int
	spin          int
	spinning      bool
	confirmApply  bool

	refreshing  bool
	refreshLeft int
	refreshAt   time.Time
}

const (
	minTermW = 60
	minTermH = 12

	doubleClickWindow = 400 * time.Millisecond
)

// errUnsupported is the toast a gated key produces on a backend whose
// Capabilities do not cover the operation. Translated per call so a live
// language switch shows up on the next gated keypress.
func errUnsupported() error { return errors.New(i18n.T("当前后端不支持该操作")) }

func unsupportedCmd(op string) tea.Cmd {
	return func() tea.Msg { return opDoneMsg{Op: op, Err: errUnsupported()} }
}

func New(drv driver.Driver, cfg *config.Config, cfgPath string) Model {
	m := Model{drv: drv, cfg: cfg, cfgPath: cfgPath, caps: drv.Capabilities(), latHist: newLatHistory()}
	m.home = newHomePage(m.caps)
	m.home.presets = drv.RoutingPresets()
	m.groups = newGroupsPage(m.caps)
	m.subs = newSubsPage(m.caps)
	m.nodes = newNodesPage(m.caps)
	m.nodes.hist = m.latHist
	m.configs = newConfigsPage(m.caps)
	m.settings = newSettings()
	m.theme, _ = config.ResolveTheme(cfg.Theme, cfg.Accent, cfg.Border, cfg.Dim)

	m.configs.builtin = cfg.Editor == "builtin"
	return m
}

func (m Model) Init() tea.Cmd {
	hasAuth := cfgHasAuth(m.cfg)
	return tea.Batch(tickCmd(0), bootCmd(m.drv, hasAuth))
}

func cfgHasAuth(c *config.Config) bool {
	return c.HasAuth()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {

	if m.phase == phaseMain {
		if err := msgAuthErr(msg); errors.Is(err, driver.ErrNeedAuth) {
			m.phase = phaseLogin
			m.settings.open, m.settings.sub = false, subMenu
			m.login = newLoginForm(false)
			m.login.username.SetValue(m.cfg.Username)
			m.login.err = i18n.T("登录已失效（凭据被拒绝），请重新登录")
			return m, m.login.init()
		}
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case spinnerMsg:
		m.spin++
		if m.anyTesting() || m.anyBusy() || m.refreshing {
			return m, spinnerTickCmd()
		}
		m.spinning = false
		return m, nil

	case tickMsg:
		m.ticks = msg.n
		if !m.toastAt.IsZero() && time.Since(m.toastAt) > m.toastDur {
			m.toast, m.toastAt = "", time.Time{}
		}

		if m.refreshStale() {
			m.refreshing, m.refreshLeft = false, 0
		}
		var cmds []tea.Cmd
		cmds = append(cmds, tickCmd(msg.n))
		if m.phase == phaseMain {
			if (m.anyTesting() || m.anyBusy() || m.refreshing) && !m.spinning {
				m.spinning = true
				cmds = append(cmds, spinnerTickCmd())
			}
			if m.caps.TrafficStats {
				cmds = append(cmds, trafficCmd(m.drv))
			}
			if m.groups.testing {
				cmds = append(cmds, latenciesCmd(m.drv, m.groups.testIDs))
			}
			if m.subs.testing {
				cmds = append(cmds, latenciesCmd(m.drv, m.subs.testIDs))
			}
			if m.nodes.testing {
				cmds = append(cmds, latenciesCmd(m.drv, m.nodes.testIDs))
			}
			if msg.n%5 == 0 {
				cmds = append(cmds, statusCmd(m.drv))
			}

			if msg.n%10 == 0 {
				cmds = append(cmds, loadInterfacesCmd(m.drv))
			}

			if msg.n%3 == 0 {
				if cmd := m.latencyPollCmd(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}
		return m, tea.Batch(cmds...)

	case bootMsg:
		if msg.ConnErr != nil {
			m.fatal = msg.ConnErr
			m.phase = phaseFatal
			return m, nil
		}
		switch {
		case msg.Users == 0:
			m.phase = phaseSetup
			m.login = newLoginForm(true)
			return m, m.login.init()
		case msg.AuthErr != nil || msg.Status.Version == "":
			m.phase = phaseLogin
			m.login = newLoginForm(false)
			m.login.username.SetValue(m.cfg.Username)
			return m, m.login.init()
		default:
			m.status = msg.Status
			m.enterMain()
			return m, m.initialLoad()
		}

	case authMsg:
		if msg.Err != nil {
			m.login.err = msg.Err.Error()
			m.login.busy = false
			return m, nil
		}

		m.settings.user = m.cfg.Username
		m.enterMain()
		return m, m.initialLoad()

	case logoutMsg:

		if err := m.cfg.ClearSession(m.cfgPath); err != nil {
			m.showErrToast(i18n.T("✗ 清除本机凭据失败: ") + shortErr(err))
		}
		if err := m.drv.Logout(context.Background()); err != nil {
			m.showErrToast(i18n.T("✗ 退出登录: ") + shortErr(err))
		}
		m.settings.open, m.settings.sub = false, subMenu
		m.settings.user = ""
		m.phase = phaseLogin
		m.login = newLoginForm(false)
		return m, m.login.init()

	case gotoGroupMsg:

		m.page = pageTree
		for i, g := range m.groups.groups {
			if g.ID == msg.ID {
				m.groups.gi = i
				break
			}
		}
		m.groups.focus = 1
		m.groups.rc = 0
		m.groups.rebuild()
		return m, nil

	case logsDoneMsg:
		return m, reenableMouse()

	case statusMsg:
		if msg.Err == nil {
			m.status = msg.Status
		}
		return m, nil

	case refreshDoneMsg:

		if m.refreshing {
			m.refreshLeft--
			if m.refreshLeft <= 0 {
				m.refreshing, m.refreshLeft = false, 0
			}
		}
		return m, nil

	case trafficMsg:
		m.home.apiTook = msg.Took
		if msg.Err == nil {
			m.home.update(msg.Snap)
		} else {
			m.home.err = msg.Err
		}
		return m, nil

	case groupsMsg:
		m.groups.handleGroups(msg.Groups, msg.Err)
		m.home.handleGroups(msg.Groups, msg.Err)
		m.nodes.setGroups(msg.Groups)
		m.configs.setGroups(msg.Groups)
		return m, nil

	case manualNodesMsg:
		m.groups.handleManualNodes(msg.Nodes, msg.Err)
		m.nodes.handleNodes(msg.Nodes, msg.Err)
		return m, nil

	case importDoneMsg:
		m.nodes.handleImport(msg)

		if msg.Err == nil {
			m.showToast(m.nodes.importToast())
		}
		return m, nil

	case nodesChangedMsg:
		m.nodes.handleNodes(msg.Nodes, msg.Err)
		m.groups.handleGroups(msg.Groups, nil)
		m.nodes.setGroups(msg.Groups)
		return m, nil

	case attachCandidatesMsg:
		m.groups.handleCandidates(msg)
		return m, nil

	case subNodesMsg:
		m.subs.handleSubNodes(msg.SubID, msg.Nodes, msg.Err)
		return m, nil

	case latenciesMsg:
		for _, l := range msg.Lats {
			m.latHist.add(l)
		}
		m.groups.handleLatencies(msg.Lats, msg.Err)
		m.home.handleLatencies(msg.Lats)
		m.subs.handleLatencies(msg.Lats)
		m.nodes.handleLatencies(msg.Lats)
		return m, nil

	case subsMsg:
		m.subs.handleSubs(msg.Subs, msg.Err)
		m.groups.setSubs(msg.Subs, msg.Err)
		m.home.setSubs(msg.Subs, msg.Err)

		if cmd := m.subs.ensureNodes(m.drv); cmd != nil {
			return m, cmd
		}
		return m, nil

	case selectionsMsg:
		m.configs.handleSelections(msg.Sel, msg.Err)
		m.home.handleSelections(msg.Sel, msg.Err, m.drv)
		m.groups.setReferences(msg.Sel.Routings)
		return m, nil

	case ifacesMsg:
		m.home.setInterfaces(msg.Ifaces)
		m.configs.setInterfaces(msg.Ifaces)
		return m, nil

	case editorDoneMsg:
		return m, tea.Batch(m.configs.handleEditorDone(msg, m.drv), reenableMouse())

	case editorValidatedMsg:
		return m, m.configs.handleValidated(msg, m.drv)

	case presetValidatedMsg:
		return m, m.home.handleValidated(msg, m.drv)

	case opDoneMsg:
		if msg.Err != nil {
			m.showErrToast("✗ " + msg.Op + ": " + shortErr(msg.Err))
		} else {
			m.showToast("✓ " + msg.Op)
		}

		switch msg.Idle {
		case busySubs:
			m.subs.busy = false
		case busyNodes:
			m.nodes.busy = false
		}
		return m, nil

	case clipboardMsg:
		if msg.OK {
			m.showToast(i18n.T("✓ 已复制") + msg.What + " (OSC 52)")
		} else {
			m.showErrToast(i18n.T("✗ 复制失败：当前输出不是终端"))
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.MouseMsg:
		return m.handleMouse(msg)
	}

	if m.phase == phaseLogin || m.phase == phaseSetup {
		var cmd tea.Cmd
		m.login, cmd = m.login.Update(msg)
		return m, cmd
	}
	return m, nil
}

func msgAuthErr(msg tea.Msg) error {
	switch v := msg.(type) {
	case statusMsg:
		return v.Err
	case groupsMsg:
		return v.Err
	case manualNodesMsg:
		return v.Err
	case importDoneMsg:
		return v.Err
	case nodesChangedMsg:
		return v.Err
	case attachCandidatesMsg:
		return v.Err
	case subNodesMsg:
		return v.Err
	case latenciesMsg:
		return v.Err
	case trafficMsg:
		return v.Err
	case subsMsg:
		return v.Err
	case selectionsMsg:
		return v.Err
	case ifacesMsg:
		return v.Err
	case opDoneMsg:
		return v.Err
	case editorDoneMsg:
		return v.Err
	case editorValidatedMsg:
		return v.Err
	case presetValidatedMsg:
		return v.Err
	}
	return nil
}

func (m *Model) enterMain() {
	m.phase = phaseMain
	m.page = pageHome
	m.settings.user = m.cfg.Username
	m.settings.open, m.settings.sub = false, subMenu
}

func (m *Model) initialLoad() tea.Cmd {
	cmds := []tea.Cmd{
		loadGroupsCmd(m.drv),
		loadSubsCmd(m.drv),
		loadSelectionsCmd(m.drv),
		loadManualNodesCmd(m.drv),
		loadInterfacesCmd(m.drv),
	}
	if m.caps.TrafficStats {
		cmds = append(cmds, trafficCmd(m.drv))
	}

	return tea.Batch(cmds...)
}

func (m Model) latencyPollCmd() tea.Cmd {
	ids := m.visibleLatencyIDs()
	if len(ids) == 0 {
		return nil
	}
	return latenciesCmd(m.drv, ids)
}

func (m Model) visibleLatencyIDs() []string {
	switch m.page {
	case pageTree:
		return m.groups.visibleLatencyIDs()
	case pageSubs:
		return m.subs.visibleLatencyIDs()
	case pageNodes:
		return m.nodes.visibleLatencyIDs()
	}
	return nil
}

func (m *Model) layout() {

	cw := max0(m.width - 2)
	ch := max0(m.height - 6)
	if cw < 1 {
		cw = 1
	}
	if ch < 1 {
		ch = 1
	}
	m.home.setSize(cw, ch)

	leftW := cw / 3
	if leftW > 38 {
		leftW = 38
	}
	if leftW < 22 {
		leftW = 22
	}
	rightW := cw - leftW - 3
	if rightW < 30 {
		rightW = 30
	}
	m.groups.setSize(leftW, rightW, ch)
	m.subs.setSize(leftW, rightW, ch)
	m.nodes.setSize(leftW, rightW, ch)
	m.configs.setSize(leftW, rightW, ch)
}

func (m *Model) showToast(s string) {
	m.toast, m.toastAt, m.toastDur = s, time.Now(), 4*time.Second
}

func (m *Model) showErrToast(s string) {
	m.toast, m.toastAt, m.toastDur = s, time.Now(), 8*time.Second
}

func stackedDetail(infoLen, h int) (topH, bottomInner int) {
	topH = infoLen + 2
	if topH > h-2 {
		topH = h - 2
	}
	if topH < 2 {
		topH = 2
	}
	return topH, max0(h - topH - 2)
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.Index(s, "access denied"); i >= 0 {
		return i18n.T("认证失败 (access denied)")
	}
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.phase {
	case phaseBoot:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil

	case phaseFatal:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			return m, tea.Quit
		case "r":
			m.fatal = nil
			m.phase = phaseBoot
			return m, tea.Batch(tickCmd(m.ticks), bootCmd(m.drv, cfgHasAuth(m.cfg)))
		}
		return m, nil

	case phaseLogin, phaseSetup:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			if m.phase == phaseLogin {
				return m, tea.Quit
			}
		case "enter":
			if u, p, ok := m.login.values(); ok {
				m.login.busy = true
				m.login.err = ""
				return m, loginCmd(m.drv, m.phase == phaseSetup, u, p)
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.login, cmd = m.login.Update(msg)
		return m, cmd
	}

	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	if m.confirmApply {
		switch msg.String() {
		case "y":
			m.confirmApply = false
			return m, runCmd(m.drv, false)
		case "n", "esc":
			m.confirmApply = false
		}
		return m, nil
	}

	if m.helpOpen {
		switch msg.String() {
		case "esc", "?", "enter":
			m.helpOpen = false
		case "j", "down":
			m.helpScroll += 3
		case "k", "up":
			m.helpScroll -= 3
		case "g":
			m.helpScroll = 0
		case "G":
			m.helpScroll = len(helpLines())
		}
		m.helpScroll = clampHelpScroll(m.helpScroll, helpWinBody(m.height-6))
		return m, nil
	}

	if m.settings.open {
		return m, m.settings.key(&m, msg)
	}

	if !m.anyModal() {
		switch msg.String() {
		case "q":
			return m, tea.Quit
		case "A":
			m.confirmApply = true
			return m, nil
		case "P":

			m.settings.open = true
			m.settings.cur = 0
			return m, nil
		case "1":
			m.page = pageHome
			return m, nil
		case "2":
			m.page = pageTree
			return m, nil
		case "3":
			m.page = pageSubs
			return m, nil
		case "4":
			m.page = pageNodes
			return m, nil
		case "5":
			m.page = pageConfigs
			return m, nil
		case "?":
			m.helpOpen = true
			m.helpScroll = clampHelpScroll(helpSectionStart(m.page), helpWinBody(m.height-6))
			return m, nil
		case "r":

			if m.refreshing {
				return m, nil
			}
			return m, m.forceRefresh()
		}
	}

	var cmd tea.Cmd
	switch m.page {
	case pageHome:
		cmd = m.home.handleKey(msg, m.drv, m.status.Running)
	case pageTree:
		cmd = m.groups.handleKey(msg, m.drv)
	case pageSubs:
		cmd = m.subs.handleKey(msg, m.drv)
	case pageNodes:
		cmd = m.nodes.handleKey(msg, m.drv)
	case pageConfigs:
		cmd = m.configs.handleKey(msg, m.drv)
	}
	return m, cmd
}

func (m Model) anyModal() bool {
	if m.helpOpen || m.settings.open {
		return true
	}
	switch m.page {
	case pageTree:
		return m.groups.mode != pickNone || m.groups.nodeView.open
	case pageSubs:
		return m.subs.mode != 0 || m.subs.nodeView.open
	case pageNodes:
		return m.nodes.mode != 0 || m.nodes.nodeView.open
	case pageConfigs:
		return m.configs.mode != 0
	case pageHome:

		return m.home.confirmSwitch || m.home.confirmPreset >= 0
	}
	return false
}

func runeKey(name string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(name)}
}

func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.phase != phaseMain || m.width < minTermW || m.height < minTermH {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		if m.confirmApply {
			return m, nil
		}
		if m.helpOpen {
			if msg.Button == tea.MouseButtonWheelDown {
				m.helpScroll += 3
			} else {
				m.helpScroll -= 3
			}
			m.helpScroll = clampHelpScroll(m.helpScroll, helpWinBody(m.height-6))
			return m, nil
		}
		if m.anyModal() {
			return m, nil
		}
		name := "j"
		if msg.Button == tea.MouseButtonWheelUp {
			name = "k"
		}
		switch m.page {
		case pageHome:
			if name == "j" {
				m.home.scrollBy(3)
			} else {
				m.home.scrollBy(-3)
			}
			return m, nil
		case pageConfigs:

			return m, m.configs.handleKey(runeKey(name), m.drv)
		case pageTree:
			return m, m.groups.handleKey(runeKey(name), m.drv)
		case pageSubs:

			return m, m.subs.handleKey(runeKey(name), m.drv)
		case pageNodes:
			return m, m.nodes.handleKey(runeKey(name), m.drv)
		}
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		return m.click(msg.X, msg.Y)
	}
	return m, nil
}

func (m Model) click(x, y int) (tea.Model, tea.Cmd) {
	if m.anyModal() || m.confirmApply {
		return m, nil
	}
	cx := x - 2
	if y == 1 && x >= 5 {
		return m.tabClick(x - 5)
	}
	if y == m.height-1 {

		m.helpOpen = true
		m.helpScroll = clampHelpScroll(helpSectionStart(m.page), helpWinBody(m.height-6))
		return m, nil
	}
	if y < 5 || cx < 0 {
		return m, nil
	}
	row := y - 5

	now := time.Now()
	dbl := m.page == m.lastClickPage && x == m.lastClickX && y == m.lastClickY &&
		now.Sub(m.lastClickAt) < doubleClickWindow
	m.lastClickPage, m.lastClickX, m.lastClickY, m.lastClickAt = m.page, x, y, now
	var cmd tea.Cmd
	switch m.page {
	case pageHome:
		m.home.click(row, cx, m.status)
	case pageTree:
		if cx <= m.groups.leftW {
			m.groups.leftClick(row)
		} else {
			m.groups.rightClick(row)
		}
	case pageSubs:
		if cx <= m.subs.leftW {

			cmd = m.subs.leftClick(row, m.drv)
		} else {
			m.subs.rightClick(row)
		}
	case pageNodes:
		if cx <= m.nodes.leftW {
			m.nodes.leftClick(row)
		}
	case pageConfigs:
		if cx <= m.configs.leftW {
			m.configs.leftClick(row)
		} else {
			m.configs.rightClick(row)
		}
	}

	if dbl && (m.page == pageTree || m.page == pageSubs || m.page == pageNodes) {
		cmd = tea.Batch(cmd, func() tea.Msg { return tea.KeyMsg{Type: tea.KeyEnter} })
	}
	return m, cmd
}

func (m Model) tabClick(cx int) (tea.Model, tea.Cmd) {
	for i, t := range tabLabels() {
		w := lipgloss.Width(t)
		if cx < w+2 {
			m.page = i
			return m, nil
		}
		cx -= w + 2
	}
	return m, nil
}

func (m *Model) forceRefresh() tea.Cmd {
	cmds := []tea.Cmd{
		statusCmd(m.drv),
		loadGroupsCmd(m.drv),
		loadSubsCmd(m.drv),
		loadSelectionsCmd(m.drv),
		loadManualNodesCmd(m.drv),
		loadInterfacesCmd(m.drv),
	}
	if m.caps.TrafficStats {
		cmds = append(cmds, trafficCmd(m.drv))
	}

	if cmd := m.latencyPollCmd(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	m.refreshing, m.refreshLeft, m.refreshAt = true, len(cmds), time.Now()
	wrapped := make([]tea.Cmd, 0, len(cmds))
	for _, c := range cmds {
		wrapped = append(wrapped, countRefreshCmd(c))
	}
	return tea.Batch(wrapped...)
}

const refreshTimeout = 15 * time.Second

func (m Model) refreshStale() bool {
	return m.refreshing && time.Since(m.refreshAt) > refreshTimeout
}

func countRefreshCmd(c tea.Cmd) tea.Cmd {
	return func() tea.Msg {
		return tea.BatchMsg{c, func() tea.Msg { return refreshDoneMsg{} }}
	}
}

func (m Model) View() string {
	switch m.phase {
	case phaseBoot:
		return bootView(m.cfg.Endpoint, m.width, m.height)

	case phaseFatal:
		return fatalView(m.cfg.Endpoint, m.fatal, m.width, m.height)

	case phaseLogin, phaseSetup:
		return m.login.View(m.cfg.Endpoint, m.phase == phaseSetup, m.width, m.height)
	}

	if m.width < minTermW || m.height < minTermH {
		return smallView(m.width, m.height)
	}

	var body string
	switch m.page {
	case pageHome:
		body = m.home.View(m.status)
	case pageTree:
		body = m.groups.View()
	case pageSubs:
		body = m.subs.View()
	case pageNodes:
		body = m.nodes.View()
	case pageConfigs:
		body = m.configs.View()
	}

	avail := m.height - 6
	if avail < 1 {
		avail = 1
	}
	cw := m.width - 2

	if ov := m.pageOverlay(); ov != nil {
		body = ui.Overlay(body, overlayBox(ov, cw-1), cw-1, avail)
	}
	if m.confirmApply {
		body = ui.Overlay(body, overlayBox(&overlaySpec{destructive: true, lines: []string{
			ui.TitleStyle.Render(i18n.T(" 确认重载 (run)？")),
			i18n.T(" 重载会按当前状态重新生成 dae 配置：方案切换、"),
			i18n.T(" 订阅更新、群组改动都要重载后才生效。"),
			"",
			ui.OKStyle.Render(i18n.T(" y 重载")) + "    " + ui.ErrorStyle.Render(i18n.T("n / esc 取消")),
		}}, cw-1), cw-1, avail)
	}
	if m.helpOpen {
		body = ui.Overlay(body, helpOverlayBox(cw-1, avail, m.helpScroll), cw-1, avail)
	}
	pageLines := strings.Split(body, "\n")
	if len(pageLines) > avail {
		pageLines = pageLines[:avail]
	}
	for len(pageLines) < avail {
		pageLines = append(pageLines, "")
	}
	for i := range pageLines {
		if pageLines[i] != "" {
			pageLines[i] = " " + ui.Truncate(pageLines[i], cw-1)
		}
	}

	header := ui.TitledBoxRight(strings.TrimLeft(m.tabsBar(), " "), m.spinSuffix(), false, cw-2,
		[]string{m.statusBar()})
	for i := range header {
		header[i] = " " + ui.Truncate(header[i], cw-1)
	}
	frame := append(header, pageLines...)
	frame = append(frame, m.toastLine())

	keys, hint := m.helpKeys()
	return ui.AppFrame(m.width, m.height, keys, hint, frame)
}

func smallView(w, h int) string {
	msg := i18n.T("终端太小 (%d×%d)\n\ndae-tui 至少需要 %d×%d，建议 80×24。\n放大窗口后自动恢复。",
		w, h, minTermW, minTermH)
	return lipgloss.Place(max0(w), max0(h), lipgloss.Center, lipgloss.Center, msg)
}

func shortEndpoint(ep string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(ep, "https://"), "http://")
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	return s
}

func (m Model) statusBar() string {
	run := ui.OKStyle.Render(i18n.T("● 运行中"))
	if !m.status.Running {
		run = ui.ErrorStyle.Render(i18n.T("○ 未运行"))
	}
	mod := ""
	if m.status.Modified {
		mod = ui.ErrorStyle.Render(i18n.T(" ⚠ 需重载 (A)"))
	}
	left := ui.TitleStyle.Render("dae-tui") + ui.HelpStyle.Render(" ("+shortEndpoint(m.cfg.Endpoint)+")") +
		"  " + run + ui.HelpStyle.Render(" dae "+m.status.Version) + mod
	right := ""
	if m.caps.TrafficStats {
		s := m.home.snap
		right = ui.HelpStyle.Render("↑" + ui.Rate(s.UpRate) + " ↓" + ui.Rate(s.DownRate))
	}
	inner := m.width - 8
	if right != "" {
		if gap := inner - lipgloss.Width(left) - lipgloss.Width(right); gap >= 2 {
			return left + strings.Repeat(" ", gap) + right
		}
	}
	return ui.Truncate(left, max0(inner))
}

// tabLabels keys the tab strip; translated at render so a language switch
// re-labels the tabs (tabClick derives spans from the same function, so the
// click math stays in sync with what is on screen).
func tabLabels() []string {
	return []string{i18n.T("1 首页"), i18n.T("2 群组"), i18n.T("3 订阅"),
		i18n.T("4 手动节点"), i18n.T("5 配置")}
}

var spinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

func (m Model) tabsBar() string {
	parts := make([]string, len(tabLabels()))
	for i, t := range tabLabels() {
		if m.page == i {
			parts[i] = ui.TabActive.Render(t)
		} else {
			parts[i] = ui.TabStyle.Render(t)
		}
	}
	return strings.Join(parts, "")
}

func (m Model) spinSuffix() string {
	var parts []string
	if m.refreshing {
		parts = append(parts, i18n.T("刷新中"))
	}
	if m.anyBusy() {
		parts = append(parts, i18n.T("处理中"))
	}
	if done, total := m.testProgress(); total > 0 {
		parts = append(parts, i18n.T("测速中 %d/%d", done, total))
	}
	if len(parts) == 0 {
		return ""
	}
	return ui.TitleStyle.Render(string(spinnerFrames[m.spin%len(spinnerFrames)])) +
		ui.HelpStyle.Render(" "+strings.Join(parts, " · "))
}

func reenableMouse() tea.Cmd {
	return tea.EnableMouseCellMotion
}

func (m Model) anyTesting() bool {
	return m.groups.testing || m.subs.testing || m.nodes.testing
}

func (m Model) anyBusy() bool {
	return m.subs.busy || m.nodes.busy
}

func (m Model) testProgress() (int, int) {
	done, total := m.groups.testProgress()
	d, t := m.subs.testProgress()
	done, total = done+d, total+t
	d, t = m.nodes.testProgress()
	return done + d, total + t
}

func (m Model) toastLine() string {
	if m.toast == "" {
		return ""
	}

	return " " + ui.Truncate(m.toast, m.width-3)
}

func (m Model) helpKeys() (keys, hint string) {
	keys = i18n.T("A 重载  1-5 切换页面  q 退出")
	switch m.page {
	case pageHome:
		keys = i18n.T("L 日志  P 设置  A 重载  r 刷新")
	case pageTree:
		keys = i18n.T("Tab 切栏  a 自动策略  t/T 测速  A 重载  r 刷新")
	case pageSubs:
		keys = i18n.T("u 更新  e 编辑  n 新增  y 复制链接  A 重载  r 刷新")
	case pageNodes:
		keys = i18n.T("a 导入  e 编辑  y 复制  t/T 测速  Tab 切栏  A 重载  r 刷新")
	case pageConfigs:
		keys = i18n.T("v 概览/原文  y 复制 DSL  l/Enter 详情  A 重载  r 刷新")
	}

	hint = i18n.T("? 帮助")
	if m.helpOpen {
		keys = i18n.T("j/k 滚动  g/G 首尾")
		hint = i18n.T("esc 关闭帮助")
	}
	return ui.HelpStyle.Render(keys), ui.HelpStyle.Render(hint)
}

type overlaySpec struct {
	destructive bool
	lines       []string
}

func overlayBox(spec *overlaySpec, maxW int) string {
	if spec == nil || len(spec.lines) == 0 {
		return ""
	}
	boxW := 40
	for _, l := range spec.lines {
		if w := lipgloss.Width(l); w+6 > boxW {
			boxW = w + 6
		}
	}
	if boxW > maxW {
		boxW = maxW
	}
	lines := make([]string, len(spec.lines))
	for i, l := range spec.lines {
		lines[i] = ui.Truncate(l, max0(boxW-6))
	}
	return strings.Join(ui.BoxLines(spec.destructive, lines...), "\n")
}

func (m Model) pageOverlay() *overlaySpec {
	if ov := m.settings.overlay(); ov != nil {
		return ov
	}
	if ov := m.home.overlay(); ov != nil {
		return ov
	}
	switch m.page {
	case pageTree:
		return m.groups.overlay()
	case pageSubs:
		return m.subs.overlay()
	case pageNodes:
		return m.nodes.overlay()
	case pageConfigs:
		return m.configs.overlay()
	}
	return nil
}

func bootView(endpoint string, w, h int) string {
	box := ui.TitledBox("dae-tui", false, 44, []string{
		ui.TitleStyle.Render(i18n.T("正在连接 ")) + ui.HelpStyle.Render(endpoint) + " …",
		"",
		ui.HelpStyle.Render(i18n.T("ctrl+c 退出")),
	})
	return lipgloss.Place(max0(w), max0(h), lipgloss.Center, lipgloss.Center, strings.Join(box, "\n"))
}

func fatalView(endpoint string, err error, w, h int) string {
	lines := []string{
		ui.ErrorStyle.Render(i18n.T("无法连接 daed")) + ui.HelpStyle.Render(" ("+endpoint+")"),
		"",
		ui.ErrorStyle.Render(ui.Truncate(err.Error(), 58)),
		"",
		i18n.T("检查: daed 是否在运行 (systemctl status daed)、"),
		i18n.T("endpoint 是否正确。远程实例推荐"),
		i18n.T("ssh -L 2023:127.0.0.1:2023 <host> 后用默认地址。"),
		"",
		ui.HelpStyle.Render(i18n.T("r 重试  q 退出")),
	}
	bw := 64
	if w > 0 && bw > w-2 {
		bw = w - 2
	}
	box := ui.TitledBox(i18n.T("连接失败"), true, bw, lines)
	return lipgloss.Place(max0(w), max0(h), lipgloss.Center, lipgloss.Center, strings.Join(box, "\n"))
}
