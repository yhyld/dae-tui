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
	"dae-tui/internal/ui"
)

// groupsPage: master-detail. Left pane lists groups; the right column is
// two stacked boxes — the selected group's info on top, its attached
// subscriptions with matched nodes and then manually added nodes below,
// with their own cursor and scroll window. Tab/l moves focus to the right
// column, h/esc returns to the group list.
type groupsPage struct {
	groups []driver.Group
	subs   []driver.Subscription
	// refs maps a group name to the routing profiles whose DSL references
	// it. Renaming or deleting such a group breaks those profiles silently
	// (daed accepts the change; the rules just stop matching), so the R/D
	// confirmations must name them.
	refs map[string][]string

	gi    int // left cursor: group index
	focus int // 0 left, 1 right (detail column)
	// per-section expansion in the right pane (default collapsed). Keyed by
	// subscription ID, not by position: a refresh can reorder the group's
	// subscriptions, and an index key would open someone else's section.
	subOpen    map[string]bool
	directOpen bool

	// right pane flattened detail rows for the selected group
	rows []trow
	rc   int // right cursor

	// marked holds the node IDs ticked with space in the detail pane, for
	// batch test / remove. Marks live across filtering and scrolling but are
	// dropped when the group changes or the detail collapses.
	marked map[string]bool
	// pickMarked is the same idea for the `n` add-node picker's candidates.
	// It is deliberately a separate map: sharing one made detail-pane ticks
	// (meant for removal) ride into the picker and get attached as
	// additions, and closing the picker with esc wiped the pending removals.
	pickMarked map[string]bool

	lat map[string]driver.Latency

	loading bool
	err     error

	// latency test polling state
	testing   bool
	testIDs   []string
	testStart time.Time
	baseline  map[string]time.Time

	// picker/modal state (rendered inside the right pane)
	mode       int
	pickCursor int
	manual     []driver.Node // manual nodes from loadManualNodesCmd
	candManual []driver.Node // `n` picker candidates
	candSubs   []subNodes
	candBusy   bool
	pickErr    string
	input      textinput.Model // inputCreate / inputRename

	nodeView // filter/sort for the detail pane's nodes and the `n` picker

	caps driver.Caps

	leftW, rightW, height int
}

const (
	rowGroup = iota
	rowSub
	rowNode
	rowDirect // section header: directly-added nodes
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
)

type trow struct {
	kind   int
	gi     int
	si     int
	node   driver.Node
	manual bool // node row in the direct section: subscription-less node
	direct bool // node row in the direct section (explicitly attached)
}

func newGroupsPage(caps driver.Caps) groupsPage {
	ti := textinput.New()
	ti.Placeholder = "名称"
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

// policyChoices are the selectable group policies (fixed is set per-node
// via Enter, so it is not offered here).
var policyChoices = []struct{ name, label string }{
	{"min_moving_avg", "自动 · 最小移动平均延迟"},
	{"min_avg10", "自动 · 最小平均延迟"},
	{"min", "自动 · 最小最新延迟"},
	{"random", "随机"},
}

func (p *groupsPage) setSize(leftW, rightW, h int) {
	p.leftW, p.rightW, p.height = leftW, rightW, h
}

// rebuild regenerates the right-pane rows for the selected group. The
// filter and sort are applied per section (visible() does both), so the
// section structure survives a latency sort — only the order inside each
// section changes. While a node filter is active, sections behave as
// expanded and drop out entirely when nothing in them matches, so the filter
// result is never hidden behind a collapsed section.
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
	// Group.nodes is exactly the set of directly-attached nodes (manual
	// imports plus nodes picked out of subscriptions).
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

// collapseSections resets per-section expansion (a group change is what
// calls it: leaving the detail column keeps the sections as they are).
func (p *groupsPage) collapseSections() {
	p.subOpen = map[string]bool{}
	p.directOpen = false
	p.rc = 0
	p.marked = map[string]bool{}
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
	// A stale picker error outlives whatever produced it (e.g. a failed
	// candidates load); a fresh group list retires it.
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
	// A subscription's tag is displayed inside groups too; patch it in place
	// so a rename does not leave the group pane showing the old tag until the
	// next full refresh.
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

// setReferences records which routing profiles reference which group names,
// from the backend's referenceGroups field.
func (p *groupsPage) setReferences(routings []driver.ConfigItem) {
	refs := map[string][]string{}
	for _, r := range routings {
		for _, g := range r.References {
			refs[g] = append(refs[g], r.Name)
		}
	}
	p.refs = refs
}

// refNote names the routing profiles that reference a group, or "" when none
// do. Renaming or deleting such a group breaks those profiles silently
// (daed accepts the change; the rules just stop matching).
func (p *groupsPage) refNote(name string) string {
	profiles := p.refs[name]
	if len(profiles) == 0 {
		return ""
	}
	return "该组被路由方案 " + strings.Join(quoteAll(profiles), "、") + " 引用，改名/删除后这些规则将静默失效"
}

func quoteAll(items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = "「" + s + "」"
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
	if err != nil {
		return
	}
	for _, l := range lats {
		p.lat[l.NodeID] = l
	}
	if !p.testing {
		return
	}
	if time.Since(p.testStart) > testWindow(len(p.testIDs)) {
		p.testing = false
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

// testProgress reports how many probed nodes have reported back, so the tab
// bar can show real progress instead of a spinner that lies.
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
	// Ticks win over the section/group semantics: the user named the nodes.
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
		// "Whole group" means every member, including the nodes contributed
		// by attached subscriptions: Group.Nodes holds only the directly
		// attached ones in daed v2, while the left pane advertises
		// len(Members()) as the group's node count.
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

// visibleLatencyIDs lists the nodes whose latency the detail pane renders:
// the node rows of the open sections (the filtered set while a filter is
// active), or the picker's candidates while the add-node picker is open —
// its sort-by-latency order reads the same table. The other modals show no
// latency, so they poll nothing.
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

// markedIDs lists the ticked nodes in row order, so batch mutations probe
// and report them deterministically. A node attached both directly and
// through a subscription occupies two rows; it is listed once.
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

// markedDirectNodes lists the ticked nodes that were attached directly
// (manually imported or picked out of a subscription) — the only ones a
// group can drop without detaching a whole subscription.
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

// pickableSubs returns subscriptions not yet attached to the given group.
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

// selectGroupAt moves the left-pane cursor. The detail rows follow the
// selected group; without the rebuild the right pane would render the
// previous group's nodes whenever a filter keeps it visible across a group
// change.
func (p *groupsPage) selectGroupAt(next int) {
	p.gi = next
	p.rc = 0
	p.collapseSections()
	p.rebuild()
}

// overlay returns the page's floating window: the create/rename input
// forms. Pickers and confirmations stay in the right pane next to what they
// act on.
func (p groupsPage) overlay() *overlaySpec {
	switch p.mode {
	case inputCreate:
		return &overlaySpec{lines: []string{
			ui.TitleStyle.Render(" 创建群组 (默认策略: 自动·最小移动平均)"),
			"",
			" 名称  " + p.input.View(),
			"",
			ui.HelpStyle.Render(" Enter 确认  esc 取消"),
		}}
	case inputRename:
		if g := p.curGroup(); g != nil {
			lines := []string{
				ui.TitleStyle.Render(" 重命名群组"),
				"",
				" 名称  " + p.input.View(),
			}
			if note := p.refNote(g.Name); note != "" {
				lines = append(lines, "", ui.ErrorStyle.Render(" ⚠ "+note))
			}
			return &overlaySpec{lines: append(lines, "",
				ui.HelpStyle.Render(" Enter 确认  esc 取消"))}
		}
	}
	return nil
}

// leftClick selects the row-th displayed group, mirroring leftLines' window
// math so the click lands on the row the user saw. Border rows hold no
// groups: a click on them must not fall through to the hidden row just
// outside the window.
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

// rightClick puts the right-pane cursor on the clicked detail row. row
// counts the column's content rows from the info box's first line (the
// column's top border row is row -1), so everything the stacked layout
// renders above the data rows — the info box including its borders (topH),
// the bottom box's top border and the filter prompt — must be subtracted
// before mapping through the same window bodyLines renders with. The info
// box is display-only: clicks there do nothing.
func (p *groupsPage) rightClick(row int) {
	if p.mode != pickNone {
		return
	}
	topH, inner := stackedDetail(len(p.infoLines()), p.height)
	head := 0
	if p.nodeView.prompt() != "" {
		head = 1
	}
	d := row - topH - head // data row within the bottom box's window
	start, rowsH := p.rowsWindow(inner)
	if d < 0 || d >= rowsH {
		return // info box, box edges, prompt line or dead space below
	}
	i := start + d
	if i >= 0 && i < len(p.rows) {
		p.focus = 1
		p.rc = i
	}
}

func (p *groupsPage) handleKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	// The filter box applies to node lists, which exist both in the detail
	// pane and in the add-node picker; it swallows every key while open.
	if p.mode == pickNone || p.mode == pickNode {
		if cmd, consumed := p.nodeView.handleKey(msg); consumed {
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

	// Global to this page.
	switch msg.String() {
	case "a":
		if !p.caps.SwitchNode {
			return unsupportedCmd("切换策略")
		}
		if g := p.curGroup(); g != nil {
			return switchNodeCmd(d, g.ID, driver.Policy{Name: "min_moving_avg"})
		}
	case "t":
		if !p.caps.TestLatency {
			return unsupportedCmd("测速")
		}
		return p.startTest(d, false)
	case "T":
		if !p.caps.TestLatency {
			return unsupportedCmd("测速")
		}
		return p.startTest(d, true)
	}

	if p.focus == 0 {
		selectGroup := p.selectGroupAt
		switch msg.String() {
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
			// The detail column is always rendered; this just moves focus.
			if len(p.groups) > 0 {
				p.focus = 1
			}
		case "s":
			if !p.caps.Subscriptions {
				return unsupportedCmd("挂载订阅")
			}
			g := p.curGroup()
			if g == nil {
				return nil
			}
			if len(p.pickableSubs(g)) == 0 {
				// Toast, not a persistent pane line: there is nothing to
				// confirm here, and a stuck red note that only a restart
				// clears reads like a broken state.
				return func() tea.Msg {
					return opDoneMsg{Op: "挂载订阅",
						Err: errors.New("没有可添加的订阅（全部已挂载或无订阅）；先在 3 订阅页新增")}
				}
			}
			p.mode = pickSub
			p.pickCursor = 0
			p.pickErr = ""
		case "n":
			if g := p.curGroup(); g != nil {
				p.mode = pickNode
				p.pickCursor = 0
				p.pickErr = ""
				p.candManual = nil
				p.candSubs = nil
				p.candBusy = true
				// The picker's ticks are its own: opening it must neither
				// inherit the detail pane's pending removals nor disturb
				// them (esc used to wipe both).
				p.pickMarked = map[string]bool{}
				return loadAttachCandidatesCmd(d)
			}
		case "c": // create group
			p.mode = inputCreate
			p.input.SetValue("")
			p.input.Focus()
			return textinput.Blink
		case "R": // rename group
			if g := p.curGroup(); g != nil {
				p.mode = inputRename
				p.input.SetValue(g.Name)
				p.input.Focus()
				return textinput.Blink
			}
		case "D": // delete group (confirm)
			if p.curGroup() != nil {
				p.mode = pickDeleteGroup
			}
		case "p": // change policy
			if p.curGroup() != nil {
				p.mode = pickPolicy
				p.pickCursor = 0
			}
		}
		return nil
	}

	// focus == 1: right pane navigation. Sections are collapsed by
	// default; Enter/l toggles the section under the cursor.
	switch msg.String() {
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
		// Leaving the detail keeps the sections as they are: the column
		// stays visible, and collapsing it under the user's eyes would read
		// as data loss. A group change is what resets sections (and marks).
		p.focus = 0
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
			// Pinning a node was removed: daed v2 fixed groups allow
			// exactly one member, so the old flow silently rebuilt the
			// whole group (detaching every subscription). Existing fixed
			// groups are still shown read-only; a/p switch them back to an
			// automatic policy.
		}
	case " ":
		// Space ticks a node for batch test / remove / attach. bubbletea
		// reports the space key as " ".
		if r := p.cur(); r != nil && r.kind == rowNode {
			if p.marked[r.node.ID] {
				delete(p.marked, r.node.ID)
			} else {
				p.marked[r.node.ID] = true
			}
		}
	case "x":
		if len(p.marked) > 0 {
			if len(p.markedDirectNodes()) == 0 {
				// Subscription-contributed nodes leave with their whole
				// subscription; say so instead of confirming a no-op.
				return func() tea.Msg {
					return opDoneMsg{Op: "移除节点", Err: errors.New("已标记的节点均来自订阅挂载，只能随订阅一起移除（对订阅行按 x）")}
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
				// only explicitly-attached nodes can be removed from a group
				p.mode = pickRemoveNode
			}
		}
	}
	return nil
}

// pickerKey routes the current picker/modal's keys; the per-mode handlers
// live in groups_picker.go.

// markedCandidateIDs lists the ticked picker candidates in display order.
func (p *groupsPage) markedCandidateIDs(rows []candidateRow) []string {
	var out []string
	for _, r := range rows {
		if p.pickMarked[r.node.ID] {
			out = append(out, r.node.ID)
		}
	}
	return out
}

// candidateRow is one selectable row in the `n` picker.
type candidateRow struct {
	node driver.Node
	tag  string // non-empty for subscription-sourced nodes
}

// candidateRows flattens manual nodes plus every subscription's nodes.
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

func (p groupsPage) View() string {
	// The page is a master/detail row whose detail side is two stacked
	// boxes (info on top, sections below), built by PaneRowColumn — the
	// same grid language as the home page's zones. Load failures and the
	// empty state stay inside the left box so the layout never collapses to
	// a bare line.
	left := p.leftLines()
	leftTitle := fmt.Sprintf("路由组 (%d)", len(p.groups))
	if p.err != nil {
		left = []string{ui.ErrorStyle.Render("✗ 加载路由组失败: " + shortErr(p.err))}
	} else if len(p.groups) == 0 {
		if p.loading {
			left = []string{ui.HelpStyle.Render("加载中…")}
		} else {
			left = []string{ui.HelpStyle.Render("无路由组（按 c 创建）")}
		}
	}
	info := p.infoLines()
	topH, bottomInner := stackedDetail(len(info), p.height)
	body := p.bodyLines(bottomInner)
	infoTitle, bodyTitle := "群组", "订阅与节点"
	if g := p.curGroup(); g != nil {
		infoTitle = g.Name
		bodyTitle += p.nodeView.sortTitle()
		if n := len(p.markedIDs()); n > 0 {
			bodyTitle += fmt.Sprintf(" · 已选 %d", n)
		}
	}
	// Key hints ride the edge of the box they belong to: the left box lists
	// group management, the bottom box lists in-list actions (and the active
	// picker's keys while one is open); page-wide keys stay on the frame.
	rightFooter := "Enter 开合分区 · space 标记 · x 移除 · / 过滤 · o 排序"
	switch p.mode {
	case pickSub:
		rightFooter = "Enter 挂载 · j/k 移动 · esc 取消"
	case pickNode:
		rightFooter = "Enter 添加 · space 标记 · esc 取消"
	case pickPolicy:
		rightFooter = "Enter 确认 · j/k 移动 · esc 取消"
	case pickDetach, pickDeleteGroup, pickRemoveNode, pickRemoveNodes:
		rightFooter = "y 确认 · n/esc 取消"
	}
	return strings.Join(ui.PaneRowColumn(
		ui.PaneSpec{Title: leftTitle, Footer: "s/n 挂订阅/节点 · c/R/D/p 组", Lines: left,
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
		cur := ""
		if sel := g.SelectedNode(); sel != nil {
			cur = " → " + ui.SpaceAfterFlag(sel.Name)
		} else if g.Policy != "fixed" {
			cur = " · 自动"
		}
		line := cursor + " " + ui.PadRight(g.Name, max0(p.leftW-20)) +
			ui.HelpStyle.Render(fmt.Sprintf("%3d节点", len(g.Members())))
		line += ui.Truncate(cur, max0(p.leftW-14))
		lines = append(lines, ui.HiRow(line, p.leftW-4, i == p.gi))
	}
	lines = ui.WithScrollbar(lines, p.leftW-4, len(p.groups), start, p.focus == 0)
	if p.pickErr != "" {
		lines = append(lines, "", ui.ErrorStyle.Render("✗ "+p.pickErr))
	}
	return lines
}

// infoLines is the right column's top box: the selected group's identity
// card — policy, membership, and which routing profiles reference it (those
// break silently when the group is renamed or deleted).
func (p groupsPage) infoLines() []string {
	g := p.curGroup()
	if g == nil {
		return []string{ui.HelpStyle.Render("（无组）")}
	}
	lines := []string{
		ui.SelectedStyle.Render("策略  ") + policyLabel(g),
		ui.SelectedStyle.Render("成员  ") + fmt.Sprintf("%d（订阅挂载 %d 个，直接挂载 %d 个）",
			len(g.Members()), len(g.Subscriptions), len(g.Nodes)),
	}
	if note := p.refNote(g.Name); note != "" {
		lines = append(lines, ui.SelectedStyle.Render("引用  ")+ui.HelpStyle.Render(note))
	}
	return lines
}

// rowsWindow returns the scroll window bodyLines renders the detail rows
// with, so rightClick can map a displayed row back to its index.
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

// bodyLines is the right column's bottom box: the group's subscription and
// direct-node sections (or the active picker/confirmation in their place).
// inner is the box's content height; the row list windows itself to it.
func (p groupsPage) bodyLines(inner int) []string {
	if p.mode == pickSub {
		return p.pickerLines("选择要添加到组的订阅", p.subPickRows(), p.pickCursor, false)
	}
	if p.mode == pickNode {
		return p.candidateLines(inner)
	}
	if p.mode == pickDetach {
		if r := p.cur(); r != nil && r.gi < len(p.groups) && r.si < len(p.groups[r.gi].Subscriptions) {
			sub := p.groups[r.gi].Subscriptions[r.si]
			return ui.BoxLines(true, "确认将订阅 \""+sub.Tag+"\" 从组 \""+
				p.groups[r.gi].Name+"\" 移除?  (y/n)")
		}
	}
	if p.mode == pickDeleteGroup {
		if g := p.curGroup(); g != nil {
			lines := []string{"确认删除群组 \"" + g.Name + "\"?  (y/n)"}
			if note := p.refNote(g.Name); note != "" {
				lines = append(lines, ui.ErrorStyle.Render(" ⚠ "+note))
			}
			return ui.BoxLines(true, lines...)
		}
	}
	if p.mode == pickRemoveNode {
		if r := p.cur(); r != nil && r.kind == rowNode {
			return ui.BoxLines(true, "确认将节点 \""+ui.SpaceAfterFlag(r.node.Name)+"\" 从组中移除?  (y/n)")
		}
	}
	if p.mode == pickRemoveNodes {
		nodes := p.markedDirectNodes()
		lines := []string{fmt.Sprintf("确认将 %d 个节点从组中移除?  (y/n)", len(nodes))}
		for i, n := range nodes {
			if i >= 5 {
				lines = append(lines, ui.HelpStyle.Render("  …等共 "+strconv.Itoa(len(nodes))+" 个"))
				break
			}
			lines = append(lines, ui.HelpStyle.Render("  "+ui.SpaceAfterFlag(n.Name)))
		}
		return ui.BoxLines(true, lines...)
	}
	if p.mode == pickPolicy {
		lines := []string{ui.TitleStyle.Render(" 选择群组策略")}
		for i, c := range policyChoices {
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
		return []string{ui.HelpStyle.Render("（无组）")}
	}
	if len(p.rows) == 0 {
		if p.nodeView.filter() != "" {
			return []string{ui.HelpStyle.Render(p.nodeView.prompt()),
				ui.ErrorStyle.Render(" 没有匹配的节点（/ 重新编辑，框内 esc 清空）")}
		}
		return []string{ui.HelpStyle.Render(" 组为空：按 s 挂订阅 / n 加手动节点")}
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

// renderRow wraps the row body with the selected-row highlight: the same
// condition that draws the ❯ cursor lights the full-width background bar.
func (p groupsPage) renderRow(g *driver.Group, i int) string {
	return ui.HiRow(p.renderRowBody(g, i), p.rightW-4, i == p.rc && p.focus == 1)
}

func (p groupsPage) renderRowBody(g *driver.Group, i int) string {
	r := &p.rows[i]
	// The cursor marker is rendered on every row kind (including section
	// headers) so the actual position is always visible.
	cur := " "
	if i == p.rc && p.focus == 1 {
		cur = ui.CursorStyle.Render("❯")
	}
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(ui.DimText)

	if r.kind == rowSub {
		s := g.Subscriptions[r.si]
		label := "▸ 订阅 " + s.Tag
		if p.subOpen[s.SubscriptionID] {
			label = "▾ 订阅 " + s.Tag
		}
		if s.NameFilterRegex != "" {
			label += " (过滤: " + s.NameFilterRegex + ")"
		}
		detail := ui.HelpStyle.Render(fmt.Sprintf("%d/%d节点  x 移除", s.MatchedCount, subTotal(p.subs, s.SubscriptionID)))
		return cur + " " + ui.PadRight(headerStyle.Render(label), max0(p.rightW-24)) + detail
	}
	if r.kind == rowDirect {
		arrow := "▸"
		if p.directOpen {
			arrow = "▾"
		}
		return cur + " " + headerStyle.Render(arrow+" 直接添加的节点 ("+strconv.Itoa(len(g.Nodes))+")")
	}

	n := r.node
	mark := "  "
	switch {
	case p.marked[n.ID]:
		mark = ui.OKStyle.Render("✓ ")
	default:
		if sel := g.SelectedNode(); sel != nil && sel.ID == n.ID {
			mark = ui.OKStyle.Render("● ")
		}
	}
	name := ui.SpaceAfterFlag(n.Name)
	if r.manual {
		name += ui.HelpStyle.Render(" (手动)")
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
			tag = "(未命名)"
		}
		out = append(out, [2]string{tag, fmt.Sprintf("%d节点", s.NodeCount)})
	}
	return out
}

// visibleCandidates applies the shared filter/sort to the add-node picker's
// rows, mapping the surviving nodes back to their rows (a node is listed
// once per source; the first occurrence wins).
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
	title := " 选择要添加到组的节点" + ui.HelpStyle.Render("  (手动 + 各订阅)")
	lines := []string{ui.TitleStyle.Render(title)}
	if p.candBusy {
		return append(lines, ui.HelpStyle.Render(" 拉取中…"))
	}
	rows := p.visibleCandidates()
	if len(p.candidateRows()) == 0 {
		return append(lines, ui.HelpStyle.Render(" （没有可添加的节点）"))
	}
	if prompt := p.nodeView.prompt(); prompt != "" {
		lines = append(lines, ui.HelpStyle.Render(prompt))
	}
	if len(rows) == 0 {
		return append(lines, ui.ErrorStyle.Render(" 没有匹配的节点（/ 重新编辑，框内 esc 清空）"))
	}
	lines[0] += p.nodeView.countTitle(len(rows), len(p.candidateRows())) +
		p.nodeView.sortTitle()
	if n := len(p.pickMarked); n > 0 {
		lines[0] += ui.OKStyle.Render(fmt.Sprintf("  已选 %d（Enter 全部添加）", n))
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
			src = "·手动"
		}
		window = append(window, ui.HiRow(style.Render(mark+ui.PadRight(ui.SpaceAfterFlag(r.node.Name), max0(p.rightW-34)))+tick+
			ui.HelpStyle.Render(ui.PadRight(src, 16)+r.node.Protocol), p.rightW-4, i == p.pickCursor))
	}
	return append(lines, ui.WithScrollbar(window, p.rightW-4, len(rows), start, true)...)
}

func (p groupsPage) pickerLines(title string, rows [][2]string, cursor int, busy bool) []string {
	lines := []string{ui.TitleStyle.Render(title)}
	if busy {
		lines[0] += ui.HelpStyle.Render("  拉取中…")
		return lines
	}
	if len(rows) == 0 {
		lines = append(lines, ui.HelpStyle.Render(" （无手动节点：可在 daed 中导入，或从订阅页面管理）"))
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

// subTotal returns a subscription's total node count from the global list.
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
			return "固定 → " + ui.SpaceAfterFlag(g.Nodes[i].Name)
		}
		return "固定"
	case "min_moving_avg":
		return "自动 (最小移动平均延迟)"
	case "min_avg10":
		return "自动 (最小平均延迟)"
	case "min":
		return "自动 (最小最新延迟)"
	case "random":
		return "随机"
	}
	return g.Policy
}
