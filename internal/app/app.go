// Package app contains the Bubble Tea application: root model with phase
// routing (connect → login/setup → main) and the individual pages.
package app

import (
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

func New(drv driver.Driver, cfg *config.Config, cfgPath string) Model {
	m := Model{drv: drv, cfg: cfg, cfgPath: cfgPath}
	m.home = newHomePage()
	m.groups = newGroupsPage()
	m.subs = newSubsPage()
	m.nodes = newNodesPage()
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
			cmds = append(cmds, trafficCmd(m.drv))
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
			// Background latency data: probes are triggered once at
			// startup (see initialLoad) and manually via t/T; results are
			// polled every 3s so latency columns stay fresh.
			if msg.n%3 == 0 {
				cmds = append(cmds, latenciesCmd(m.drv, nil))
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
		m.enterMain()
		return m, m.initialLoad()

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
		return m, nil

	case manualNodesMsg:
		m.groups.handleManualNodes(msg.Nodes, msg.Err)
		m.nodes.handleNodes(msg.Nodes, msg.Err)
		return m, nil

	case attachCandidatesMsg:
		m.groups.handleCandidates(msg)
		return m, nil

	case subNodesMsg:
		m.subs.handleSubNodes(msg.SubID, msg.Nodes, msg.Err)
		return m, nil

	case latenciesMsg:
		m.groups.handleLatencies(msg.Lats, msg.Err)
		m.home.handleLatencies(msg.Lats)
		m.subs.handleLatencies(msg.Lats)
		m.nodes.handleLatencies(msg.Lats)
		return m, nil

	case subsMsg:
		m.subs.handleSubs(msg.Subs, msg.Err)
		m.groups.setSubs(msg.Subs, msg.Err)
		return m, nil

	case selectionsMsg:
		m.configs.handleSelections(msg.Sel, msg.Err)
		return m, nil

	case editorDoneMsg:
		return m, m.configs.handleEditorDone(msg, m.drv)

	case opDoneMsg:
		if msg.Err != nil {
			m.showToast("✗ " + msg.Op + ": " + shortErr(msg.Err))
		} else {
			m.showToast("✓ " + msg.Op)
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
}

func (m *Model) initialLoad() tea.Cmd {
	return tea.Batch(
		loadGroupsCmd(m.drv),
		loadSubsCmd(m.drv),
		loadSelectionsCmd(m.drv),
		loadManualNodesCmd(m.drv),
		trafficCmd(m.drv),
		testLatencyCmd(m.drv, nil), // populate latency data for the home page
	)
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
		return m.groups.mode != pickNone
	case pageSubs:
		return m.subs.mode != 0
	case pageNodes:
		return m.nodes.mode != 0
	case pageConfigs:
		return m.configs.mode != 0
	}
	return false
}

// forceRefresh reloads data for the current page.
func (m *Model) forceRefresh() tea.Cmd {
	switch m.page {
	case pageHome:
		return tea.Batch(statusCmd(m.drv), loadGroupsCmd(m.drv), trafficCmd(m.drv))
	case pageTree:
		return loadGroupsCmd(m.drv)
	case pageSubs:
		return loadSubsCmd(m.drv)
	case pageNodes:
		return loadManualNodesCmd(m.drv)
	case pageConfigs:
		return loadSelectionsCmd(m.drv)
	}
	return nil
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
	s := m.home.snap
	right := ui.HelpStyle.Render("↑" + ui.Rate(s.UpRate) + " ↓" + ui.Rate(s.DownRate))
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
	if m.groups.testing || m.subs.testing || m.nodes.testing {
		bar += ui.HelpStyle.Render("   ⏳ 测速中…")
	}
	return bar
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
		keys = "o 开关代理  A 应用  r 刷新  1-5 切页"
	case pageTree:
		keys = "j/k 移动  Tab/l 展开  Enter(分区)开合/(节点)固定  x 移除  c/R/D/p 建组/改名/删除/策略  s/n 挂订阅/加节点  t 测速"
	case pageSubs:
		keys = "j/k 移动  Tab/l 看节点  u 更新  n 新增  x 删除  c 定时刷新  t 测速"
	case pageNodes:
		keys = "j/k 移动  a 导入  x 删除  t/T 测速  Tab 后 G 加入群组"
	case pageConfigs:
		keys = "j/k 移动  Enter 选择  e 编辑  c/R/D 新建/改名/删除  Tab/l 滚动  A 应用(全局)"
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
