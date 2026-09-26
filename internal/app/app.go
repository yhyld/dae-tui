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

	page       int
	home       homePage
	groups     groupsPage
	subs       subsPage
	nodes      nodesPage
	configs    configsPage
	helpScroll int // `?` page scroll offset (the page itself is static)

	login loginForm

	width, height int
	toast         string
	toastAt       time.Time
	ticks         int
	spin          int  // spinner frame counter for the latency-test indicator
	spinning      bool // a spinnerMsg chain is alive (guards against stacking chains)
	confirmApply  bool // global `A` apply confirmation
}

// Terminal floors: below them the two-pane math no longer fits and the
// per-line truncation would turn the screen into wrapped garbage, so the
// app renders a plain "resize me" page instead.
const (
	minTermW = 60
	minTermH = 12
)

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

	case spinnerMsg:
		m.spin++
		if m.anyTesting() {
			return m, spinnerTickCmd()
		}
		m.spinning = false
		return m, nil

	case tickMsg:
		m.ticks = msg.n
		if !m.toastAt.IsZero() && time.Since(m.toastAt) > 4*time.Second {
			m.toast, m.toastAt = "", time.Time{}
		}
		var cmds []tea.Cmd
		cmds = append(cmds, tickCmd(msg.n))
		if m.phase == phaseMain {
			if m.anyTesting() && !m.spinning {
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
		return m, reenableMouse()

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
		return m, tea.Batch(m.configs.handleEditorDone(msg, m.drv), reenableMouse())

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

	case tea.MouseMsg:
		return m.handleMouse(msg)
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
	// The app frame (a rounded border around everything) costs 2 columns
	// and 2 rows; statusbar+tabs+toast+help cost 4 more.
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
	if leftW > 36 {
		leftW = 36
	}
	if leftW < 20 {
		leftW = 20
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
			m.helpScroll = 0
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
	case pageHelp:
		// The help page is one static block; its only interaction is scroll.
		switch msg.String() {
		case "j", "down":
			m.helpScroll += 3
		case "k", "up":
			m.helpScroll -= 3
		case "g":
			m.helpScroll = 0
		case "G":
			m.helpScroll = 1 << 30 // clamped right below
		}
		m.helpScroll = clampHelpScroll(m.helpScroll, m.height-6)
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

// runeKey builds the KeyMsg the wheel handler replays into a page — the
// same message a physical j/k press produces.
func runeKey(name string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(name)}
}

// handleMouse routes mouse input. The wheel scrolls whatever the current
// page scrolls with j/k (three rows per notch); a click switches tabs or
// selects a row. While a modal is open the page owns every keystroke, and
// the mouse is ignored — a stray click mid-form is worse than no click.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.phase != phaseMain || m.width < minTermW || m.height < minTermH {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		if m.anyModal() || m.confirmApply {
			return m, nil
		}
		name := "j"
		if msg.Button == tea.MouseButtonWheelUp {
			name = "k"
		}
		switch m.page {
		case pageHelp:
			if name == "j" {
				m.helpScroll += 3
			} else {
				m.helpScroll -= 3
			}
			return m, nil
		case pageHome:
			if name == "j" {
				m.home.scrollBy(3)
			} else {
				m.home.scrollBy(-3)
			}
			return m, nil
		case pageConfigs:
			// Right-pane focus scrolls the content view; left-pane focus
			// moves the cursor, same as the keys.
			m.configs.handleKey(runeKey(name), m.drv)
			return m, nil
		case pageTree:
			m.groups.handleKey(runeKey(name), m.drv)
			return m, nil
		case pageSubs:
			m.subs.handleKey(runeKey(name), m.drv)
			return m, nil
		case pageNodes:
			m.nodes.handleKey(runeKey(name), m.drv)
			return m, nil
		}
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		return m.click(msg.X, msg.Y)
	}
	return m, nil
}

// click maps a cell-coordinate press onto the UI. Frame rows: 0 is the top
// border, 1 the status line, 2 the tabs, 3+ the body; in a pane page the
// body starts with the pane title (3) and rule (4), rows start at 5. Column
// 0 is the frame's left border.
func (m Model) click(x, y int) (tea.Model, tea.Cmd) {
	if m.anyModal() || m.confirmApply {
		return m, nil
	}
	cx := x - 1
	if y == 2 && cx >= 0 {
		return m.tabClick(cx)
	}
	if y < 5 || cx < 0 {
		return m, nil
	}
	row := y - 5
	switch m.page {
	case pageTree:
		if cx <= m.groups.leftW {
			m.groups.leftClick(row)
		} else {
			m.groups.rightClick(row)
		}
	case pageSubs:
		if cx <= m.subs.leftW {
			m.subs.leftClick(row)
		}
	case pageNodes:
		if cx <= m.nodes.leftW {
			m.nodes.leftClick(row)
		}
	case pageConfigs:
		if cx <= m.configs.leftW {
			m.configs.leftClick(row)
		}
	}
	return m, nil
}

// tabClick switches to the tab whose rendered span contains column cx. Tab
// spans come from the same joined labels tabsBar renders (styles only
// recolor, so the width math is shared).
func (m Model) tabClick(cx int) (tea.Model, tea.Cmd) {
	for i, t := range tabLabels {
		w := lipgloss.Width(t)
		if cx < w+2 { // TabStyle pads (0,1) on both sides
			m.page = i
			if i == pageHelp {
				m.helpScroll = 0
			}
			return m, nil
		}
		cx -= w + 2
	}
	return m, nil
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
		body = m.configs.View(m.status.Modified)
	case pageHelp:
		body = helpView(m.width-2, m.height-6, m.helpScroll)
	}
	// Hard clamp: a page must never push the chrome (status/tabs/help)
	// off screen — this was the original scrolling bug. The body is also
	// padded up to the available height so the frame always spans exactly
	// the terminal and the help line stays anchored to the last row.
	avail := m.height - 6
	if avail < 1 {
		avail = 1
	}
	lines := strings.Split(body, "\n")
	if m.confirmApply {
		// The global apply confirmation renders as a boxed block above the
		// page body; pushing a few page lines out is fine for a modal.
		box := ui.BoxLines(true, " 确认应用当前选中 config+dns+routing (run)?   y 确认 / n 取消")
		lines = append(append(box, ""), lines...)
	}
	if len(lines) > avail {
		lines = lines[:avail]
	}
	for len(lines) < avail {
		lines = append(lines, "")
	}
	cw := m.width - 2
	for i := range lines {
		if lines[i] != "" {
			lines[i] = ui.Truncate(lines[i], cw)
		}
	}

	var b strings.Builder
	b.WriteString(m.statusBar() + "\n")
	b.WriteString(m.tabsBar() + "\n")
	b.WriteString(strings.Join(lines, "\n") + "\n")
	b.WriteString(m.toastLine() + "\n")
	b.WriteString(m.helpLine())
	// The frame: everything inside a rounded border sized to the terminal,
	// so the TUI reads as one closed window instead of open-ended text.
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("238")).
		Width(cw).
		Height(m.height - 2).
		Render(b.String())
}

// smallView is the below-floor fallback: say what is wrong instead of
// rendering wrapped garbage. A WindowSizeMsg above the floor restores the
// normal layout with no extra state.
func smallView(w, h int) string {
	msg := fmt.Sprintf("终端太小 (%d×%d)\n\ndae-tui 至少需要 %d×%d，建议 80×24。\n放大窗口后自动恢复。",
		w, h, minTermW, minTermH)
	return lipgloss.Place(max0(w), max0(h), lipgloss.Center, lipgloss.Center, msg)
}

// shortEndpoint shrinks the endpoint to host:port — the scheme and path are
// constant noise on every status line.
func shortEndpoint(ep string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(ep, "https://"), "http://")
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	return s
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
	left := ui.TitleStyle.Render("dae-tui") + ui.HelpStyle.Render(" ("+shortEndpoint(m.cfg.Endpoint)+")") +
		"  " + run + ui.HelpStyle.Render(" dae "+m.status.Version) + mod
	right := ""
	if m.caps.TrafficStats {
		s := m.home.snap
		right = ui.HelpStyle.Render("↑" + ui.Rate(s.UpRate) + " ↓" + ui.Rate(s.DownRate))
	}
	gap := m.width - 2 - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return ui.Truncate(left+strings.Repeat(" ", gap)+right, m.width-2)
}

var tabLabels = []string{"1 首页", "2 群组", "3 订阅", "4 手动节点", "5 配置", "? 帮助"}

// spinnerFrames is the braille spinner shown in the tabs bar while a
// latency test runs.
var spinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

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
		bar += "  " + ui.TitleStyle.Render(string(spinnerFrames[m.spin%len(spinnerFrames)])) +
			ui.HelpStyle.Render(fmt.Sprintf(" 测速中 %d/%d", done, total))
	}
	// The progress suffix must never wrap the bar onto a second row — the
	// height math reserves exactly one line for the tabs.
	return ui.Truncate(bar, m.width-2)
}

// reenableMouse restores mouse reporting after a tea.ExecProcess round-trip
// ($EDITOR, the journal viewer). bubbletea's ReleaseTerminal disables mouse
// mode before handing the terminal to the child, but RestoreTerminal (v1.3)
// re-enables altscreen, bracketed paste and focus reporting — not the mouse
// — so clicks and wheel events silently stop arriving otherwise.
func reenableMouse() tea.Cmd {
	return tea.EnableMouseCellMotion
}

// anyTesting reports whether any page has a latency test in flight.
func (m Model) anyTesting() bool {
	return m.groups.testing || m.subs.testing || m.nodes.testing
}

// testProgress reports the in-flight latency tests as (returned, total)
// across every page — a test keeps running when the user switches tabs, so
// the indicator must not vanish with it.
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
	// One row, never two: a wrapped toast would steal the help line's row.
	return ui.Truncate(m.toast, m.width-2)
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
	case pageHelp:
		keys = "j/k 滚动  1-5 切换页面  q 退出"
	default:
		keys = "A 应用  1-5 切换页面  q 退出"
	}
	// Truncate instead of wrap: a second help row would push the chrome off
	// screen on narrow terminals (the body clamp reserves exactly one row).
	return ui.HelpStyle.Render(ui.Truncate(" "+keys, m.width-2))
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
