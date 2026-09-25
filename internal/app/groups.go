package app

import (
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

// groupsPage: master-detail. Left pane lists groups only (collapsed by
// default); the right pane shows the selected group's detail — attached
// subscriptions with their matched nodes, then manually added nodes — with
// its own cursor and scroll window. Tab/l expands (focus right), h/esc
// returns to the group list.
type groupsPage struct {
	groups []driver.Group
	subs   []driver.Subscription

	gi       int  // left cursor: group index
	focus    int  // 0 left, 1 right
	expanded bool // detail shown only after the user expands
	// per-section expansion in the right pane (default collapsed)
	subOpen    map[int]bool
	directOpen bool

	// right pane flattened detail rows for the selected group
	rows []trow
	rc   int // right cursor

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
	pickPin
	pickRemoveNode
)

type trow struct {
	kind   int
	gi     int
	si     int
	node   driver.Node
	manual bool // node row in the direct section: subscription-less node
	direct bool // node row in the direct section (explicitly attached)
}

func newGroupsPage() groupsPage {
	ti := textinput.New()
	ti.Placeholder = "名称"
	ti.CharLimit = 64
	ti.Width = 32
	return groupsPage{
		lat:      map[string]driver.Latency{},
		baseline: map[string]time.Time{},
		subOpen:  map[int]bool{},
		input:    ti,
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

// rebuild regenerates the right-pane rows for the selected group.
func (p *groupsPage) rebuild() {
	p.rows = p.rows[:0]
	if p.gi >= len(p.groups) {
		return
	}
	g := &p.groups[p.gi]
	for si := range g.Subscriptions {
		p.rows = append(p.rows, trow{kind: rowSub, gi: p.gi, si: si})
		if p.subOpen[si] {
			for _, n := range g.Subscriptions[si].Nodes {
				p.rows = append(p.rows, trow{kind: rowNode, gi: p.gi, si: si, node: n})
			}
		}
	}
	// Group.nodes is exactly the set of directly-attached nodes (manual
	// imports plus nodes picked out of subscriptions).
	if len(g.Nodes) > 0 {
		p.rows = append(p.rows, trow{kind: rowDirect, gi: p.gi})
		if p.directOpen {
			for _, n := range g.Nodes {
				p.rows = append(p.rows, trow{kind: rowNode, gi: p.gi, node: n,
					manual: n.SubscriptionID == "", direct: true})
			}
		}
	}
	if p.rc >= len(p.rows) {
		p.rc = max0(len(p.rows) - 1)
	}
}

// collapseSections resets per-section expansion (group change / collapse).
func (p *groupsPage) collapseSections() {
	p.subOpen = map[int]bool{}
	p.directOpen = false
	p.rc = 0
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
	p.subs = subs
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
	if time.Since(p.testStart) > 15*time.Second {
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

func (p *groupsPage) testIDsFor(onlySelected bool) []string {
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

func (p *groupsPage) handleKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	if p.mode != pickNone {
		return p.pickerKey(msg, d)
	}

	// Global to this page.
	switch msg.String() {
	case "a":
		if g := p.curGroup(); g != nil {
			return switchNodeCmd(d, g.ID, driver.Policy{Name: "min_moving_avg"})
		}
	case "t":
		return p.startTest(d, false)
	case "T":
		return p.startTest(d, true)
	}

	if p.focus == 0 {
		switch msg.String() {
		case "j", "down":
			if p.gi < len(p.groups)-1 {
				p.gi++
				p.rc = 0
				p.expanded = false
				p.collapseSections()
			}
		case "k", "up":
			if p.gi > 0 {
				p.gi--
				p.rc = 0
				p.expanded = false
				p.collapseSections()
			}
		case "g":
			p.gi = 0
			p.rc = 0
			p.expanded = false
			p.collapseSections()
		case "G":
			p.gi = max0(len(p.groups) - 1)
			p.rc = 0
			p.expanded = false
			p.collapseSections()
		case "tab", "l", "right", "enter":
			if len(p.groups) > 0 {
				p.expanded = true
				p.rebuild()
				if len(p.rows) > 0 {
					p.focus = 1
				}
			}
		case "s":
			g := p.curGroup()
			if g == nil {
				return nil
			}
			if len(p.pickableSubs(g)) == 0 {
				p.pickErr = "没有可添加的订阅（全部已挂载或无订阅）"
				return nil
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
		p.focus = 0
		p.expanded = false
		p.collapseSections()
	case "enter", "l", "right":
		r := p.cur()
		if r == nil {
			return nil
		}
		switch r.kind {
		case rowSub:
			p.subOpen[r.si] = !p.subOpen[r.si]
			p.rebuild()
		case rowDirect:
			p.directOpen = !p.directOpen
			p.rebuild()
		case rowNode:
			p.mode = pickPin
		}
	case "x":
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

func (p *groupsPage) pickerKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	if p.mode == pickDetach {
		switch msg.String() {
		case "y":
			r := p.cur()
			p.mode = pickNone
			if r == nil || r.gi >= len(p.groups) || r.si >= len(p.groups[r.gi].Subscriptions) {
				return nil
			}
			sub := p.groups[r.gi].Subscriptions[r.si]
			return groupMutateCmd(d, groupMutation{kind: 1, groupID: p.groups[r.gi].ID,
				ids: []string{sub.SubscriptionID}}, "移除组内订阅 "+sub.Tag)
		case "n", "esc", "enter":
			p.mode = pickNone
		}
		return nil
	}

	if p.mode == pickDeleteGroup {
		switch msg.String() {
		case "y":
			g := p.curGroup()
			p.mode = pickNone
			if g == nil {
				return nil
			}
			return groupMutateCmd(d, groupMutation{kind: 4, groupID: g.ID}, "删除群组 "+g.Name)
		case "n", "esc", "enter":
			p.mode = pickNone
		}
		return nil
	}

	if p.mode == pickPin {
		switch msg.String() {
		case "y":
			r := p.cur()
			p.mode = pickNone
			if r == nil || r.kind != rowNode {
				return nil
			}
			return pinNodeCmd(d, p.groups[r.gi].ID, r.node.ID, ui.SpaceAfterFlag(r.node.Name))
		case "n", "esc", "enter":
			p.mode = pickNone
		}
		return nil
	}

	if p.mode == pickRemoveNode {
		switch msg.String() {
		case "y":
			r := p.cur()
			p.mode = pickNone
			if r == nil || r.kind != rowNode {
				return nil
			}
			return groupMutateCmd(d, groupMutation{kind: 7, groupID: p.groups[r.gi].ID,
				ids: []string{r.node.ID}}, "移除组内节点 "+ui.SpaceAfterFlag(r.node.Name))
		case "n", "esc", "enter":
			p.mode = pickNone
		}
		return nil
	}

	if p.mode == inputCreate || p.mode == inputRename {
		switch msg.String() {
		case "esc":
			p.mode = pickNone
			p.input.Blur()
			return nil
		case "enter":
			name := strings.TrimSpace(p.input.Value())
			isCreate := p.mode == inputCreate
			p.mode = pickNone
			p.input.Blur()
			if name == "" {
				return nil
			}
			if isCreate {
				return groupMutateCmd(d, groupMutation{kind: 3, name: name, policy: "min_moving_avg"},
					"创建群组 "+name)
			}
			g := p.curGroup()
			if g == nil {
				return nil
			}
			return groupMutateCmd(d, groupMutation{kind: 5, groupID: g.ID, name: name},
				"重命名群组 "+g.Name+" → "+name)
		}
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(msg)
		return cmd
	}

	var n int
	switch p.mode {
	case pickSub:
		if g := p.curGroup(); g != nil {
			n = len(p.pickableSubs(g))
		}
	case pickNode:
		n = len(p.candidateRows())
	case pickPolicy:
		n = len(policyChoices)
	}
	switch msg.String() {
	case "esc":
		p.mode = pickNone
		p.candBusy = false
	case "j", "down":
		if p.pickCursor < n-1 {
			p.pickCursor++
		}
	case "k", "up":
		if p.pickCursor > 0 {
			p.pickCursor--
		}
	case "enter":
		g := p.curGroup()
		if g == nil || p.pickCursor >= n {
			return nil
		}
		switch p.mode {
		case pickSub:
			sub := p.pickableSubs(g)[p.pickCursor]
			p.mode = pickNone
			return groupMutateCmd(d, groupMutation{kind: 0, groupID: g.ID, ids: []string{sub.ID}},
				"添加订阅 "+sub.Tag+" 到组 "+g.Name)
		case pickNode:
			rows := p.candidateRows()
			if p.pickCursor >= len(rows) {
				p.mode = pickNone
				return nil
			}
			node := rows[p.pickCursor].node
			p.mode = pickNone
			return groupMutateCmd(d, groupMutation{kind: 2, groupID: g.ID, ids: []string{node.ID}},
				"添加节点 "+ui.SpaceAfterFlag(node.Name)+" 到组 "+g.Name)
		case pickPolicy:
			choice := policyChoices[p.pickCursor]
			p.mode = pickNone
			return groupMutateCmd(d, groupMutation{kind: 6, groupID: g.ID, policy: choice.name},
				g.Name+" 策略改为 "+choice.label)
		}
	}
	return nil
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
	if p.err != nil {
		return ui.ErrorStyle.Render(" ✗ 加载路由组失败: " + shortErr(p.err))
	}
	if len(p.groups) == 0 {
		if p.loading {
			return ui.HelpStyle.Render(" 加载中…")
		}
		return ui.HelpStyle.Render(" 无路由组（在 daed 中创建组后刷新）")
	}

	left := p.leftLines()
	right := p.rightLines()
	lv := ui.Pane(fmt.Sprintf(" 路由组 (%d) ", len(p.groups)), p.focus == 0, p.leftW, p.height, left)
	var title string
	if g := p.curGroup(); g != nil {
		title = fmt.Sprintf(" %s · %s ", g.Name, policyLabel(g))
	}
	rv := ui.Pane(title, p.focus == 1, p.rightW, p.height, right)
	return lipgloss.JoinHorizontal(lipgloss.Top, lv, " ", rv)
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
		line := cursor + " " + ui.PadRight(g.Name, max0(p.leftW-18)) +
			ui.HelpStyle.Render(fmt.Sprintf("%3d节点", len(g.Members())))
		lines = append(lines, line+ui.Truncate(cur, max0(p.leftW-12)))
	}
	if p.pickErr != "" {
		lines = append(lines, "", ui.ErrorStyle.Render("✗ "+p.pickErr))
	}
	return lines
}

func (p groupsPage) rightLines() []string {
	if p.mode == pickSub {
		return p.pickerLines("选择要添加到组的订阅", p.subPickRows(), p.pickCursor, false)
	}
	if p.mode == pickNode {
		return p.candidateLines()
	}
	if p.mode == pickDetach {
		if r := p.cur(); r != nil && r.gi < len(p.groups) && r.si < len(p.groups[r.gi].Subscriptions) {
			sub := p.groups[r.gi].Subscriptions[r.si]
			return []string{ui.ErrorStyle.Render("确认将订阅 \"" + sub.Tag + "\" 从组 \"" +
				p.groups[r.gi].Name + "\" 移除?  (y/n)")}
		}
	}
	if p.mode == pickDeleteGroup {
		if g := p.curGroup(); g != nil {
			return []string{ui.ErrorStyle.Render("确认删除群组 \"" + g.Name + "\"?  (y/n)")}
		}
	}
	if p.mode == pickPin {
		if r := p.cur(); r != nil && r.kind == rowNode {
			return []string{
				ui.ErrorStyle.Render("固定到节点 \"" + ui.SpaceAfterFlag(r.node.Name) + "\"?  (y/n)"),
				ui.HelpStyle.Render(" daed v2 语义: fixed 组只能有一个成员节点。"),
				ui.HelpStyle.Render(" 将移除该组的其他订阅挂载与节点，仅保留此节点"),
				ui.HelpStyle.Render(" (策略 fixed)。之后可用 s 重新挂载订阅恢复。"),
			}
		}
	}
	if p.mode == pickRemoveNode {
		if r := p.cur(); r != nil && r.kind == rowNode {
			return []string{ui.ErrorStyle.Render("确认将节点 \"" + ui.SpaceAfterFlag(r.node.Name) + "\" 从组中移除?  (y/n)")}
		}
	}
	if p.mode == inputCreate {
		return []string{ui.TitleStyle.Render(" 创建群组 (默认策略: 自动·最小移动平均)"),
			"", " 名称  " + p.input.View(), "",
			ui.HelpStyle.Render(" Enter 确认  esc 取消")}
	}
	if p.mode == inputRename {
		return []string{ui.TitleStyle.Render(" 重命名群组"),
			"", " 名称  " + p.input.View(), "",
			ui.HelpStyle.Render(" Enter 确认  esc 取消")}
	}
	if p.mode == pickPolicy {
		lines := []string{ui.TitleStyle.Render(" 选择群组策略")}
		for i, c := range policyChoices {
			mark, style := "  ", ui.HelpStyle
			if i == p.pickCursor {
				mark, style = "❯ ", ui.CursorStyle
			}
			lines = append(lines, style.Render(mark+c.label))
		}
		return append(lines, ui.HelpStyle.Render(" Enter 确认  esc 取消"))
	}

	g := p.curGroup()
	if g == nil {
		return []string{ui.HelpStyle.Render("（无组）")}
	}
	if !p.expanded {
		direct := len(g.Nodes)
		return []string{
			ui.HelpStyle.Render(fmt.Sprintf("策略   ") + policyLabel(g)),
			ui.HelpStyle.Render(fmt.Sprintf("成员   %d（订阅挂载 %d 个，直接挂载 %d 个）", len(g.Members()), len(g.Subscriptions), direct)),
			"",
			ui.SelectedStyle.Render("按 Tab/l/Enter 展开查看订阅与节点"),
		}
	}
	if len(p.rows) == 0 {
		return []string{ui.HelpStyle.Render(" 组为空：按 s 挂订阅 / n 加手动节点")}
	}

	rowsH := max0(p.height - 2)
	start := 0
	if p.rc >= rowsH {
		start = p.rc - rowsH + 1
	}
	lines := make([]string, 0, rowsH)
	for i := start; i < len(p.rows) && i < start+rowsH; i++ {
		lines = append(lines, p.renderRow(g, i))
	}
	return lines
}

func (p groupsPage) renderRow(g *driver.Group, i int) string {
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
		if p.subOpen[r.si] {
			label = "▾ 订阅 " + s.Tag
		}
		if s.NameFilterRegex != "" {
			label += " (过滤: " + s.NameFilterRegex + ")"
		}
		detail := ui.HelpStyle.Render(fmt.Sprintf("%d/%d节点  x 移除", s.MatchedCount, subTotal(p.subs, s.SubscriptionID)))
		return cur + " " + ui.PadRight(headerStyle.Render(label), max0(p.rightW-22)) + detail
	}
	if r.kind == rowDirect {
		arrow := "▸"
		if p.directOpen {
			arrow = "▾"
		}
		return cur + " " + headerStyle.Render(arrow+" 直接添加的节点 ("+strconv.Itoa(len(g.Nodes))+")")
	}

	n := r.node
	l, hasLat := p.lat[n.ID]
	latStr, latStyle := "-", ui.LatencyStyle(0, false, false)
	if hasLat && !l.TestedAt.IsZero() {
		if l.Alive && l.Ms > 0 {
			latStr = fmt.Sprintf("%dms", l.Ms)
		} else if !l.Alive {
			latStr = "dead"
		} else {
			latStr = "…"
		}
		latStyle = ui.LatencyStyle(l.Ms, l.Alive, true)
	}
	mark := "  "
	if sel := g.SelectedNode(); sel != nil && sel.ID == n.ID {
		mark = ui.OKStyle.Render("● ")
	}
	name := ui.SpaceAfterFlag(n.Name)
	if r.manual {
		name += ui.HelpStyle.Render(" (手动)")
	}
	latW, protoW := 9, 8
	nameW := max0(p.rightW - latW - protoW - 12)
	return "│  " + mark + cur + " " +
		ui.PadRight(name, nameW) +
		ui.HelpStyle.Render(ui.PadRight(n.Protocol, protoW)) +
		latStyle.Render(ui.PadLeft(latStr, latW))
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

func (p groupsPage) candidateLines() []string {
	lines := []string{ui.TitleStyle.Render(" 选择要添加到组的节点") + ui.HelpStyle.Render("  (手动 + 各订阅)")}
	if p.candBusy {
		return append(lines, ui.HelpStyle.Render(" 拉取中…"))
	}
	rows := p.candidateRows()
	if len(rows) == 0 {
		return append(lines, ui.HelpStyle.Render(" （没有可添加的节点）"))
	}
	rowsH := max0(p.height - 5)
	start := 0
	if p.pickCursor >= rowsH {
		start = p.pickCursor - rowsH + 1
	}
	for i := start; i < len(rows) && i < start+rowsH; i++ {
		r := rows[i]
		mark, style := "  ", ui.HelpStyle
		if i == p.pickCursor {
			mark, style = "❯ ", ui.CursorStyle
		}
		src := ""
		if r.tag != "" {
			src = "·" + r.tag
		} else {
			src = "·手动"
		}
		lines = append(lines, style.Render(mark+ui.PadRight(ui.SpaceAfterFlag(r.node.Name), max0(p.rightW-32)))+
			ui.HelpStyle.Render(ui.PadRight(src, 16)+r.node.Protocol))
	}
	return append(lines, ui.HelpStyle.Render(" Enter 添加  esc 取消"))
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
		lines = append(lines, style.Render(mark+ui.PadRight(r[0], 28))+ui.HelpStyle.Render(r[1]))
	}
	return append(lines, ui.HelpStyle.Render(" Enter 确认  esc 取消"))
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
