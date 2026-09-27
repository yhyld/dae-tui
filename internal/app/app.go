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

	// helpOpen turns the help into a floating window over the current page
	// (it no longer occupies a tab); helpScroll is its content offset.
	helpOpen   bool
	helpScroll int

	login loginForm

	width, height int
	toast         string
	toastAt       time.Time
	toastDur      time.Duration
	// Last body-row click, for double-click detection. Position, page and
	// time all have to repeat; modals, the tab strip and the help edge
	// never record (click returns before reaching the recorder), so a
	// double can't leak into a modal.
	lastClickPage int
	lastClickX    int
	lastClickY    int
	lastClickAt   time.Time
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

	// doubleClickWindow is how fast a second press on the same cell counts
	// as a double-click (the desktop convention is ~500ms; 400 feels
	// snappier and leaves less room for accidental doubles).
	doubleClickWindow = 400 * time.Millisecond
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
	// "builtin" swaps the DNS/routing $EDITOR round-trip for the in-app
	// floating editor (config key: editor).
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
	// An ErrNeedAuth arriving mid-session means the driver's silent re-auth
	// already failed (stored credentials rejected): every poll would keep
	// failing the same way, so route the user back to the login form
	// instead of an endless stream of error toasts.
	if m.phase == phaseMain {
		if err := msgAuthErr(msg); errors.Is(err, driver.ErrNeedAuth) {
			m.phase = phaseLogin
			m.login = newLoginForm(false)
			m.login.username.SetValue(m.cfg.Username)
			m.login.err = "登录已失效（凭据被拒绝），请重新登录"
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
		if m.anyTesting() || m.anyBusy() {
			return m, spinnerTickCmd()
		}
		m.spinning = false
		return m, nil

	case tickMsg:
		m.ticks = msg.n
		if !m.toastAt.IsZero() && time.Since(m.toastAt) > m.toastDur {
			m.toast, m.toastAt = "", time.Time{}
		}
		var cmds []tea.Cmd
		cmds = append(cmds, tickCmd(msg.n))
		if m.phase == phaseMain {
			if (m.anyTesting() || m.anyBusy()) && !m.spinning {
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
		if err := m.cfg.ClearSession(m.cfgPath); err != nil {
			m.showErrToast("✗ 清除本机凭据失败: " + shortErr(err))
		}
		if err := m.drv.Logout(context.Background()); err != nil {
			m.showErrToast("✗ 退出登录: " + shortErr(err))
		}
		m.home.user = ""
		m.phase = phaseLogin
		m.login = newLoginForm(false)
		return m, m.login.init()

	case gotoGroupMsg:
		// Home page Enter on a group row: open the groups page with that
		// group selected and the right column focused. The detail column is
		// always visible now, so there is nothing left to "expand".
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
		// A follow-up list failure arrives here too (the import itself
		// landed); its error goes to the pane, not the toast line, which
		// would otherwise summarize a batch that never completed.
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
			m.showErrToast("✗ " + msg.Op + ": " + shortErr(msg.Err))
		} else {
			m.showToast("✓ " + msg.Op)
		}
		// A failed mutation terminates here instead of in the refreshed
		// list message its success path returns; the page's busy flag (and
		// the 处理中 indicator riding on it) has no other clearing point.
		switch msg.Idle {
		case busySubs:
			m.subs.busy = false
		case busyNodes:
			m.nodes.busy = false
		}
		return m, nil

	case clipboardMsg:
		if msg.OK {
			m.showToast("✓ 已复制到剪贴板 (OSC 52)")
		} else {
			m.showErrToast("✗ 复制失败：当前输出不是终端")
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

// msgAuthErr returns the error carried by a driver-backed result message,
// for the central ErrNeedAuth check (nil when the message carries none).
// Boot- and login-phase messages are deliberately absent: their errors are
// auth-specific and handled by their own cases.
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
	// The app frame (rounded border, help keys riding its bottom edge)
	// costs 2 columns and 2 rows; the header box (tabs riding its top
	// edge) 3 more rows, the toast line 1. Body budget stays height-6.
	cw := max0(m.width - 2)
	ch := max0(m.height - 6)
	if cw < 1 {
		cw = 1
	}
	if ch < 1 {
		ch = 1
	}
	m.home.setSize(cw, ch)
	// leftW/rightW are the boxes' outer widths (borders included); 38
	// keeps the left content width at 34 cells now that each box spends
	// four columns on borders and padding instead of the panes' two.
	leftW := cw / 3
	if leftW > 38 {
		leftW = 38
	}
	if leftW < 22 {
		leftW = 22
	}
	rightW := cw - leftW - 3 // one-space gutter between the two boxes
	if rightW < 30 {
		rightW = 30
	}
	m.groups.setSize(leftW, rightW, ch)
	m.subs.setSize(leftW, rightW, ch)
	m.nodes.setSize(leftW, rightW, ch)
	m.configs.setSize(leftW, rightW, ch)
}

// showToast shows a transient success notice for the standard 4 seconds.
func (m *Model) showToast(s string) {
	m.toast, m.toastAt, m.toastDur = s, time.Now(), 4*time.Second
}

// showErrToast is the failure variant: error text (DSL line numbers, the
// kept temp-file path, per-link import failures) routinely needs longer
// than 4s to read, so it stays for 8.
func (m *Model) showErrToast(s string) {
	m.toast, m.toastAt, m.toastDur = s, time.Now(), 8*time.Second
}

// stackedDetail heights for a right column split into a small info box and
// a content box (groups/subs pages): the info box is content-sized (len+2
// border rows), clamped so a content box always survives below it, and the
// content box's inner height is what its scroll windows must use. ui.
// PaneRowColumn applies the same clamp when rendering.
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

	// phaseMain: ctrl+c quits unconditionally — it must survive every
	// modal, floating window and confirmation.
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
	// The help overlay owns every key while open, like a page modal.
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
			m.helpScroll = len(helpLines()) // clamped below
		}
		m.helpScroll = clampHelpScroll(m.helpScroll, helpWinBody(m.height-6))
		return m, nil
	}
	// The account window opens from any page (P is global), so its keys
	// are routed here before the page sees them.
	if m.home.acct != 0 {
		return m, m.home.acctKey(msg, m.drv)
	}
	// While a page-level modal/input is open, every keystroke (including
	// the digit page-switch hotkeys — names, cron expressions and links
	// are full of digits) must reach the modal, not the global router.
	if !m.anyModal() {
		switch msg.String() {
		case "q":
			return m, tea.Quit
		case "A":
			m.confirmApply = true
			return m, nil
		case "P":
			// The account window floats, so it opens from any page.
			m.home.acct = 1
			m.home.acctCur = 0
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

// anyModal reports whether the current page has a modal or text input
// open that should receive raw keystrokes.
func (m Model) anyModal() bool {
	if m.helpOpen || m.home.acct != 0 {
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
			// Right-pane focus scrolls the content view; left-pane focus
			// moves the cursor, same as the keys.
			return m, m.configs.handleKey(runeKey(name), m.drv)
		case pageTree:
			return m, m.groups.handleKey(runeKey(name), m.drv)
		case pageSubs:
			// The wheel replays j/k, and a j/k here also fires the newly
			// selected subscription's node fetch — dropping that cmd would
			// leave the pane stuck on 拉取节点中 forever.
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

// click maps a cell-coordinate press onto the UI. Frame rows: 0 is the top
// border, 1 the header box's tab edge, 2 the status line, 3 the header
// box's bottom border, 4 the page body's first line — the boxes' shared
// top border on box-layout pages. Pages receive row = y-5: the index of
// the clicked line counting from the first line inside the boxes (row 0 is
// the first content line, border rows live at row -1 and page height-2).
// That contract is easy to get wrong — configs.leftClick once shifted the
// whole column by one row, and groups.rightClick kept mapping clicks
// through the info box it never subtracted — so every page mapper must
// re-derive its own stack (stacked detail boxes, section boxes, prefix
// lines) from this row, treat border rows as no-ops, and stay in sync with
// the renderer it mirrors. Column 0 is the frame's left border, column 1
// the page-wide margin; the boxes start at column 2, the tabs at column 5.
func (m Model) click(x, y int) (tea.Model, tea.Cmd) {
	if m.anyModal() || m.confirmApply {
		return m, nil
	}
	cx := x - 2 // frame border + page-wide left margin
	if y == 1 && x >= 5 {
		return m.tabClick(x - 5)
	}
	if y == m.height-1 {
		// The frame's bottom edge — the help keys riding it — doubles as
		// the overlay's entry point: its trailing "? 帮助" names the key,
		// the click opens it (at the current page's section, like ?).
		m.helpOpen = true
		m.helpScroll = clampHelpScroll(helpSectionStart(m.page), helpWinBody(m.height-6))
		return m, nil
	}
	if y < 5 || cx < 0 {
		return m, nil
	}
	row := y - 5
	// Double-click detection: a second press on the same cell of the same
	// page within the window. Only body-row clicks get here, so modals can
	// never see the synthesized Enter.
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
			// Selecting a subscription also (re)fetches its node list: the
			// right column shows the nodes without a separate expand step.
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
			cmd = m.configs.rightClick(row)
		}
	}
	// A double-click acts as Enter — but only on the list pages whose Enter
	// is navigational (switch focus, expand a section). The configs left
	// column's Enter switches the live profile and the home preset's Enter
	// arms a whole-DSL replace, so there a double-click stays a selection:
	// activation stays on the keyboard wherever a keypress changes state.
	if dbl && (m.page == pageTree || m.page == pageSubs || m.page == pageNodes) {
		cmd = tea.Batch(cmd, func() tea.Msg { return tea.KeyMsg{Type: tea.KeyEnter} })
	}
	return m, cmd
}

// tabClick switches to the tab whose rendered span contains column cx
// (0-based into the tabs segment of the header box's top edge). Tab spans
// come from the same joined labels tabsBar renders (styles only recolor,
// so the width math is shared).
func (m Model) tabClick(cx int) (tea.Model, tea.Cmd) {
	for i, t := range tabLabels {
		w := lipgloss.Width(t)
		if cx < w+2 { // TabStyle pads (0,1) on both sides
			m.page = i
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
	// Hard clamp: a page must never push the chrome (header box, toast,
	// frame edges) off screen — this was the original scrolling bug. The
	// body is also padded up to the available height so the frame always
	// spans exactly the terminal and the help edge stays anchored to the
	// last row. Every page line gets the page-wide left margin (one cell
	// inside the frame), the same baseline the header box uses.
	avail := m.height - 6
	if avail < 1 {
		avail = 1
	}
	cw := m.width - 2
	// Floating windows stack over the page instead of displacing it: the
	// page's own form/dialog, then the global apply confirmation, then the
	// help — at most one page overlay is open at a time (anyModal gates
	// the keys), but A may be pressed over one. They center in the
	// margin-adjusted content width, matching the clamp below.
	if ov := m.pageOverlay(); ov != nil {
		body = ui.Overlay(body, overlayBox(ov, cw-1), cw-1, avail)
	}
	if m.confirmApply {
		body = ui.Overlay(body, overlayBox(&overlaySpec{destructive: true, lines: []string{
			ui.TitleStyle.Render(" 确认重载 (run)？"),
			" 重载会按当前状态重新生成 dae 配置：方案切换、",
			" 订阅更新、群组改动都要重载后才生效。",
			"",
			ui.OKStyle.Render(" y 重载") + "    " + ui.ErrorStyle.Render("n / esc 取消"),
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
	// The header box carries the tabs in its top edge (the active one as a
	// filled chip) and the status line inside — the same title-in-border
	// language as every other box. It gets the same page-wide left-margin
	// stamp as the page lines so its edges align with the boxes below; the
	// tab styles carry their own left padding, trimmed so the edge reads
	// "╭─ 1 首页".
	header := ui.TitledBoxRight(strings.TrimLeft(m.tabsBar(), " "), m.spinSuffix(), false, cw-2,
		[]string{m.statusBar()})
	for i := range header {
		header[i] = " " + ui.Truncate(header[i], cw-1)
	}
	frame := append(header, pageLines...)
	frame = append(frame, m.toastLine())
	// The frame: everything inside a rounded border sized to the terminal,
	// so the TUI reads as one closed window instead of open-ended text;
	// the help keys ride its bottom edge.
	keys, hint := m.helpKeys()
	return ui.AppFrame(m.width, m.height, keys, hint, frame)
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

// statusBar is the header box's content line: identity, runtime state and
// the live rates. The box pads and truncates it to its inner width; the
// rates stay pinned to the right edge while they fit.
func (m Model) statusBar() string {
	run := ui.OKStyle.Render("● 运行中")
	if !m.status.Running {
		run = ui.ErrorStyle.Render("○ 未运行")
	}
	mod := ""
	if m.status.Modified {
		mod = ui.ErrorStyle.Render(" ⚠ 需重载 (A)")
	}
	left := ui.TitleStyle.Render("dae-tui") + ui.HelpStyle.Render(" ("+shortEndpoint(m.cfg.Endpoint)+")") +
		"  " + run + ui.HelpStyle.Render(" dae "+m.status.Version) + mod
	right := ""
	if m.caps.TrafficStats {
		s := m.home.snap
		right = ui.HelpStyle.Render("↑" + ui.Rate(s.UpRate) + " ↓" + ui.Rate(s.DownRate))
	}
	inner := m.width - 8 // frame 2 + header box borders 2 + its padding 2 per side
	if right != "" {
		if gap := inner - lipgloss.Width(left) - lipgloss.Width(right); gap >= 2 {
			return left + strings.Repeat(" ", gap) + right
		}
	}
	return ui.Truncate(left, max0(inner))
}

var tabLabels = []string{"1 首页", "2 群组", "3 订阅", "4 手动节点", "5 配置"}

// spinnerFrames is the braille spinner shown in the header box's top edge
// while a latency test runs.
var spinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// tabsBar is the tab row riding the header box's top edge: the active page
// as a filled chip, the rest dim. It is embedded as the box's "title", so
// it must not be prefixed or truncated here — titledBox sizes it to the
// edge.
func (m Model) tabsBar() string {
	parts := make([]string, len(tabLabels))
	for i, t := range tabLabels {
		if m.page == i {
			parts[i] = ui.TabActive.Render(t)
		} else {
			parts[i] = ui.TabStyle.Render(t)
		}
	}
	return strings.Join(parts, "")
}

// spinSuffix is the indicator right-aligned in the header box's top edge:
// the same braille spinner covers in-flight mutations ("处理中") and latency
// tests ("测速中 x/y"), both cross-page facts, so both survive a page
// switch and share one chain.
func (m Model) spinSuffix() string {
	var parts []string
	if m.anyBusy() {
		parts = append(parts, "处理中")
	}
	if done, total := m.testProgress(); total > 0 {
		parts = append(parts, fmt.Sprintf("测速中 %d/%d", done, total))
	}
	if len(parts) == 0 {
		return ""
	}
	return ui.TitleStyle.Render(string(spinnerFrames[m.spin%len(spinnerFrames)])) +
		ui.HelpStyle.Render(" "+strings.Join(parts, " · "))
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

// anyBusy reports whether a page has a mutation in flight (subscription
// update/import/edit, node import/remove/edit). Like a running test it is a
// cross-page fact: the indicator lives in the chrome, not in a pane.
func (m Model) anyBusy() bool {
	return m.subs.busy || m.nodes.busy
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
	return " " + ui.Truncate(m.toast, m.width-3)
}

// helpKeys returns the two segments riding the frame's bottom edge: only
// page-wide and global keys live here — a key that works in just one box
// rides that box's own footer instead (see PaneSpec.Footer). AppFrame
// truncates the keys first so the call-to-action always survives.
func (m Model) helpKeys() (keys, hint string) {
	keys = "A 重载  1-5 切换页面  q 退出"
	switch m.page {
	case pageHome:
		keys = "L 日志  P 账户  A 重载  r 刷新"
	case pageTree:
		keys = "Tab 切栏  a 自动策略  t/T 测速  A 重载  r 刷新"
	case pageSubs:
		keys = "u 更新  e 编辑  n 新增  y 复制链接  A 重载  r 刷新"
	case pageNodes:
		keys = "a 导入  e 编辑  y 复制  t/T 测速  Tab 切栏  A 重载  r 刷新"
	case pageConfigs:
		keys = "v 概览/原文  y 复制 DSL  l/Enter 详情  A 重载  r 刷新"
	}
	// The overlay names its own keys while it is up; the trailing hint then
	// reads as "how to get out".
	hint = "? 帮助"
	if m.helpOpen {
		keys = "j/k 滚动  g/G 首尾"
		hint = "esc 关闭帮助"
	}
	return ui.HelpStyle.Render(keys), ui.HelpStyle.Render(hint)
}

// overlaySpec is a floating window's content. Decision dialogs and forms
// render over the page instead of displacing pane content — the rule of
// thumb: decisions and forms float, browsing and comparison stay in panes.
type overlaySpec struct {
	destructive bool
	lines       []string
}

// overlayBox renders a spec as a bordered box at most maxW cells wide.
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

// pageOverlay returns the active floating window, or nil. The account
// window is checked first: P opens it from any page.
func (m Model) pageOverlay() *overlaySpec {
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

// bootView and fatalView render in their own phase, outside the app frame,
// so they carry their own centered box — the same title-in-border language
// the main phases use.
func bootView(endpoint string, w, h int) string {
	box := ui.TitledBox("dae-tui", false, 44, []string{
		ui.TitleStyle.Render("正在连接 ") + ui.HelpStyle.Render(endpoint) + " …",
		"",
		ui.HelpStyle.Render("ctrl+c 退出"),
	})
	return lipgloss.Place(max0(w), max0(h), lipgloss.Center, lipgloss.Center, strings.Join(box, "\n"))
}

func fatalView(endpoint string, err error, w, h int) string {
	lines := []string{
		ui.ErrorStyle.Render("无法连接 daed") + ui.HelpStyle.Render(" ("+endpoint+")"),
		"",
		ui.ErrorStyle.Render(ui.Truncate(err.Error(), 58)),
		"",
		"检查: daed 是否在运行 (systemctl status daed)、",
		"endpoint 是否正确。远程实例推荐",
		"ssh -L 2023:127.0.0.1:2023 <host> 后用默认地址。",
		"",
		ui.HelpStyle.Render("r 重试  q 退出"),
	}
	bw := 64
	if w > 0 && bw > w-2 {
		bw = w - 2
	}
	box := ui.TitledBox("连接失败", true, bw, lines)
	return lipgloss.Place(max0(w), max0(h), lipgloss.Center, lipgloss.Center, strings.Join(box, "\n"))
}
