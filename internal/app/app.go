package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"dae-tui/internal/config"
	"dae-tui/internal/driver"
	"dae-tui/internal/i18n"
	"dae-tui/internal/keymap"
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
	logs       logsViewer

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

	// Pending-reload bookkeeping: reloadNotes lists what this session
	// changed since the server last reported an unmodified running config
	// (the A confirm shows it); reloadDirty marks that the latest flip to
	// modified is ours, so no "external change" line is fabricated;
	// reloadLastNote guards against a status poll that was already in
	// flight when the newest note landed (its snapshot predates the
	// mutation). autoReloadAt is the pending auto-reload deadline (zero =
	// idle; see the tickMsg handler).
	reloadNotes    []string
	reloadDirty    bool
	reloadLastNote time.Time
	autoReloadAt   time.Time

	refreshing  bool
	refreshLeft int
	refreshAt   time.Time

	// connFails counts consecutive probe failures (traffic and status
	// polls); any success resets it. Trip level => the top bar badge.
	connFails int

	// pin is the pinned-node state; the persisted half mirrors cfg.Pin and
	// the runtime half is re-derived by rederivePin on every groups/
	// selections refresh (see pin.go).
	pin pinState
}

const (
	minTermW = 60
	minTermH = 12

	doubleClickWindow = 400 * time.Millisecond

	// connFailTrip is how many consecutive probe failures mark the backend
	// as disconnected. One dropped poll is noise; three in a row is a down
	// tunnel (≈3s on the 1s traffic heartbeat, ≈15s on the 5s status-only
	// fallback of a driver without TrafficStats).
	connFailTrip = 3
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
	loadKeys(cfgPath)

	m.configs.builtin = cfg.Editor == "builtin"
	// The E backup lands next to the app's own config, not in $PWD: launched
	// from anywhere, the destination must stay predictable.
	m.configs.exportDir = filepath.Join(filepath.Dir(cfgPath), "export")
	m.rederivePin()
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
			m.showErrToast(i18n.T("✗ 刷新超时（部分回复未返回）"))
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
			// Auto-reload: fire once the debounce deadline passes. Never
			// while the user stares at the A confirm (firing under their
			// finger re-arms instead); a fired attempt leaves the deadline
			// zeroed, so a failure never retries on its own.
			if !m.autoReloadAt.IsZero() {
				if m.confirmApply {
					m.autoReloadAt = time.Now().Add(autoReloadDelay)
				} else if time.Now().After(m.autoReloadAt) {
					m.autoReloadAt = time.Time{}
					cmds = append(cmds, runCmd(m.drv, false))
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
			m.applyStatus(msg.Status)
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
		// Same reset selectGroupAt does when switching groups: marks and
		// section state from the previous group must not leak into this one.
		m.groups.collapseSections()
		m.groups.rebuild()
		return m, nil

	case logsSpawnMsg:
		if msg.err != nil {
			m.logs.loading, m.logs.following = false, false
			m.logs.err = shortErr(msg.err)
			if !m.logs.open {
				m.showErrToast(i18n.T("✗ 打开日志: ") + shortErr(msg.err))
			}
			return m, nil
		}
		// An open/reload spawn implies follow intent; a resume spawn ('f')
		// inherits it, so a cancel that landed mid-flight still wins.
		if msg.fresh {
			m.logs.wantFollow = true
		}
		m.logs.open, m.logs.loading = true, false
		m.logs.stopped, m.logs.err = false, ""
		if msg.fresh {
			m.logs.lines, m.logs.scroll = nil, 0
		}
		// A double L races two spawns; the one that lands second replaces
		// the proc, so the loser's child would stream into a channel nobody
		// reads ever again. Kill on replace (idempotent — r already did).
		m.logs.kill()
		m.logs.proc = msg.proc
		if !m.logs.wantFollow {
			// The user paused while this spawn was in flight: kill the
			// process it produced and drop it — re-arming its read chain
			// would stream lines over the pause.
			m.logs.kill()
			m.logs.proc = nil
			return m, nil
		}
		m.logs.following = true
		return m, logsWaitCmd(msg.proc.lines)

	case logsLineMsg:

		// Stale chain (a killed or restarted follower): swallow and let it
		// die instead of re-arming.
		if !m.logs.open || m.logs.proc == nil || msg.lines != m.logs.proc.lines {
			return m, nil
		}
		win := logsWinBody(m.height - 6)
		atBottom := m.logs.scroll >= logsMaxScroll(len(m.logs.lines), win)
		m.logs.appendLine(msg.line)
		if m.logs.following && atBottom {
			m.logs.scroll = logsMaxScroll(len(m.logs.lines), win)
		}
		return m, logsWaitCmd(msg.lines)

	case logsStoppedMsg:

		// Only a stop from the live chain while we still believe we are
		// following marks the follower as exited-on-its-own; an echo from a
		// chain we killed ourselves (pause, reload) is not news.
		if m.logs.open && m.logs.following && m.logs.proc != nil && msg.lines == m.logs.proc.lines {
			m.logs.following, m.logs.loading, m.logs.stopped = false, false, true
		}
		return m, nil

	case statusMsg:
		if msg.Err == nil {
			m.applyStatus(msg.Status)
			m.connFails = 0
		} else {
			m.connFails++
		}
		return m, nil

	case reloadNoteMsg:
		// Notes only matter while the proxy runs: daed reports modified
		// false whenever it is stopped, so notes recorded while stopped
		// would never be confirmed — the next start applies everything
		// wholesale anyway.
		if m.status.Running {
			dup := false
			for _, n := range m.reloadNotes {
				if n == msg.Note {
					dup = true
					break
				}
			}
			if !dup {
				m.reloadNotes = append(m.reloadNotes, msg.Note)
			}
			m.reloadDirty, m.reloadLastNote = true, time.Now()
			if m.cfg.AutoReload {
				m.autoReloadAt = m.reloadLastNote.Add(autoReloadDelay)
			}
		}
		return m, nil

	case autoReloadToggledMsg:
		if msg.Err != nil {
			m.showErrToast(i18n.T("✗ 切换自动重载失败: ") + shortErr(msg.Err))
			return m, nil
		}
		m.settings.autoReload = msg.On
		if msg.On {
			m.showToast(i18n.T("已开启自动重载（每次重载会短暂断开现有连接）"))
		} else {
			m.showToast(i18n.T("已关闭自动重载"))
			m.autoReloadAt = time.Time{} // a pending fire dies with the switch
		}
		return m, nil

	case refreshDoneMsg:

		if m.refreshing {
			m.refreshLeft--
			if m.refreshLeft <= 0 {
				m.refreshing, m.refreshLeft = false, 0
				// r gives no other completion signal: without a toast the
				// indicator just vanishes and looks like a dead key.
				m.showToast(i18n.T("✓ 已刷新"))
			}
		}
		return m, nil

	case trafficMsg:
		m.home.apiTook = msg.Took
		if msg.Err == nil {
			m.home.update(msg.Snap)
			m.connFails = 0
		} else {
			m.home.err = msg.Err
			m.connFails++
		}
		return m, nil

	case pinRequestedMsg:
		return m, pinNodeCmd(m.drv, m.cfg, m.cfgPath, msg.NodeID, msg.NodeName, msg.FromGroup)

	case unpinRequestedMsg:
		return m, unpinNodeCmd(m.drv, m.cfg, m.cfgPath)

	case switchGroupRequestedMsg:
		return m, switchGroupCmd(m.drv, m.cfg, m.cfgPath, msg.From, msg.To, m.pinManagedName())

	case groupsMsg:
		m.groups.handleGroups(msg.Groups, msg.Err)
		m.home.handleGroups(msg.Groups, msg.Err)
		m.nodes.setGroups(msg.Groups)
		m.configs.setGroups(msg.Groups)
		m.rederivePin()
		return m, nil

	case manualNodesMsg:
		m.groups.handleManualNodes(msg.Nodes, msg.Err)
		m.nodes.handleNodes(msg.Nodes, msg.Err)
		return m, nil

	case importDoneMsg:
		m.nodes.handleImport(msg)

		if msg.Err == nil {
			m.showToast(m.nodes.importToast())
		} else if m.page != pageNodes {
			// The failure lands in the nodes page's import panel; from any
			// other page it would read as the busy spinner dying silently.
			m.showErrToast(i18n.T("✗ 导入后刷新节点列表: ") + shortErr(msg.Err))
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
		wasTesting := m.anyTesting()
		m.groups.handleLatencies(msg.Lats, msg.Err)
		m.home.handleLatencies(msg.Lats)
		m.subs.handleLatencies(msg.Lats, msg.Err)
		m.nodes.handleLatencies(msg.Lats, msg.Err)
		// Ring once when the aggregate spinner ("测速中 x/y") disappears:
		// in-page latency numbers are the in-band feedback, the bell is the
		// out-of-band channel for the user who tabbed away.
		if wasTesting && !m.anyTesting() {
			return m, testDoneNotifyCmd()
		}
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
		m.rederivePin()
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
		} else if msg.Err != nil {
			m.showErrToast(i18n.T("✗ 复制失败：") + msg.What + " · " + msg.Err.Error())
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

// rederivePin recomputes the pin's runtime flags from live groups and the
// selected routing's references, then pushes the ID/facts the pages need.
// The persisted pair (groupID, restore) is read straight off cfg — the pin
// cmds are the only writers, and their completion messages order the read —
// so an in-memory mirror can never drift from what was persisted.
// "Active" is derived from the routing, not from a stored flag: if the user
// switches routing schemes or hand-edits the DSL, the pin honestly reads as
// 失效 and home's x still cleans the state up.
func (m *Model) rederivePin() {
	p := m.pin
	p.groupID, p.restore = m.cfg.Pin.GroupID, m.cfg.Pin.Restore
	p.active, p.empty, p.nodeID, p.node, p.subID = false, false, "", "", ""
	var g *driver.Group
	if p.groupID != "" {
		for i := range m.groups.groups {
			if m.groups.groups[i].ID == p.groupID {
				g = &m.groups.groups[i]
				break
			}
		}
	}
	// The name the routing would reference; when the managed group is gone
	// (deleted out from under us) the last name we can assume is the
	// canonical one, and home's missing-group warning covers the rest.
	managedName := pinGroupName
	if g != nil {
		managedName = g.Name
	}
	if p.restore != "" {
		for _, r := range m.home.routingRefs {
			if r == managedName {
				p.active = true
				break
			}
		}
	}
	if g != nil {
		if sel := g.SelectedNode(); sel != nil {
			p.nodeID, p.node, p.subID = sel.ID, sel.Name, sel.SubscriptionID
		} else if len(g.Nodes) > 0 {
			p.nodeID, p.node, p.subID = g.Nodes[0].ID, g.Nodes[0].Name, g.Nodes[0].SubscriptionID
		}
		// The reload blocker is "a routing-referenced group with no member";
		// Members() (not just direct nodes) is the honest count here.
		p.empty = p.active && len(g.Members()) == 0
	}
	m.pin = p
	m.groups.pinGroupID = p.groupID
	m.groups.setPin(p)
	m.nodes.pinGroupID = p.groupID
	m.home.pin = p
	// The group a switch (S) re-points away from: the managed group while
	// the pin is live (that is what the routing literally references), the
	// rederive-mapped proxy group otherwise.
	switchFrom := m.home.proxyGroup()
	if p.restore != "" && p.active {
		switchFrom = managedName
	}
	m.home.switchFrom = switchFrom
	m.groups.switchFrom = switchFrom
}

// pinManagedName resolves the managed group's live name for switchGroupCmd's
// pin bookkeeping: "" while no pin is recorded or the group is gone — in both
// cases a switch to it is a plain rewrite with nothing to re-arm (unlike
// rederivePin, no canonical-name fallback: re-arming needs the real group).
func (m *Model) pinManagedName() string {
	if m.pin.groupID == "" {
		return ""
	}
	for _, g := range m.groups.groups {
		if g.ID == m.pin.groupID {
			return g.Name
		}
	}
	return ""
}

// reloadStaleGuard covers the status poll's in-flight window: a snapshot
// taken just before a mutation can arrive just after its note, so a status
// this close to the newest note is trusted for display but not for list
// bookkeeping (it cannot know about the change yet).
const reloadStaleGuard = 7 * time.Second

// autoReloadDelay is the trailing debounce for auto-reload: every note
// pushes the deadline, so a chain of mutations (pin's multi-step op, a
// burst of field edits) reloads exactly once at the end.
const autoReloadDelay = 2 * time.Second

// applyStatus records a fresh backend status and maintains the
// pending-reload list: a flip to modified with no local cause is an
// external change (daed web UI, or this session never saw the edit), and
// an unmodified status clears the list — the running config absorbed it.
func (m *Model) applyStatus(st driver.Status) {
	prev := m.status.Modified
	m.status = st
	if time.Since(m.reloadLastNote) < reloadStaleGuard {
		return
	}
	switch {
	case st.Modified && !prev && !m.reloadDirty:
		m.reloadNotes = append(m.reloadNotes, i18n.T("外部变更（daed Web UI 或本会话之外的操作）"))
	case !st.Modified && len(m.reloadNotes) > 0:
		m.reloadNotes = nil
	}
	m.reloadDirty = false
}

func (m *Model) enterMain() {
	m.phase = phaseMain
	m.page = pageHome
	m.settings.user = m.cfg.Username
	m.settings.autoReload = m.cfg.AutoReload
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
		switch tk(keymap.Global, msg.String()) {
		case "ctrl+c", "q", "esc":
			return m, tea.Quit
		case "r":
			m.fatal = nil
			m.phase = phaseBoot
			// No extra tickCmd here: the per-second chain re-arms in every
			// phase (see the tickMsg handler), so batching another one would
			// double every poll — and stack with each retry.
			return m, bootCmd(m.drv, cfgHasAuth(m.cfg))
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

	if m.logs.open {
		switch msg.String() {
		case "esc", "q":
			m.logs.open, m.logs.following, m.logs.wantFollow = false, false, false
			m.logs.kill()
			return m, nil
		case "j", "down":
			m.logs.scroll += 3
		case "k", "up":
			m.logs.scroll -= 3
		case "g":
			m.logs.scroll = 0
		case "G":
			m.logs.scroll = len(m.logs.lines)
		case "f":
			if m.logs.following || m.logs.loading {
				m.logs.following, m.logs.loading, m.logs.wantFollow = false, false, false
				m.logs.kill()
				return m, nil
			}
			m.logs.loading, m.logs.wantFollow = true, true
			return m, logsSpawnCmd(0, false)
		case "r":
			m.logs.loading = true
			m.logs.kill()
			return m, logsSpawnCmd(logsHistoryLines, true)
		}
		m.logs.scroll = clampLogsScroll(m.logs.scroll, len(m.logs.lines), logsWinBody(m.height-6))
		return m, nil
	}

	if !m.anyModal() {
		switch tk(keymap.Global, msg.String()) {
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
	if m.helpOpen || m.settings.open || m.logs.open {
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

		return m.home.confirmSwitch || m.home.confirmPreset >= 0 || m.home.confirmUnpin ||
			m.home.confirmGroupTo != ""
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
		if m.logs.open {
			if msg.Button == tea.MouseButtonWheelDown {
				m.logs.scroll += 3
			} else {
				m.logs.scroll -= 3
			}
			m.logs.scroll = clampLogsScroll(m.logs.scroll, len(m.logs.lines), logsWinBody(m.height-6))
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
	if y < 5 || y >= m.height-2 || cx < 0 {
		// y >= height-2 is the reserved toast line (the frame's bottom
		// border above it already opened help): mapping it into the page
		// would select whatever row happens to sit at that index.
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
		body = ui.Overlay(body, overlayBox(m.applyOverlay(), cw-1), cw-1, avail)
	}
	if m.helpOpen {
		body = ui.Overlay(body, helpOverlayBox(cw-1, avail, m.helpScroll), cw-1, avail)
	}
	if m.logs.open {
		body = ui.Overlay(body, logsOverlayBox(cw-1, avail, &m.logs), cw-1, avail)
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

// applyOverlay builds the A confirm. The pending-change list answers "what
// would this reload apply?" at the exact moment the user asks — the notes
// are the session's own mutation labels plus the external-change line; the
// whole section hides while the server reports nothing pending (a note
// racing the next status poll would otherwise show a list under a
// no-reload-needed confirm).
func (m Model) applyOverlay() *overlaySpec {
	lines := []string{ui.TitleStyle.Render(i18n.T(" 确认重载 (run)？"))}
	if m.status.Modified && len(m.reloadNotes) > 0 {
		lines = append(lines, ui.HelpStyle.Render(i18n.T(" 自上次重载以来的变更：")))
		const maxNotes = 8
		notes := m.reloadNotes
		if len(notes) > maxNotes {
			lines = append(lines, ui.HelpStyle.Render(
				i18n.T("  （较早的 %d 项省略）", len(notes)-maxNotes)))
			notes = notes[len(notes)-maxNotes:]
		}
		for _, n := range notes {
			lines = append(lines, " · "+n)
		}
		lines = append(lines, "")
	}
	lines = append(lines,
		i18n.T(" 重载会按当前状态重新生成 dae 配置：方案切换、"),
		i18n.T(" 订阅更新、群组改动都要重载后才生效。"),
		"",
		ui.OKStyle.Render(i18n.T(" y 重载"))+"    "+ui.ErrorStyle.Render(i18n.T("n / esc 取消")),
	)
	return &overlaySpec{destructive: true, lines: lines}
}

func (m Model) statusBar() string {
	run := ui.OKStyle.Render(i18n.T("● 运行中"))
	if !m.status.Running {
		run = ui.ErrorStyle.Render(i18n.T("○ 未运行"))
	}
	down := m.connFails >= connFailTrip
	if down {
		// The last known run state is stale while the link is down; the
		// honest badge is the link itself. The refresh key doubles as the
		// manual retry — its effective binding is what the badge names.
		run = ui.ErrorStyle.Render(i18n.T("⚠ 连接断开") + " · " +
			K(keymap.Global, "r") + " " + i18n.T("重试"))
	}
	mod := ""
	if m.status.Modified {
		// Yellow, never red: a pending reload is a notice, not an emergency
		// — the proxy keeps running on the old config, and the real
		// emergencies (stopped, link down) carry their own red badges.
		txt := "⚠ " + i18n.T("需重载") + " (" + K(keymap.Global, "A") + ")"
		if n := len(m.reloadNotes); n > 0 {
			txt += i18n.T(" · %d 项", n)
		}
		mod = " " + lipgloss.NewStyle().Foreground(ui.Yellow).Render(txt)
	}
	left := ui.TitleStyle.Render("dae-tui") + ui.HelpStyle.Render(" ("+shortEndpoint(m.cfg.Endpoint)+")") +
		"  " + run + ui.HelpStyle.Render(" dae "+m.status.Version) + mod
	right := ""
	if m.caps.TrafficStats && !down {
		// Rates frozen at the moment the link died would read as current.
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

// helpKeys composes the frame's key strip from the keymap, so it always
// names the keys that actually work under a remap.
func (m Model) helpKeys() (keys, hint string) {
	gA := kb(keymap.Global, "A", "重载")
	gR := kb(keymap.Global, "r", "刷新")
	switch m.page {
	case pageHome:
		keys = kb(keymap.Home, "L", "日志") + "  " + kb(keymap.Global, "P", "设置") +
			"  " + gA + "  " + gR
	case pageTree:
		keys = i18n.T("Tab 切栏") + "  " + kb(keymap.Groups, "a", "自动策略") +
			"  " + kb(keymap.Groups, "S", "切换组") +
			"  " + K(keymap.Groups, "t") + "/" + K(keymap.Groups, "T") + " " + i18n.T("测速") +
			"  " + gA + "  " + gR
	case pageSubs:
		keys = kb(keymap.Subs, "u", "更新") + "  " + kb(keymap.Subs, "e", "编辑") +
			"  " + kb(keymap.Subs, "n", "新增") + "  " + kb(keymap.Subs, "y", "复制链接") +
			"  " + gA + "  " + gR
	case pageNodes:
		keys = kb(keymap.Nodes, "a", "导入") + "  " + kb(keymap.Nodes, "e", "编辑") +
			"  " + kb(keymap.Nodes, "y", "复制") + "  " + K(keymap.Nodes, "t") + "/" +
			K(keymap.Nodes, "T") + " " + i18n.T("测速") + "  " + i18n.T("Tab 切栏") +
			"  " + gA + "  " + gR
	case pageConfigs:
		keys = kb(keymap.Configs, "v", "概览/原文") + "  " + kb(keymap.Configs, "y", "复制 DSL") +
			"  " + kb(keymap.Configs, "E", "导出备份") + "  " + K(keymap.Configs, "l") + "/Enter " +
			i18n.T("详情") + "  " + gA + "  " + gR
	default:
		keys = kb(keymap.Global, "A", "重载") + "  " + K(keymap.Global, "1") + "-" +
			K(keymap.Global, "5") + " " + i18n.T("切换页面") + "  " + kb(keymap.Global, "q", "退出")
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
