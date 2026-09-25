// Package app contains the Bubble Tea application: root model with phase
// routing (connect → login/setup → main) and the individual pages.
package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"dae-tui/internal/config"
	"dae-tui/internal/driver"
	"dae-tui/internal/ui"
)

const (
	pageHome = iota
	pageTree
	pageSubs
	pageNodes
	pageConfigs
	pageHelp
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

	// caps is the backend's feature set, read once at startup. Pages gate
	// their keys on it so a less capable driver degrades with feedback
	// instead of leaving keys that silently do nothing.
	caps driver.Caps

	// latHist accumulates per-node latency samples from the poll stream, for
	// the detail-pane trend sparkline.
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

	login loginForm

	width, height int
	toast         string
	toastAt       time.Time
	ticks         int
	confirmApply  bool // global `A` apply confirmation
}

// errUnsupported is the toast a gated key produces on a backend whose
// Capabilities do not cover the operation.
var errUnsupported = errors.New("当前后端不支持该操作")

// unsupportedCmd reports a capability-gated keypress as a toast.
func unsupportedCmd(op string) tea.Cmd {
	return func() tea.Msg { return opDoneMsg{Op: op, Err: errUnsupported} }
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
	return m
}

func (m Model) Init() tea.Cmd {
	hasAuth := cfgHasAuth(m.cfg)
	return tea.Batch(tickCmd(0), bootCmd(m.drv, hasAuth))
}

func cfgHasAuth(c *config.Config) bool {
	return c.Token != "" || (c.Username != "" && c.Password != "")
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case tickMsg:
		m.ticks = msg.n
		if !m.toastAt.IsZero() && time.Since(m.toastAt) > 4*time.Second {
			m.toast, m.toastAt = "", time.Time{}
		}
		var cmds []tea.Cmd
		cmds = append(cmds, tickCmd(msg.n))
		if m.phase == phaseMain {
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
			// NICs change rarely, but a DHCP renewal is exactly the kind of
			// change worth seeing without a manual refresh.
			if msg.n%10 == 0 {
				cmds = append(cmds, loadInterfacesCmd(m.drv))
			}
			// Latency data is polled for what the current page actually
			// shows: a full-instance poll every 3s is wasted work when the
			// visible list is a dozen nodes, and on a large instance it is
			// the single largest recurring response. Pages that show no
			// nodes (a collapsed group detail, the home page) poll nothing.
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
		// credentials were persisted by the driver's SaveToken hook
		m.home.user = m.cfg.Username
		m.enterMain()
		return m, m.initialLoad()

	case logoutMsg:
		// Forget the session locally: daed's JWT stays valid until it
		// expires, but this machine must stop replaying the credentials.
		m.cfg.Username, m.cfg.Password, m.cfg.Token = "", "", ""
		if err := m.cfg.Save(m.cfgPath); err != nil {
			m.showToast("✗ 清除本机凭据失败: " + shortErr(err))
		}
		m.drv.Logout(context.Background())
		m.home.user = ""
		m.phase = phaseLogin
		m.login = newLoginForm(false)
		return m, m.login.init()

	case gotoGroupMsg:
		// Home page Enter on a group row: open the groups page with that
		// group already expanded, so the node list is one j/k away.
		m.page = pageTree
		for i, g := range m.groups.groups {
			if g.ID == msg.ID {
				m.groups.gi = i
				break
			}
		}
		m.groups.expanded = true
		m.groups.focus = 1
		m.groups.rc = 0
		m.groups.rebuild()
		return m, nil

	case logsDoneMsg:
		return m, nil

	case statusMsg:
		if msg.Err == nil {
			m.status = msg.Status
		}
		return m, nil

	case trafficMsg:
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
		m.showToast(m.nodes.importToast())
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
		// An `u` update dropped the expanded subscription's cached nodes;
		// re-fetch them now instead of waiting for a collapse/expand cycle.
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
		return m, m.configs.handleEditorDone(msg, m.drv)

	case editorValidatedMsg:
		return m, m.configs.handleValidated(msg, m.drv)

	case presetValidatedMsg:
		return m, m.home.handleValidated(msg, m.drv)

	case opDoneMsg:
		if msg.Err != nil {
			m.showToast("✗ " + msg.Op + ": " + shortErr(msg.Err))
		} else {
			m.showToast("✓ " + msg.Op)
		}
		return m, nil

	case clipboardMsg:
		if msg.OK {
			m.showToast("✓ 已复制到剪贴板 (OSC 52)")
		} else {
			m.showToast("✗ 复制失败：当前输出不是终端")
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	// forward non-key messages to focused sub-components (text inputs)
	if m.phase == phaseLogin || m.phase == phaseSetup {
		var cmd tea.Cmd
		m.login, cmd = m.login.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) enterMain() {
	m.phase = phaseMain
	m.page = pageHome
	m.home.user = m.cfg.Username
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
	// No startup-wide latency test on purpose: probing every node of the
	// instance is the heaviest one-time load the tool can cause, it fires on
	// every launch regardless of what the user opened the tool for, and it
	// would probe nodes no visible list asks about. Measurements are created
	// on demand (t/T on a group, subscription or list) and then kept fresh
	// by the per-page poll.
	return tea.Batch(cmds...)
}

// latencyPollCmd polls the latency data the current page displays, or nil
// when the page shows no nodes.
func (m Model) latencyPollCmd() tea.Cmd {
	ids := m.visibleLatencyIDs()
	if len(ids) == 0 {
		return nil
	}
	return latenciesCmd(m.drv, ids)
}

// visibleLatencyIDs lists the nodes whose latency the current page renders.
// The home page deliberately reports nothing: its per-group "current node"
// for automatic policies is an estimate over group members, and keeping it
// exact would mean polling every member of every group — the full-instance
// cost this mechanism exists to avoid. The home page shows the estimate
// without a millisecond figure instead, from whatever has been measured.
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
	chrome := 4 // statusbar + tabs + help + toast lines
	h := m.height - chrome
	if h < 3 {
		h = 3
	}
	w := m.width
	m.home.setSize(w, h)
	leftW := w / 3
	if leftW > 36 {
		leftW = 36
	}
	if leftW < 20 {
		leftW = 20
	}
	rightW := w - leftW - 3
	if rightW < 30 {
		rightW = 30
	}
	m.groups.setSize(leftW, rightW, h)
	m.subs.setSize(leftW, rightW, h)
	m.nodes.setSize(leftW, rightW, h)
	m.configs.setSize(leftW, rightW, h)
}

func (m *Model) showToast(s string) {
	m.toast, m.toastAt = s, time.Now()
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.Index(s, "access denied"); i >= 0 {
		return "认证失败 (access denied)"
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

	// phaseMain
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
	// While a page-level modal/input is open, every keystroke (including
	// the digit page-switch hotkeys — names, cron expressions and links
	// are full of digits) must reach the modal, not the global router.
	if !m.anyModal() {
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "q":
			return m, tea.Quit
		case "A":
			m.confirmApply = true
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
			m.page = pageHelp
			return m, nil
		case "r":
			return m, m.forceRefresh()
		}
	} else if msg.String() == "ctrl+c" {
		return m, tea.Quit
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

// anyModal reports whether the current page has a modal or text input
// open that should receive raw keystrokes.
func (m Model) anyModal() bool {
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
		// A confirmation must be answered before any global hotkey fires.
		return m.home.confirmSwitch || m.home.confirmPreset >= 0 || m.home.acct != 0
	}
	return false
}

// forceRefresh reloads every list from the backend, not just the current
// page's: the pages share data (the home page shows groups, the groups page
// shows subscription tags, the home routing section comes from selections),
// so a per-page refresh left the views you were not looking at stale.
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
	// Latency data is polled on its own cadence; include the current page's
	// poll so a manual refresh feels complete.
	if cmd := m.latencyPollCmd(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

func (m Model) View() string {
	switch m.phase {
	case phaseBoot:
		return bootView(m.cfg.Endpoint)

	case phaseFatal:
		return fatalView(m.cfg.Endpoint, m.fatal)

	case phaseLogin, phaseSetup:
		return m.login.View(m.cfg.Endpoint, m.phase == phaseSetup)
	}

	var b strings.Builder
	b.WriteString(m.statusBar())
	b.WriteString("\n")
	b.WriteString(m.tabsBar())
	b.WriteString("\n")

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
		body = m.configs.View(m.status.Modified)
	case pageHelp:
		body = helpView()
	}
	// Hard clamp: a page must never push the chrome (status/tabs/help)
	// off screen — this was the original scrolling bug.
	lines := strings.Split(body, "\n")
	avail := m.height - 4
	if avail < 1 {
		avail = 1
	}
	if len(lines) > avail {
		lines = lines[:avail]
	}
	b.WriteString(strings.Join(lines, "\n"))
	b.WriteString("\n")
	b.WriteString(m.toastLine())
	b.WriteString("\n")
	b.WriteString(m.helpLine())
	return lipgloss.NewStyle().Width(m.width).Render(b.String())
}

func (m Model) statusBar() string {
	run := ui.OKStyle.Render("● 运行中")
	if !m.status.Running {
		run = ui.ErrorStyle.Render("○ 未运行")
	}
	mod := ""
	if m.status.Modified {
		mod = ui.ErrorStyle.Render(" ⚠ 未应用")
	}
	left := ui.TitleStyle.Render("dae-tui") + ui.HelpStyle.Render(" ("+m.cfg.Endpoint+")") +
		"  " + run + ui.HelpStyle.Render(" dae "+m.status.Version) + mod
	right := ""
	if m.caps.TrafficStats {
		s := m.home.snap
		right = ui.HelpStyle.Render("↑" + ui.Rate(s.UpRate) + " ↓" + ui.Rate(s.DownRate))
	}
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

var tabLabels = []string{"1 首页", "2 群组", "3 订阅", "4 手动节点", "5 配置", "? 帮助"}

func (m Model) tabsBar() string {
	parts := make([]string, len(tabLabels))
	for i, t := range tabLabels {
		if m.page == i {
			parts[i] = ui.TabActive.Render(t)
		} else {
			parts[i] = ui.TabStyle.Render(t)
		}
	}
	bar := strings.Join(parts, "")
	if done, total := m.testProgress(); total > 0 {
		bar += ui.HelpStyle.Render(fmt.Sprintf("   ⏳ 测速中 %d/%d", done, total))
	}
	return bar
}

// testProgress reports the current page's in-flight latency test as
// (returned, total), or a zero total when nothing is being probed.
func (m Model) testProgress() (int, int) {
	switch m.page {
	case pageTree:
		return m.groups.testProgress()
	case pageSubs:
		return m.subs.testProgress()
	case pageNodes:
		return m.nodes.testProgress()
	}
	return 0, 0
}

func (m Model) toastLine() string {
	if m.confirmApply {
		return ui.ErrorStyle.Render(" ▸ 确认应用当前选中 config+dns+routing (run)? (y/n)")
	}
	if m.toast == "" {
		return ""
	}
	return m.toast
}

func (m Model) helpLine() string {
	var keys string
	switch m.page {
	case pageHome:
		keys = "o 开关代理  Tab 切焦点  j/k+Enter 切换路由/跳群组页  L 日志  g 换组  P 账户  A 应用  r 刷新"
	case pageTree:
		keys = "j/k 移动  Tab/l 展开  Enter(分区)开合  space 标记  a 自动策略  x 移除(选中则批量)  c/R/D/p 建组/改名/删除/策略  s/n 挂订阅/加节点  t 测速  / 过滤  o 排序"
	case pageSubs:
		keys = "j/k 移动  Tab/l 看节点  y 复制链接  u 更新  e 编辑标签/链接  n 新增  x 删除  c 定时刷新  t 测速  / 过滤  o 排序"
	case pageNodes:
		keys = "j/k 移动  a 批量导入  y 复制链接  e 编辑标签/链接  x 删除  t/T 测速  Tab 后 G 加入群组  / 过滤  o 排序"
	case pageConfigs:
		keys = "j/k 移动  Enter 选择  y 复制 DSL  e 编辑(DSL 先校验后 diff)  v 概览/原文  c/R/D 新建/改名/删除  Tab/l 滚动  A 应用(全局)"
	default:
		keys = "A 应用  1-5 切换页面  q 退出"
	}
	return ui.HelpStyle.Render(" " + keys)
}

func bootView(endpoint string) string {
	return lipgloss.NewStyle().Width(60).Padding(1, 2).Render(
		ui.TitleStyle.Render("dae-tui")+"\n正在连接 "+endpoint+" …") +
		"\n\n" + ui.HelpStyle.Render("ctrl+c 退出")
}

func fatalView(endpoint string, err error) string {
	return lipgloss.NewStyle().Width(70).Padding(1, 2).Render(
		ui.ErrorStyle.Render("无法连接 daed")+ui.HelpStyle.Render(" ("+endpoint+")")+"\n\n"+
			ui.ErrorStyle.Render(err.Error())+
			"\n\n检查: daed 是否在运行 (systemctl status daed)、endpoint 是否正确。\n远程实例推荐 ssh -L 2023:127.0.0.1:2023 <host> 后用默认地址。") +
		"\n\n" + ui.HelpStyle.Render("r 重试  q 退出")
}
