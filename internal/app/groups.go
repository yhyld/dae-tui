package app

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"dae-tui/internal/driver"
	"dae-tui/internal/i18n"
	"dae-tui/internal/keymap"
	"dae-tui/internal/ui"
)

type groupsPage struct {
	groups []driver.Group
	subs   []driver.Subscription

	refs map[string][]string

	gi    int
	focus int

	subOpen    map[string]bool
	directOpen bool

	rows []trow
	rc   int

	marked map[string]bool

	// markAnchor is the armed endpoint of a range select (ctrl+space): a
	// node ID, never a row index — the 3s latency poll reorders rows between
	// the two presses, so the closing press resolves both endpoints against
	// the row order it sees right then.
	markAnchor string

	pickMarked map[string]bool

	lat map[string]driver.Latency

	loading bool
	err     error

	testing   bool
	testIDs   []string
	testStart time.Time
	baseline  map[string]time.Time

	mode       int
	pickCursor int
	manual     []driver.Node
	candManual []driver.Node
	candSubs   []subNodes
	candBusy   bool
	pickErr    string
	input      textinput.Model

	// pinGroupID is the managed pinned group's ID (empty = feature unused);
	// mutations on that group are refused with a toast (pin.go's design).
	// pinTarget is the node the pin confirm overlay was armed for.
	// routingName/routingBody mirror the selected routing so f can count the
	// references it would rewrite before asking for confirmation.
	// switchFrom is the group that routing currently references (managed
	// pinned group while a pin is active); S switches it to the viewed group.
	pinGroupID  string
	pinTarget   driver.Node
	routingName string
	routingBody string
	switchFrom  string
	pinActive   bool

	nodeView

	caps driver.Caps

	leftW, rightW, height int
}

const (
	rowGroup = iota
	rowSub
	rowNode
	rowDirect
)

const (
	pickNone = iota
	pickSub
	pickNode
	pickDetach
	pickPolicy
	pickDeleteGroup
	inputCreate
	inputRename
	pickRemoveNode
	pickRemoveNodes
	pickPin
	pickSwitch
)

type trow struct {
	kind   int
	gi     int
	si     int
	node   driver.Node
	manual bool
	direct bool
}

func newGroupsPage(caps driver.Caps) groupsPage {
	ti := textinput.New()
	ti.CharLimit = 64
	ti.Width = 32
	return groupsPage{
		lat:        map[string]driver.Latency{},
		baseline:   map[string]time.Time{},
		subOpen:    map[string]bool{},
		marked:     map[string]bool{},
		pickMarked: map[string]bool{},
		refs:       map[string][]string{},
		input:      ti,
		nodeView:   newNodeView(),
		caps:       caps,
	}
}

var policyChoices = []struct{ name, label string }{
	{"min_moving_avg", "自动 · 最小移动平均延迟"},
	{"min_avg10", "自动 · 最小平均延迟"},
	{"min", "自动 · 最小最新延迟"},
	{"random", "随机"},
}

func (p *groupsPage) setSize(leftW, rightW, h int) {
	p.leftW, p.rightW, p.height = leftW, rightW, h
}

func (p *groupsPage) rebuild() {
	p.rows = p.rows[:0]
	if p.gi >= len(p.groups) {
		return
	}
	g := &p.groups[p.gi]
	filtering := p.nodeView.filter() != ""
	for si := range g.Subscriptions {
		nodes := p.nodeView.visible(g.Subscriptions[si].Nodes, p.lat)
		if filtering && len(nodes) == 0 {
			continue
		}
		p.rows = append(p.rows, trow{kind: rowSub, gi: p.gi, si: si})
		if p.subOpen[g.Subscriptions[si].SubscriptionID] || filtering {
			for _, n := range nodes {
				p.rows = append(p.rows, trow{kind: rowNode, gi: p.gi, si: si, node: n})
			}
		}
	}

	direct := p.nodeView.visible(g.Nodes, p.lat)
	if len(direct) > 0 {
		p.rows = append(p.rows, trow{kind: rowDirect, gi: p.gi})
		if p.directOpen || filtering {
			for _, n := range direct {
				p.rows = append(p.rows, trow{kind: rowNode, gi: p.gi, node: n,
					manual: n.SubscriptionID == "", direct: true})
			}
		}
	}
	if p.rc >= len(p.rows) {
		p.rc = max0(len(p.rows) - 1)
	}
}

func (p *groupsPage) collapseSections() {
	p.subOpen = map[string]bool{}
	p.directOpen = false
	p.rc = 0
	p.marked = map[string]bool{}
	p.markAnchor = ""
}

// markRange arms or closes a k9s-style range select on the cursor's node
// row: the first press anchors, the second marks every node row between the
// anchor and the cursor (inclusive, both endpoints resolved against the
// row order at the closing press — the press-time snapshot). An anchor that
// vanished with a refresh restarts the range at the cursor rather than
// silently marking the wrong span. Marks are set, never toggled: the result
// of a range must be predictable while single space-toggles remain
// available for the exceptions.
func (p *groupsPage) markRange() {
	r := p.cur()
	if r == nil || r.kind != rowNode {
		return
	}
	if p.markAnchor == "" {
		p.markAnchor = r.node.ID
		return
	}
	lo, hi := -1, -1
	for i := range p.rows {
		if p.rows[i].kind != rowNode {
			continue
		}
		switch p.rows[i].node.ID {
		case p.markAnchor:
			lo = i
		case r.node.ID:
			hi = i
		}
	}
	if lo < 0 || hi < 0 {
		p.markAnchor = r.node.ID
		return
	}
	if lo > hi {
		lo, hi = hi, lo
	}
	for i := lo; i <= hi; i++ {
		if p.rows[i].kind == rowNode {
			p.marked[p.rows[i].node.ID] = true
		}
	}
	p.markAnchor = ""
}

func (p *groupsPage) cur() *trow {
	if p.rc < 0 || p.rc >= len(p.rows) {
		return nil
	}
	return &p.rows[p.rc]
}

func (p *groupsPage) curGroup() *driver.Group {
	if p.gi < 0 || p.gi >= len(p.groups) {
		return nil
	}
	return &p.groups[p.gi]
}

func (p *groupsPage) handleGroups(groups []driver.Group, err error) {
	if err != nil {
		p.err = err
		return
	}
	p.err = nil

	p.pickErr = ""
	p.groups = groups
	p.loading = false
	if p.gi >= len(p.groups) {
		p.gi = max0(len(p.groups) - 1)
	}
	p.rebuild()
}

func (p *groupsPage) setSubs(subs []driver.Subscription, err error) {
	if err != nil {
		return
	}
	p.pickErr = ""
	p.subs = subs

	for gi := range p.groups {
		for si := range p.groups[gi].Subscriptions {
			gs := &p.groups[gi].Subscriptions[si]
			for _, s := range subs {
				if s.ID == gs.SubscriptionID {
					gs.Tag = s.Tag
				}
			}
		}
	}
	p.rebuild()
}

func (p *groupsPage) setReferences(routings []driver.ConfigItem) {
	refs := map[string][]string{}
	for _, r := range routings {
		for _, g := range r.References {
			refs[g] = append(refs[g], r.Name)
		}
	}
	p.refs = refs
	p.routingName, p.routingBody = "", ""
	for _, r := range routings {
		if r.Selected {
			p.routingName, p.routingBody = r.Name, r.Body
			break
		}
	}
}

// setPin is the root model's push of the re-derived pin state (rederivePin).
func (p *groupsPage) setPin(pin pinState) {
	p.pinGroupID = pin.groupID
	p.pinActive = pin.active
}

// pinGuarded reports whether the current group is the TUI-managed pinned
// group, whose membership/policy/existence all belong to the pin flow.
func (p *groupsPage) pinGuarded() bool {
	g := p.curGroup()
	return g != nil && p.pinGroupID != "" && g.ID == p.pinGroupID
}

// startPin arms the pin confirm overlay for the node row under the cursor.
func (p *groupsPage) startPin() tea.Cmd {
	if !p.caps.ConfigMgmt {
		return unsupportedCmd(i18n.T("固定节点"))
	}
	r := p.cur()
	g := p.curGroup()
	if r == nil || r.kind != rowNode || g == nil {
		return nil
	}
	if p.pinGuarded() || g.Name == pinGroupName {
		return pinGuardCmd(i18n.T("固定节点"))
	}
	if p.routingBody == "" {
		return opErrCmd(i18n.T("固定节点"), errors.New(i18n.T("路由方案未加载，按 r 刷新后重试")))
	}
	if countOutboundRefs(p.routingBody, g.Name) == 0 {
		return opErrCmd(i18n.T("固定节点"), fmt.Errorf(
			i18n.T("路由方案 %s 未引用组 %s，固定不会改变流量走向"), p.routingName, g.Name))
	}
	p.pinTarget = r.node
	p.mode = pickPin
	return nil
}

// startSwitch arms the switch-group confirm overlay for the group being
// viewed: the selected routing's references move from switchFrom to it.
func (p *groupsPage) startSwitch() tea.Cmd {
	if !p.caps.ConfigMgmt {
		return unsupportedCmd(i18n.T("切换代理组"))
	}
	g := p.curGroup()
	if g == nil {
		return nil
	}
	if p.routingBody == "" || p.switchFrom == "" {
		return opErrCmd(i18n.T("切换代理组"), errors.New(i18n.T("路由方案未加载，按 r 刷新后重试")))
	}
	if g.Name == p.switchFrom {
		return opErrCmd(i18n.T("切换代理组"), fmt.Errorf(i18n.T("%s 已是当前路由使用的组"), g.Name))
	}
	// The managed group is a valid target — S to it re-uses the fixed(0) node
	// that stayed behind after an unpin, no re-pinning by hand. With no member
	// left it is the empty-fixed-group reload blocker, so demand a fresh f.
	if p.pinGroupID != "" && g.ID == p.pinGroupID && len(g.Members()) == 0 {
		return opErrCmd(i18n.T("切换代理组"), errors.New(i18n.T("固定组当前没有节点，请在来源组对节点按 f 重新固定")))
	}
	p.mode = pickSwitch
	return nil
}

func (p *groupsPage) refNote(name string) string {
	profiles := p.refs[name]
	if len(profiles) == 0 {
		return ""
	}
	return i18n.T("该组被路由方案 ") + strings.Join(quoteAll(profiles), "、") + i18n.T(" 引用，改名/删除后这些规则将静默失效")
}

// refProfilesContain reports whether the selected routing is among the
// profiles a group's reference list names (refs maps group → profiles for
// ALL routings; the ● marker must only track the selected one).
func refProfilesContain(profiles []string, selected string) bool {
	if selected == "" {
		return false
	}
	for _, p := range profiles {
		if p == selected {
			return true
		}
	}
	return false
}

func quoteAll(items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = i18n.T("「") + s + i18n.T("」")
	}
	return out
}

func (p *groupsPage) handleManualNodes(nodes []driver.Node, err error) {
	p.manual = nodes
}

func (p *groupsPage) handleCandidates(msg attachCandidatesMsg) {
	p.candBusy = false
	if msg.Err != nil {
		p.pickErr = shortErr(msg.Err)
		return
	}
	p.candManual = msg.Manual
	p.candSubs = msg.Subs
	if p.mode == pickNode {
		p.pickCursor = 0
	}
}

func (p *groupsPage) handleLatencies(lats []driver.Latency, err error) {
	for _, l := range lats {
		p.lat[l.NodeID] = l
	}
	if !p.testing {
		return
	}
	// The timeout check comes before the error check: a backend that keeps
	// failing must still end the window, otherwise testing stays true and
	// the root model's tick re-fires latenciesCmd every second forever.
	if time.Since(p.testStart) > testWindow(len(p.testIDs)) {
		p.testing = false
		return
	}
	if err != nil {
		return
	}
	for _, id := range p.testIDs {
		l, ok := p.lat[id]
		if !ok || !l.TestedAt.After(p.baseline[id]) {
			return
		}
	}
	p.testing = false
}

func (p *groupsPage) testProgress() (done, total int) {
	if !p.testing {
		return 0, 0
	}
	for _, id := range p.testIDs {
		if l, ok := p.lat[id]; ok && l.TestedAt.After(p.baseline[id]) {
			done++
		}
	}
	return done, len(p.testIDs)
}

func (p *groupsPage) testIDsFor(onlySelected bool) []string {

	if ids := p.markedIDs(); len(ids) > 0 && !onlySelected {
		return ids
	}
	var nodes []driver.Node
	if p.focus == 0 || onlySelected {
		g := p.curGroup()
		if g == nil {
			return nil
		}
		if onlySelected {
			r := p.cur()
			if r == nil || r.kind != rowNode {
				return nil
			}
			return []string{r.node.ID}
		}

		nodes = g.Members()
	} else {
		r := p.cur()
		if r == nil {
			return nil
		}
		if r.kind == rowSub && r.si < len(p.groups[r.gi].Subscriptions) {
			nodes = p.groups[r.gi].Subscriptions[r.si].Nodes
		} else {
			nodes = p.groups[r.gi].Nodes
		}
	}
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if len(ids) >= 500 {
			break
		}
		ids = append(ids, n.ID)
	}
	return ids
}

func (p *groupsPage) visibleLatencyIDs() []string {
	if p.mode == pickNode {
		rows := p.visibleCandidates()
		ids := make([]string, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.node.ID)
		}
		return ids
	}
	if p.mode != pickNone {
		return nil
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(p.rows))
	for _, r := range p.rows {
		if r.kind == rowNode && !seen[r.node.ID] {
			seen[r.node.ID] = true
			ids = append(ids, r.node.ID)
		}
	}
	return ids
}

func (p *groupsPage) markedIDs() []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range p.rows {
		if r.kind == rowNode && p.marked[r.node.ID] && !seen[r.node.ID] {
			seen[r.node.ID] = true
			out = append(out, r.node.ID)
		}
	}
	return out
}

func (p *groupsPage) markedDirectNodes() []driver.Node {
	seen := map[string]bool{}
	var out []driver.Node
	for _, r := range p.rows {
		if r.kind == rowNode && r.direct && p.marked[r.node.ID] && !seen[r.node.ID] {
			seen[r.node.ID] = true
			out = append(out, r.node)
		}
	}
	return out
}

func (p *groupsPage) startTest(d driver.Driver, onlySelected bool) tea.Cmd {
	ids := p.testIDsFor(onlySelected)
	if len(ids) == 0 {
		return nil
	}
	p.testing = true
	p.testStart = time.Now()
	p.testIDs = ids
	for _, id := range ids {
		if l, ok := p.lat[id]; ok {
			p.baseline[id] = l.TestedAt
		} else {
			p.baseline[id] = time.Time{}
		}
	}
	return testLatencyCmd(d, ids)
}

func (p *groupsPage) pickableSubs(g *driver.Group) []driver.Subscription {
	attached := map[string]bool{}
	for _, s := range g.Subscriptions {
		attached[s.SubscriptionID] = true
	}
	out := make([]driver.Subscription, 0, len(p.subs))
	for _, s := range p.subs {
		if !attached[s.ID] {
			out = append(out, s)
		}
	}
	return out
}

func (p *groupsPage) selectGroupAt(next int) {
	p.gi = next
	p.rc = 0
	p.collapseSections()
	p.rebuild()
}

func (p groupsPage) overlay() *overlaySpec {
	// Placeholders re-translate here rather than living in the constructor:
	// the input outlives a settings-window language switch.
	p.input.Placeholder = i18n.T("名称")
	switch p.mode {
	case inputCreate:
		return &overlaySpec{lines: []string{
			ui.TitleStyle.Render(i18n.T(" 创建群组 (默认策略: 自动·最小移动平均)")),
			"",
			i18n.T(" 名称  ") + p.input.View(),
			"",
			ui.HelpStyle.Render(i18n.T(" Enter 确认  esc 取消")),
		}}
	case inputRename:
		if g := p.curGroup(); g != nil {
			lines := []string{
				ui.TitleStyle.Render(i18n.T(" 重命名群组")),
				"",
				i18n.T(" 名称  ") + p.input.View(),
			}
			if note := p.refNote(g.Name); note != "" {
				lines = append(lines, "", ui.ErrorStyle.Render(" ⚠ "+note))
			}
			return &overlaySpec{lines: append(lines, "",
				ui.HelpStyle.Render(i18n.T(" Enter 确认  esc 取消")))}
		}
	case pickPin:
		if g := p.curGroup(); g != nil {
			n := countOutboundRefs(p.routingBody, g.Name)
			lines := []string{
				ui.TitleStyle.Render(i18n.T(" 固定节点")),
				"",
				i18n.T(" 节点  ") + ui.SpaceAfterFlag(p.pinTarget.Name),
				i18n.T(" 来源组 ") + g.Name,
				"",
				ui.HelpStyle.Render(i18n.T(" 固定组 ") + pinGroupName + i18n.T(" 的成员将设为该节点 (fixed)，")),
				ui.HelpStyle.Render(i18n.T(" 路由方案 ") + p.routingName + i18n.T(" 中对 ") + g.Name +
					i18n.T(" 的 %d 处引用改指固定组", n)),
				ui.HelpStyle.Render(i18n.T(" 原有组的成员与订阅保持不动；A 重载后生效")),
				"",
				ui.OKStyle.Render(i18n.T(" y 确认")) + "    " + ui.ErrorStyle.Render(i18n.T("n / esc 取消")),
			}
			return &overlaySpec{lines: lines}
		}
	case pickSwitch:
		if g := p.curGroup(); g != nil {
			lines := []string{
				ui.TitleStyle.Render(i18n.T(" 切换代理组")),
				"",
				i18n.T(" 当前  ") + ui.SelectedStyle.Render(p.switchFrom) + " → " +
					ui.SelectedStyle.Render(g.Name),
				ui.HelpStyle.Render(i18n.T(" 路由方案 ") + p.routingName + i18n.T(" 中对 ") +
					p.switchFrom + i18n.T(" 的 %d 处引用将改指 ",
					countOutboundRefs(p.routingBody, p.switchFrom)) + g.Name),
				"",
				ui.HelpStyle.Render(i18n.T(" A 重载后生效")),
			}
			if p.pinGroupID != "" && g.ID == p.pinGroupID {
				lines = append(lines,
					ui.HelpStyle.Render(i18n.T(" 目标是固定节点专用组：复用其中已固定的节点")),
					ui.HelpStyle.Render(i18n.T(" 解除固定回到 ")+p.switchFrom+i18n.T(" 用首页 x")))
			} else if p.pinActive {
				lines = append(lines, ui.ErrorStyle.Render(i18n.T(" ⚠ 当前的固定节点将同时解除")))
			}
			return &overlaySpec{lines: append(lines, "",
				ui.OKStyle.Render(i18n.T(" y 确认"))+"    "+ui.ErrorStyle.Render(i18n.T("n / esc 取消")))}
		}
	}
	return nil
}

func (p *groupsPage) leftClick(row int) {
	rowsH := max0(p.height - 2)
	if row < 0 || row >= rowsH {
		return
	}
	start := 0
	if p.gi >= rowsH {
		start = p.gi - rowsH + 1
	}
	i := start + row
	if i < 0 || i >= len(p.groups) || i == p.gi {
		return
	}
	p.selectGroupAt(i)
}

func (p *groupsPage) rightClick(row int) {
	if p.mode != pickNone {
		return
	}
	topH, inner := stackedDetail(len(p.infoLines()), p.height)
	head := 0
	if p.nodeView.prompt() != "" {
		head = 1
	}
	d := row - topH - head
	start, rowsH := p.rowsWindow(inner)
	if d < 0 || d >= rowsH {
		return
	}
	i := start + d
	if i >= 0 && i < len(p.rows) {
		p.focus = 1
		p.rc = i
	}
}

func (p *groupsPage) handleKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {

	if p.mode == pickNone || p.mode == pickNode {
		if cmd, consumed := p.nodeView.handleKey(msg, keymap.Groups); consumed {
			if p.mode == pickNode {
				if n := len(p.visibleCandidates()); p.pickCursor >= n {
					p.pickCursor = max0(n - 1)
				}
			} else {
				p.rebuild()
			}
			return cmd
		}
	}
	if p.mode != pickNone {
		return p.pickerKey(msg, d)
	}

	// Remap layer for everything below: the filter input above stays raw,
	// the picker and the action switches dispatch through the translated
	// key.
	key := tk(keymap.Groups, msg.String())

	switch key {
	case "a":
		if !p.caps.SwitchNode {
			return unsupportedCmd(i18n.T("切换策略"))
		}
		if p.pinGuarded() {
			return pinGuardCmd(i18n.T("切换策略"))
		}
		if g := p.curGroup(); g != nil {
			return switchNodeCmd(d, g.ID, driver.Policy{Name: "min_moving_avg"})
		}
	case "t":
		if !p.caps.TestLatency {
			return unsupportedCmd(i18n.T("测速"))
		}
		return p.startTest(d, false)
	case "T":
		if !p.caps.TestLatency {
			return unsupportedCmd(i18n.T("测速"))
		}
		return p.startTest(d, true)
	case "U":
		// Clear every mark at once — the convenient undo for a range mark
		// (unmarking 50 nodes with space is not a workflow). Works from
		// either pane; the anchor is v/V's own business and stays.
		p.marked = map[string]bool{}
	}

	if p.focus == 0 {
		selectGroup := p.selectGroupAt
		switch key {
		case "j", "down":
			if p.gi < len(p.groups)-1 {
				selectGroup(p.gi + 1)
			}
		case "k", "up":
			if p.gi > 0 {
				selectGroup(p.gi - 1)
			}
		case "g":
			selectGroup(0)
		case "G":
			selectGroup(max0(len(p.groups) - 1))
		case "tab", "l", "right", "enter":

			if len(p.groups) > 0 {
				p.focus = 1
			}
		case "s":
			if !p.caps.Subscriptions {
				return unsupportedCmd(i18n.T("挂载订阅"))
			}
			if p.pinGuarded() {
				return pinGuardCmd(i18n.T("挂载订阅"))
			}
			g := p.curGroup()
			if g == nil {
				return nil
			}
			if len(p.pickableSubs(g)) == 0 {

				return func() tea.Msg {
					return opDoneMsg{Op: i18n.T("挂载订阅"),
						Err: errors.New(i18n.T("没有可添加的订阅（全部已挂载或无订阅）；先在 3 订阅页新增"))}
				}
			}
			p.mode = pickSub
			p.pickCursor = 0
			p.pickErr = ""
		case "n":
			if p.pinGuarded() {
				return pinGuardCmd(i18n.T("加节点"))
			}
			if g := p.curGroup(); g != nil {
				p.mode = pickNode
				p.pickCursor = 0
				p.pickErr = ""
				p.candManual = nil
				p.candSubs = nil
				p.candBusy = true

				p.pickMarked = map[string]bool{}
				return loadAttachCandidatesCmd(d)
			}
		case "c":
			p.mode = inputCreate
			p.input.SetValue("")
			p.input.Focus()
			return textinput.Blink
		case "R":
			if p.pinGuarded() {
				return pinGuardCmd(i18n.T("重命名群组"))
			}
			if g := p.curGroup(); g != nil {
				p.mode = inputRename
				p.input.SetValue(g.Name)
				p.input.Focus()
				return textinput.Blink
			}
		case "D":
			if p.pinGuarded() {
				return pinGuardCmd(i18n.T("删除群组"))
			}
			if p.curGroup() != nil {
				p.mode = pickDeleteGroup
			}
		case "p":
			if p.pinGuarded() {
				return pinGuardCmd(i18n.T("修改策略"))
			}
			if p.curGroup() != nil {
				p.mode = pickPolicy
				p.pickCursor = 0
			}
		case "S":
			return p.startSwitch()
		}
		return nil
	}

	switch key {
	case "j", "down":
		if p.rc < len(p.rows)-1 {
			p.rc++
		}
	case "k", "up":
		if p.rc > 0 {
			p.rc--
		}
	case "g":
		p.rc = 0
	case "G":
		p.rc = len(p.rows) - 1
	case "tab", "h", "left", "esc":

		p.focus = 0
		p.markAnchor = ""
	case "enter", "l", "right":
		r := p.cur()
		if r == nil {
			return nil
		}
		switch r.kind {
		case rowSub:
			id := p.groups[r.gi].Subscriptions[r.si].SubscriptionID
			p.subOpen[id] = !p.subOpen[id]
			p.rebuild()
		case rowDirect:
			p.directOpen = !p.directOpen
			p.rebuild()
		case rowNode:

		}
	case " ":

		if r := p.cur(); r != nil && r.kind == rowNode {
			if p.marked[r.node.ID] {
				delete(p.marked, r.node.ID)
			} else {
				p.marked[r.node.ID] = true
			}
		}
	case "v":
		// Range mark, vim visual-mode style: v anchors on the cursor's
		// node, move, v again marks everything between. A letter key (not
		// ctrl+space — that toggles the input method on most systems) and
		// remappable through the catalog like the rest of the page.
		p.markRange()
	case "V":
		// The abort half of the pair, same key capital-shifted like t/T:
		// drop the pending anchor, mark nothing, stay in the right pane.
		// esc keeps its single meaning (back to the left pane).
		p.markAnchor = ""
	case "f":
		return p.startPin()
	case "S":
		// The right pane shows one group; S switches the routing to that
		// group — same action as in the left pane, where gi selects it.
		return p.startSwitch()
	case "x":
		if p.pinGuarded() {
			return pinGuardCmd(i18n.T("移除节点"))
		}
		if len(p.marked) > 0 {
			if len(p.markedDirectNodes()) == 0 {

				return func() tea.Msg {
					return opDoneMsg{Op: i18n.T("移除节点"), Err: errors.New(i18n.T("已标记的节点均来自订阅挂载，只能随订阅一起移除（对订阅行按 x）"))}
				}
			}
			p.mode = pickRemoveNodes
			return nil
		}
		r := p.cur()
		if r == nil {
			return nil
		}
		switch r.kind {
		case rowSub:
			p.mode = pickDetach
		case rowNode:
			if r.direct {

				p.mode = pickRemoveNode
			}
		}
	}
	return nil
}

func (p *groupsPage) markedCandidateIDs(rows []candidateRow) []string {
	var out []string
	for _, r := range rows {
		if p.pickMarked[r.node.ID] {
			out = append(out, r.node.ID)
		}
	}
	return out
}

type candidateRow struct {
	node driver.Node
	tag  string
}

func (p *groupsPage) candidateRows() []candidateRow {
	out := make([]candidateRow, 0, len(p.candManual)+64)
	for _, n := range p.candManual {
		out = append(out, candidateRow{node: n})
	}
	for _, sub := range p.candSubs {
		for _, n := range sub.Nodes {
			out = append(out, candidateRow{node: n, tag: sub.Tag})
		}
	}
	return out
}

// anchorLabel renders the footer's armed-anchor state line: which node the
// range starts at, what closes it (the live v binding), what aborts it (the
// live V binding). Empty when no anchor.
func (p groupsPage) anchorLabel() string {
	if p.markAnchor == "" {
		return ""
	}
	for i := range p.rows {
		if r := &p.rows[i]; r.kind == rowNode && r.node.ID == p.markAnchor {
			return i18n.T("◆ 区间起点 ") + ui.SpaceAfterFlag(r.node.Name) + "  ·  " +
				K(keymap.Groups, "v") + i18n.T(" 收区间 · ") +
				K(keymap.Groups, "V") + i18n.T(" 取消锚定")
		}
	}
	return i18n.T("◆ 区间起点已失效 · ") + K(keymap.Groups, "V") + i18n.T(" 取消锚定")
}

func (p groupsPage) View() string {

	left := p.leftLines()
	leftTitle := i18n.T("路由组 (%d)", len(p.groups))
	if p.err != nil {
		left = []string{ui.ErrorStyle.Render(i18n.T("✗ 加载路由组失败: ") + shortErr(p.err))}
	} else if len(p.groups) == 0 {
		if p.loading {
			left = []string{ui.HelpStyle.Render(i18n.T("加载中…"))}
		} else {
			left = []string{ui.HelpStyle.Render(centerLine(i18n.T("无路由组（按 c 创建）"), p.leftW-4))}
		}
	}
	info := p.infoLines()
	topH, bottomInner := stackedDetail(len(info), p.height)
	body := p.bodyLines(bottomInner)
	infoTitle, bodyTitle := i18n.T("群组"), i18n.T("订阅与节点")
	if g := p.curGroup(); g != nil {
		infoTitle = g.Name
		bodyTitle += p.nodeView.sortTitle()
		if n := len(p.markedIDs()); n > 0 {
			// The clear hint rides the count — the place a user looks when
			// they want to undo a (possibly range) selection.
			bodyTitle += i18n.T(" · 已选 %d", n) + ui.HelpStyle.Render(
				i18n.T("（")+K(keymap.Groups, "U")+i18n.T(" 清除）"))
		}
	}

	rightFooter := i18n.T("Enter 开合分区 · space 标记 · ") + kb(keymap.Groups, "v", "区间") +
		" · " + kb(keymap.Groups, "x", "移除") +
		" · " + kb(keymap.Groups, "f", "固定") +
		" · / " + i18n.T("过滤") + " · " + kb(keymap.Groups, "o", "排序")
	if anchor := p.anchorLabel(); anchor != "" {
		// While a range anchor is armed the footer becomes the state line:
		// the pending half of the range is the thing the user most needs to
		// know (and that esc cancels it).
		rightFooter = anchor
	}
	switch p.mode {
	case pickSub:
		rightFooter = i18n.T("Enter 挂载 · j/k 移动 · esc 取消")
	case pickNode:
		rightFooter = i18n.T("Enter 添加 · space 标记 · esc 取消")
	case pickPolicy:
		rightFooter = i18n.T("Enter 确认 · j/k 移动 · esc 取消")
	case pickDetach, pickDeleteGroup, pickRemoveNode, pickRemoveNodes, pickPin, pickSwitch:
		rightFooter = i18n.T("y 确认 · n/esc 取消")
	}
	return strings.Join(ui.PaneRowColumn(
		ui.PaneSpec{Title: leftTitle, Footer: K(keymap.Groups, "s") + "/" + K(keymap.Groups, "n") + " " + i18n.T("挂订阅/节点") +
			" · " + K(keymap.Groups, "c") + "/" + K(keymap.Groups, "R") + "/" + K(keymap.Groups, "D") +
			"/" + K(keymap.Groups, "p") + " " + i18n.T("组"), Lines: left,
			Focused: p.focus == 0, W: p.leftW, H: p.height},
		ui.PaneSpec{Title: infoTitle, Lines: info, W: p.rightW, H: topH},
		ui.PaneSpec{Title: bodyTitle, Footer: rightFooter, Lines: body,
			Focused: p.focus == 1, W: p.rightW},
	), "\n")
}

func (p groupsPage) leftLines() []string {
	rowsH := max0(p.height - 2)
	start := 0
	if p.gi >= rowsH {
		start = p.gi - rowsH + 1
	}
	lines := make([]string, 0, rowsH)
	for i := start; i < len(p.groups) && i < start+rowsH; i++ {
		g := p.groups[i]
		cursor := " "
		if i == p.gi {
			cursor = ui.CursorStyle.Render("❯")
		}
		// Same ● the home group box uses: the group the selected routing
		// currently sends traffic through (builtin names skipped — a group
		// sharing one is shadowed by the dae builtin in the DSL anyway).
		mark := "  "
		if !isBuiltinOutbound(g.Name) && refProfilesContain(p.refs[g.Name], p.routingName) {
			mark = ui.OKStyle.Render("● ")
		}
		cur := ""
		if sel := g.SelectedNode(); sel != nil {
			cur = " → " + ui.SpaceAfterFlag(sel.Name)
		} else if g.Policy != "fixed" {
			cur = i18n.T(" · 自动")
		}
		line := cursor + " " + mark + ui.PadRight(g.Name, max0(p.leftW-22)) +
			ui.HelpStyle.Render(i18n.T("%3d节点", len(g.Members())))
		line += ui.Truncate(cur, max0(p.leftW-16))
		lines = append(lines, ui.HiRow(line, p.leftW-4, i == p.gi))
	}
	lines = ui.WithScrollbar(lines, p.leftW-4, len(p.groups), start, p.focus == 0)
	if p.pickErr != "" {
		lines = append(lines, "", ui.ErrorStyle.Render("✗ "+p.pickErr))
	}
	return lines
}

func (p groupsPage) infoLines() []string {
	g := p.curGroup()
	if g == nil {
		return []string{ui.HelpStyle.Render(i18n.T("（无组）"))}
	}
	lines := []string{
		detailLabel("策略") + policyLabel(g),
		detailLabel("成员") + i18n.T("%d（订阅挂载 %d 个，直接挂载 %d 个）",
			len(g.Members()), len(g.Subscriptions), len(g.Nodes)),
	}

	if profiles := p.refs[g.Name]; len(profiles) > 0 {
		row := detailLabel("引用") + ui.HelpStyle.Render(strings.Join(quoteAll(profiles), "、"))
		warn := ui.HelpStyle.Render(i18n.T(" · 改名/删除将静默失效"))
		if lipgloss.Width(row)+lipgloss.Width(warn) <= p.rightW-4 {
			row += warn
		}
		lines = append(lines, row)
	}
	if p.pinGroupID != "" && g.ID == p.pinGroupID {
		lines = append(lines, detailLabel("固定")+
			ui.HelpStyle.Render(i18n.T("TUI 固定节点专用组：换节点去来源组按 f，解除去首页按 x")))
	}
	return lines
}

func (p groupsPage) rowsWindow(inner int) (start, rowsH int) {
	head := 0
	if p.nodeView.prompt() != "" {
		head = 1
	}
	rowsH = max0(inner - head)
	start = 0
	if p.rc >= rowsH {
		start = p.rc - rowsH + 1
	}
	return start, rowsH
}

func (p groupsPage) bodyLines(inner int) []string {
	if p.mode == pickSub {
		return p.pickerLines(i18n.T("选择要添加到组的订阅"), p.subPickRows(), p.pickCursor, false)
	}
	if p.mode == pickNode {
		return p.candidateLines(inner)
	}
	if p.mode == pickDetach {
		if r := p.cur(); r != nil && r.gi < len(p.groups) && r.si < len(p.groups[r.gi].Subscriptions) {
			sub := p.groups[r.gi].Subscriptions[r.si]
			return ui.BoxLines(true, i18n.T("确认将订阅 \"")+sub.Tag+i18n.T("\" 从组 \"")+
				p.groups[r.gi].Name+i18n.T("\" 移除?  (y/n)"))
		}
	}
	if p.mode == pickDeleteGroup {
		if g := p.curGroup(); g != nil {
			lines := []string{i18n.T("确认删除群组 \"") + g.Name + "\"?  (y/n)"}
			if note := p.refNote(g.Name); note != "" {
				lines = append(lines, ui.ErrorStyle.Render(" ⚠ "+note))
			}
			return ui.BoxLines(true, lines...)
		}
	}
	if p.mode == pickRemoveNode {
		if r := p.cur(); r != nil && r.kind == rowNode {
			return ui.BoxLines(true, i18n.T("确认将节点 \"")+ui.SpaceAfterFlag(r.node.Name)+i18n.T("\" 从组中移除?  (y/n)"))
		}
	}
	if p.mode == pickRemoveNodes {
		nodes := p.markedDirectNodes()
		lines := []string{i18n.T("确认将 %d 个节点从组中移除?  (y/n)", len(nodes))}
		for i, n := range nodes {
			if i >= 5 {
				lines = append(lines, ui.HelpStyle.Render(i18n.T("  …等共 ")+strconv.Itoa(len(nodes))+i18n.T(" 个")))
				break
			}
			lines = append(lines, ui.HelpStyle.Render("  "+ui.SpaceAfterFlag(n.Name)))
		}
		return ui.BoxLines(true, lines...)
	}
	if p.mode == pickPolicy {
		lines := []string{ui.TitleStyle.Render(i18n.T(" 选择群组策略"))}
		for i, c := range policyChoices {
			c.label = i18n.T(c.label)
			mark, style := "  ", ui.HelpStyle
			if i == p.pickCursor {
				mark, style = "❯ ", ui.CursorStyle
			}
			lines = append(lines, ui.HiRow(style.Render(mark+c.label), p.rightW-4, i == p.pickCursor))
		}
		return lines
	}

	g := p.curGroup()
	if g == nil {
		return []string{ui.HelpStyle.Render(i18n.T("（无组）"))}
	}
	if len(p.rows) == 0 {
		if p.nodeView.filter() != "" {
			return []string{ui.HelpStyle.Render(p.nodeView.prompt()),
				ui.ErrorStyle.Render(i18n.T(" 没有匹配的节点（/ 重新编辑，框内 esc 清空）"))}
		}
		return []string{ui.HelpStyle.Render(i18n.T(" 组为空：按 s 挂订阅 / n 加手动节点"))}
	}

	start, rowsH := p.rowsWindow(inner)
	lines := make([]string, 0, rowsH)
	if prompt := p.nodeView.prompt(); prompt != "" {
		lines = append(lines, ui.HelpStyle.Render(prompt))
	}
	var window []string
	for i := start; i < len(p.rows) && i < start+rowsH; i++ {
		window = append(window, p.renderRow(g, i))
	}
	return append(lines, ui.WithScrollbar(window, p.rightW-4, len(p.rows), start, p.focus == 1)...)
}

func (p groupsPage) renderRow(g *driver.Group, i int) string {
	return ui.HiRow(p.renderRowBody(g, i), p.rightW-4, i == p.rc && p.focus == 1)
}

func (p groupsPage) renderRowBody(g *driver.Group, i int) string {
	r := &p.rows[i]

	cur := " "
	if i == p.rc && p.focus == 1 {
		cur = ui.CursorStyle.Render("❯")
	}
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(ui.DimText)

	if r.kind == rowSub {
		s := g.Subscriptions[r.si]
		label := i18n.T("▸ 订阅 ") + s.Tag
		if p.subOpen[s.SubscriptionID] {
			label = i18n.T("▾ 订阅 ") + s.Tag
		}
		if s.NameFilterRegex != "" {
			label += i18n.T(" (过滤: ") + s.NameFilterRegex + ")"
		}
		detail := ui.HelpStyle.Render(i18n.T("%d/%d节点  x 移除", s.MatchedCount, subTotal(p.subs, s.SubscriptionID)))
		return cur + " " + ui.PadRight(headerStyle.Render(label), max0(p.rightW-24)) + detail
	}
	if r.kind == rowDirect {
		arrow := "▸"
		if p.directOpen {
			arrow = "▾"
		}
		return cur + " " + headerStyle.Render(arrow+i18n.T(" 直接添加的节点 (")+strconv.Itoa(len(g.Nodes))+")")
	}

	n := r.node
	mark := "  "
	switch {
	case p.markAnchor == n.ID:
		// The armed range anchor outranks everything else in this column:
		// it is the one transient state the user must be able to see (esc
		// cancels it; yellow matches the app's other "pending" language).
		mark = ui.WarnStyle.Render("◆ ")
	case p.marked[n.ID]:
		mark = ui.OKStyle.Render("✓ ")
	default:
		if sel := g.SelectedNode(); sel != nil && sel.ID == n.ID {
			mark = ui.OKStyle.Render("● ")
		}
	}
	name := nodeName(n, p.nodeView.filter())
	if r.manual {
		name += ui.HelpStyle.Render(i18n.T(" (手动)"))
	}
	protoW := 8
	latW := latCellW(p.rightW - 4)
	nameW := max0(p.rightW - protoW - latW - 14)
	return "│  " + mark + cur + " " +
		ui.PadRight(name, nameW) +
		ui.HelpStyle.Render(ui.PadRight(n.Protocol, protoW)) +
		latencyCell(p.lat, n.ID, latW)
}

func (p groupsPage) subPickRows() [][2]string {
	out := [][2]string{}
	for _, s := range p.pickableSubs(p.curGroup()) {
		tag := s.Tag
		if tag == "" {
			tag = i18n.T("(未命名)")
		}
		out = append(out, [2]string{tag, i18n.T("%d节点", s.NodeCount)})
	}
	return out
}

func (p *groupsPage) visibleCandidates() []candidateRow {
	rows := p.candidateRows()
	nodes := make([]driver.Node, len(rows))
	for i, r := range rows {
		nodes[i] = r.node
	}
	byID := map[string]candidateRow{}
	for _, r := range rows {
		if _, dup := byID[r.node.ID]; !dup {
			byID[r.node.ID] = r
		}
	}
	visible := p.nodeView.visible(nodes, p.lat)
	out := make([]candidateRow, 0, len(visible))
	for _, n := range visible {
		out = append(out, byID[n.ID])
	}
	return out
}

func (p groupsPage) candidateLines(inner int) []string {
	title := i18n.T(" 选择要添加到组的节点") + ui.HelpStyle.Render(i18n.T("  (手动 + 各订阅)"))
	lines := []string{ui.TitleStyle.Render(title)}
	if p.candBusy {
		return append(lines, ui.HelpStyle.Render(i18n.T(" 拉取中…")))
	}
	rows := p.visibleCandidates()
	if len(p.candidateRows()) == 0 {
		return append(lines, ui.HelpStyle.Render(i18n.T(" （没有可添加的节点）")))
	}
	if prompt := p.nodeView.prompt(); prompt != "" {
		lines = append(lines, ui.HelpStyle.Render(prompt))
	}
	if len(rows) == 0 {
		return append(lines, ui.ErrorStyle.Render(i18n.T(" 没有匹配的节点（/ 重新编辑，框内 esc 清空）")))
	}
	lines[0] += p.nodeView.countTitle(len(rows), len(p.candidateRows())) +
		p.nodeView.sortTitle()
	if n := len(p.pickMarked); n > 0 {
		lines[0] += ui.OKStyle.Render(i18n.T("  已选 %d（Enter 全部添加）", n))
	}
	rowsH := max0(inner - len(lines))
	start := 0
	if p.pickCursor >= rowsH {
		start = p.pickCursor - rowsH + 1
	}
	var window []string
	for i := start; i < len(rows) && i < start+rowsH; i++ {
		r := rows[i]
		mark, style := "  ", ui.HelpStyle
		if i == p.pickCursor {
			mark, style = "❯ ", ui.CursorStyle
		}
		tick := ""
		if p.pickMarked[r.node.ID] {
			tick = ui.OKStyle.Render(" ✓")
		}
		src := ""
		if r.tag != "" {
			src = "·" + r.tag
		} else {
			src = i18n.T("·手动")
		}
		window = append(window, ui.HiRow(style.Render(mark+ui.PadRight(nodeName(r.node, p.nodeView.filter()), max0(p.rightW-34)))+tick+
			ui.HelpStyle.Render(ui.PadRight(src, 16)+r.node.Protocol), p.rightW-4, i == p.pickCursor))
	}
	return append(lines, ui.WithScrollbar(window, p.rightW-4, len(rows), start, true)...)
}

func (p groupsPage) pickerLines(title string, rows [][2]string, cursor int, busy bool) []string {
	lines := []string{ui.TitleStyle.Render(title)}
	if busy {
		lines[0] += ui.HelpStyle.Render(i18n.T("  拉取中…"))
		return lines
	}
	if len(rows) == 0 {
		lines = append(lines, ui.HelpStyle.Render(i18n.T(" （无手动节点：可在 daed 中导入，或从订阅页面管理）")))
	}
	for i, r := range rows {
		mark, style := "  ", ui.HelpStyle
		if i == cursor {
			mark, style = "❯ ", ui.CursorStyle
		}
		lines = append(lines, ui.HiRow(style.Render(mark+ui.PadRight(r[0], 28))+
			ui.HelpStyle.Render(r[1]), p.rightW-4, i == cursor))
	}
	return lines
}

func subTotal(subs []driver.Subscription, id string) int {
	for _, s := range subs {
		if s.ID == id {
			return s.NodeCount
		}
	}
	return 0
}

func policyLabel(g *driver.Group) string {
	if g == nil {
		return ""
	}
	switch g.Policy {
	case "fixed":
		if i := g.FixedIndex(); i >= 0 && i < len(g.Nodes) {
			return i18n.T("固定 → ") + ui.SpaceAfterFlag(g.Nodes[i].Name)
		}
		return i18n.T("固定")
	case "min_moving_avg":
		return i18n.T("自动 (最小移动平均延迟)")
	case "min_avg10":
		return i18n.T("自动 (最小平均延迟)")
	case "min":
		return i18n.T("自动 (最小最新延迟)")
	case "random":
		return i18n.T("随机")
	}
	return g.Policy
}
