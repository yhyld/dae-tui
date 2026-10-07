package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/yhyld/dae-tui/internal/driver"
	"github.com/yhyld/dae-tui/internal/i18n"
	"github.com/yhyld/dae-tui/internal/keymap"
	"github.com/yhyld/dae-tui/internal/ui"
)

type nodesPage struct {
	nodes  []driver.Node
	sel    int
	focus  int
	groups []driver.Group

	// pinGroupID is the managed pinned group's ID; the G picker hides it —
	// its membership belongs to the pin flow, not to ad-hoc additions.
	pinGroupID string

	lat map[string]driver.Latency

	testing   bool
	testIDs   []string
	testStart time.Time
	baseline  map[string]time.Time

	err  error
	busy bool

	mode       int
	links      textarea.Model
	link       textinput.Model
	tag        textinput.Model
	ifld       int
	pickCursor int
	// opID is the node the open modal (delete confirm, group picker, edit
	// form) targets. The visible list re-sorts on every latency poll, so
	// resolving the target through p.sel at submit time can silently act on
	// a different node than the one the user opened the modal for; the ID
	// pins the target across that drift.
	opID string

	importOK   int
	importFail []driver.NodeImportResult

	nodeView

	caps driver.Caps
	hist *latHistory

	leftW, rightW, height int
}

func newNodesPage(caps driver.Caps) nodesPage {
	l := textinput.New()
	l.CharLimit = 4096
	l.Width = 48
	t := textinput.New()
	t.CharLimit = 64
	t.Width = 32
	ta := textarea.New()
	ta.CharLimit = 65536
	ta.SetWidth(52)
	ta.SetHeight(6)
	ta.ShowLineNumbers = false
	return nodesPage{
		links: ta, link: l, tag: t,
		lat:      map[string]driver.Latency{},
		baseline: map[string]time.Time{},
		nodeView: newNodeView(),
		caps:     caps,
	}
}

func (p *nodesPage) setSize(leftW, rightW, h int) {
	p.leftW, p.rightW, p.height = leftW, rightW, h
}

func (p *nodesPage) handleNodes(nodes []driver.Node, err error) {

	p.busy = false
	if err != nil {
		p.err = err
		return
	}
	p.err = nil
	p.nodes = nodes

	if n := len(p.visibleNodes()); p.sel >= n {
		p.sel = max0(n - 1)
	}
}

func (p *nodesPage) handleImport(msg importDoneMsg) {
	p.busy = false
	p.err = msg.Err
	if len(msg.Nodes) > 0 {
		p.nodes = msg.Nodes
		if n := len(p.visibleNodes()); p.sel >= n {
			p.sel = max0(n - 1)
		}
	}
	p.importOK, p.importFail = 0, nil
	for _, r := range msg.Results {
		if r.Error == "" {
			p.importOK++
			continue
		}
		p.importFail = append(p.importFail, r)
	}
}

func (p *nodesPage) importToast() string {
	total := p.importOK + len(p.importFail)
	switch {
	case total == 0:
		return i18n.T("✓ 没有可导入的链接")
	case len(p.importFail) == 0:
		return i18n.T("✓ 导入 %d/%d 成功", p.importOK, total)
	default:
		return i18n.T("✗ 导入 %d/%d 成功，%d 条失败（详情见右栏）",
			p.importOK, total, len(p.importFail))
	}
}

func (p *nodesPage) setGroups(groups []driver.Group) {
	p.groups = groups
}

func (p *nodesPage) handleLatencies(lats []driver.Latency, err error) {
	for _, l := range lats {
		p.lat[l.NodeID] = l
	}
	if !p.testing {
		return
	}
	// Same law as groupsPage: the window ends on time even when every poll
	// errors, so a dead backend can never pin the testing flag (and the
	// per-second polling that comes with it).
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

func (p *nodesPage) testProgress() (done, total int) {
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

func (p *nodesPage) cur() *driver.Node {
	visible := p.visibleNodes()
	if p.sel < 0 || p.sel >= len(visible) {
		return nil
	}
	return &visible[p.sel]
}

func (p *nodesPage) visibleNodes() []driver.Node {
	return p.nodeView.visible(p.nodes, p.lat)
}

// opNode resolves the modal's locked target ID against the full node list
// (not the visible window, whose order drifts with every latency poll).
func (p *nodesPage) opNode() *driver.Node {
	for i := range p.nodes {
		if p.nodes[i].ID == p.opID {
			return &p.nodes[i]
		}
	}
	return nil
}

// opGone reports a modal whose locked target no longer exists (removed by a
// concurrent refresh) as a toast instead of silently doing nothing.
func opGone(what string) tea.Cmd {
	return func() tea.Msg {
		return opDoneMsg{Op: what, Err: fmt.Errorf("%s", i18n.T("目标已随刷新移除，操作已取消"))}
	}
}

func (p *nodesPage) visibleLatencyIDs() []string {
	nodes := p.visibleNodes()
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	return ids
}

func (p *nodesPage) clampCursor() {
	if n := len(p.visibleNodes()); p.sel >= n {
		p.sel = max0(n - 1)
	}
}

func (p *nodesPage) startTest(d driver.Driver, onlySelected bool) tea.Cmd {

	visible := p.visibleNodes()
	ids := make([]string, 0, len(visible))
	if onlySelected {
		if n := p.cur(); n != nil {
			ids = append(ids, n.ID)
		}
	} else {
		for i, n := range visible {
			if i >= 500 {
				break
			}
			ids = append(ids, n.ID)
		}
	}
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

func (p *nodesPage) handleKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	if p.mode == 1 {
		return p.addFormKey(msg, d)
	}
	if p.mode == 4 {
		return p.editFormKey(msg, d)
	}
	if p.mode == 2 {
		switch msg.String() {
		case "y":
			n := p.opNode()
			p.mode = 0
			if n == nil {
				return opGone(i18n.T("删除节点"))
			}
			p.busy = true
			return nodeMutateCmd(d, nodeMutation{kind: 1, ids: []string{n.ID}}, i18n.T("删除节点 ")+ui.SpaceAfterFlag(n.Name))
		case "n", "esc", "enter":
			p.mode = 0
		}
		return nil
	}
	if p.mode == 3 {
		groups := p.pickGroups()
		switch msg.String() {
		case "esc":
			p.mode = 0
		case "j", "down":
			if p.pickCursor < len(groups)-1 {
				p.pickCursor++
			}
		case "k", "up":
			if p.pickCursor > 0 {
				p.pickCursor--
			}
		case "enter":
			n := p.opNode()
			if n == nil || p.pickCursor >= len(groups) {
				p.mode = 0
				if n == nil {
					return opGone(i18n.T("加入群组"))
				}
				return nil
			}
			g := groups[p.pickCursor]
			p.mode = 0
			return groupMutateCmd(d, groupMutation{kind: 2, groupID: g.ID, ids: []string{n.ID}},
				i18n.T("添加节点 ")+ui.SpaceAfterFlag(n.Name)+i18n.T(" 到组 ")+g.Name)
		}
		return nil
	}

	if cmd, consumed := p.nodeView.handleKey(msg, keymap.Nodes); consumed {
		p.clampCursor()
		return cmd
	}

	// Remap layer: the forms and the filter input above stay raw; the
	// action switch below dispatches through the translated key.
	key := tk(keymap.Nodes, msg.String())

	switch key {
	case "a":
		// Busy guard: the import submission sets busy; opening a second
		// form while one runs would let it stack another mutation.
		if p.busy {
			return nil
		}
		p.mode = 1
		p.ifld = 0
		p.links.Reset()
		p.tag.SetValue("")
		p.tag.Blur()
		return p.links.Focus()
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
	case "y":
		if n := p.cur(); n != nil && n.Link != "" {
			return osc52CopyCmd(n.Link, i18n.T("节点链接"))
		}
	}

	if p.focus == 0 {
		nodes := p.visibleNodes()
		switch key {
		case "j", "down":
			if p.sel < len(nodes)-1 {
				p.sel++
			}
		case "k", "up":
			if p.sel > 0 {
				p.sel--
			}
		case "g":
			p.sel = 0
		case "G":
			p.sel = len(nodes) - 1
		case "tab", "l", "right", "enter":
			if p.cur() != nil {
				p.focus = 1
			}
		case "x":
			if n := p.cur(); n != nil {
				p.mode = 2
				p.opID = n.ID
			}
		case "e":
			if n := p.cur(); n != nil {
				return p.openEdit(n)
			}
		}
		return nil
	}

	switch key {
	case "tab", "h", "left", "esc":
		p.focus = 0
	case "e":
		if n := p.cur(); n != nil {
			return p.openEdit(n)
		}
	case "G":

		if n := p.cur(); n != nil && len(p.groups) > 0 {
			p.mode = 3
			p.pickCursor = 0
			p.opID = n.ID
		}
	}
	return nil
}

func (p *nodesPage) openEdit(n *driver.Node) tea.Cmd {
	p.mode = 4
	p.ifld = 0
	p.opID = n.ID
	p.tag.SetValue(n.Tag)
	p.link.SetValue(n.Link)
	p.tag.Focus()
	p.link.Blur()
	return textinput.Blink
}

func (p *nodesPage) editFormKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	switch msg.String() {
	case "esc":
		p.mode = 0
		return nil
	case "tab", "shift+tab":
		if p.ifld == 0 {
			p.ifld = 1
			p.tag.Blur()
			p.link.Focus()
		} else {
			p.ifld = 0
			p.link.Blur()
			p.tag.Focus()
		}
		return nil
	case "enter":
		n := p.opNode()
		if n == nil {
			p.mode = 0
			return opGone(i18n.T("编辑节点"))
		}
		tag := strings.TrimSpace(p.tag.Value())
		link := strings.TrimSpace(p.link.Value())
		p.mode = 0
		p.busy = true
		nm := nodeMutation{kind: 2, ids: []string{n.ID}, tag: tag, link: link}
		if link != "" && link != n.Link {
			nm.doLink = true
		}
		return nodeMutateCmd(d, nm, i18n.T("编辑节点 ")+ui.SpaceAfterFlag(n.Name))
	}
	var cmd tea.Cmd
	if p.ifld == 0 {
		p.tag, cmd = p.tag.Update(msg)
	} else {
		p.link, cmd = p.link.Update(msg)
	}
	return cmd
}

func (p *nodesPage) addFormKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	switch msg.String() {
	case "esc":
		p.mode = 0
		p.links.Blur()
		p.tag.Blur()
		return nil
	case "tab", "shift+tab":
		if p.ifld == 0 {
			p.ifld = 1
			p.links.Blur()
			return p.tag.Focus()
		}
		p.ifld = 0
		p.tag.Blur()
		return p.links.Focus()
	case "ctrl+s":
		return p.submitImport(d)
	case "enter":

		if p.ifld == 1 {
			return p.submitImport(d)
		}
	}
	var cmd tea.Cmd
	if p.ifld == 0 {
		p.links, cmd = p.links.Update(msg)
	} else {
		p.tag, cmd = p.tag.Update(msg)
	}
	return cmd
}

func (p *nodesPage) submitImport(d driver.Driver) tea.Cmd {
	links := splitLines(p.links.Value())
	tag := strings.TrimSpace(p.tag.Value())
	p.mode = 0
	p.links.Blur()
	p.tag.Blur()
	if len(links) == 0 {
		return nil
	}
	p.busy = true
	p.importOK, p.importFail = 0, nil
	return nodeMutateCmd(d, nodeMutation{kind: 0, links: links, tag: tag}, i18n.T("导入节点"))
}

func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (p nodesPage) View() string {

	rvTitle := i18n.T("详情")
	if p.mode == 2 || p.mode == 3 {
		rvTitle = p.modalTitle()
	}

	rightFooter := i18n.T("G 加入群组")
	switch p.mode {
	case 2:
		rightFooter = i18n.T("y 确认 · n/esc 取消")
	case 3:
		rightFooter = i18n.T("Enter 确认 · j/k 移动 · esc 取消")
	}
	return strings.Join(ui.PaneRow(
		ui.PaneSpec{Title: i18n.T("手动节点") + p.nodeView.countTitle(len(p.visibleNodes()), len(p.nodes)) +
			p.nodeView.sortTitle(), Footer: kb(keymap.Nodes, "x", "删除") + " · / " + i18n.T("过滤") +
			" · " + kb(keymap.Nodes, "o", "排序"), Lines: p.leftLines(),
			Focused: p.focus == 0, W: p.leftW, H: p.height},
		ui.PaneSpec{Title: rvTitle, Footer: rightFooter, Lines: p.rightLines(),
			Focused: p.focus == 1, W: p.rightW, H: p.height},
	), "\n")
}

func (p nodesPage) overlay() *overlaySpec {
	// Placeholders re-translate here rather than living in the constructor:
	// the inputs outlive a settings-window language switch.
	p.links.Placeholder = i18n.T("每行一个分享链接，可整段粘贴\nvmess://…\nss://…")
	p.link.Placeholder = i18n.T("vmess://… / ss://… / trojan://… 分享链接")
	p.tag.Placeholder = i18n.T("标签 (可空)")
	switch p.mode {
	case 1:
		lines := []string{
			ui.TitleStyle.Render(i18n.T(" 导入手动节点 (分享链接)")),
			"",
		}
		lines = append(lines, strings.Split(p.links.View(), "\n")...)
		lines = append(lines,
			"",
			i18n.T(" 标签  ")+p.tag.View(),
			"",
			ui.HelpStyle.Render(i18n.T(" Tab 切换字段  标签框内 Enter 或 ctrl+s 提交  esc 取消")),
			ui.HelpStyle.Render(i18n.T(" 批量粘贴：每行一条，坏链接会逐条报错，不影响其他链接")))
		return &overlaySpec{lines: lines}
	case 4:
		title := i18n.T(" 编辑手动节点")
		if n := p.opNode(); n != nil {
			title += " · " + ui.SpaceAfterFlag(n.Name)
		}
		return &overlaySpec{lines: []string{
			ui.TitleStyle.Render(title),
			"",
			i18n.T(" 标签  ") + p.tag.View(),
			i18n.T(" 链接  ") + p.link.View(),
			"",
			ui.HelpStyle.Render(i18n.T(" Tab 切换字段  Enter 提交  esc 取消")),
		}}
	}
	return nil
}

// pickGroups is the G picker's candidate list: every group except the
// managed pinned one. Navigation, Enter and rendering all go through it so
// the cursor index cannot drift from what is on screen.
func (p nodesPage) pickGroups() []driver.Group {
	out := make([]driver.Group, 0, len(p.groups))
	for _, g := range p.groups {
		if p.pinGroupID != "" && g.ID == p.pinGroupID {
			continue
		}
		out = append(out, g)
	}
	return out
}

func (p nodesPage) modalTitle() string {
	switch p.mode {
	case 2:
		return i18n.T("删除确认")
	case 3:
		return i18n.T("加入群组")
	}
	return i18n.T("详情")
}

func (p nodesPage) modalLines() []string {
	switch p.mode {
	case 2:
		if n := p.opNode(); n != nil {
			return ui.BoxLines(true, i18n.T("确认删除节点 \"")+ui.SpaceAfterFlag(n.Name)+"\"? (y/n)")
		}
	case 3:
		lines := []string{ui.TitleStyle.Render(i18n.T(" 选择要加入的群组"))}
		for i, g := range p.pickGroups() {
			mark, style := "  ", ui.HelpStyle
			if i == p.pickCursor {
				mark, style = "❯ ", ui.CursorStyle
			}
			lines = append(lines, ui.HiRow(style.Render(mark+ui.PadRight(g.Name, 24))+
				ui.HelpStyle.Render(i18n.T("%d节点", len(g.Nodes))), p.rightW-4, i == p.pickCursor))
		}
		return lines
	}
	return nil
}

func (p nodesPage) leftLines() []string {
	var lines []string
	if p.err != nil {
		lines = append(lines, ui.ErrorStyle.Render("✗ "+shortErr(p.err)))
	}
	if len(p.nodes) == 0 {
		lines = append(lines, ui.HelpStyle.Render(centerLine(i18n.T("无手动节点（按 a 导入）"), p.leftW-4)))
	}
	if prompt := p.nodeView.prompt(); prompt != "" {
		lines = append(lines, ui.HelpStyle.Render(prompt))
	}
	nodes := p.visibleNodes()
	if len(p.nodes) > 0 && len(nodes) == 0 {
		lines = append(lines, ui.ErrorStyle.Render(i18n.T(" 没有匹配的节点（/ 重新编辑，框内 esc 清空）")))
	}
	rowsH := max0(p.height - 2 - len(lines))
	start := 0
	if p.sel >= rowsH {
		start = p.sel - rowsH + 1
	}
	var list []string
	for i := start; i < len(nodes) && i < start+rowsH; i++ {
		n := nodes[i]
		cursor := " "
		if i == p.sel {
			cursor = ui.CursorStyle.Render("❯")
		}
		row := cursor + " " + ui.PadRight(nodeName(n, p.nodeView.filter()), max0(p.leftW-17-latCellW(p.leftW-4))) +
			ui.HelpStyle.Render(ui.PadRight(n.Protocol, 8)) +
			latencyCell(p.lat, n.ID, latCellW(p.leftW-4))
		list = append(list, ui.HiRow(row, p.leftW-4, i == p.sel))
	}
	return append(lines, ui.WithScrollbar(list, p.leftW-4, len(nodes), start, p.focus == 0)...)
}

func (p *nodesPage) leftClick(row int) {
	prefix := 0
	if p.err != nil {
		prefix++
	}
	if len(p.nodes) == 0 {
		prefix++
	}
	if p.nodeView.filter() != "" {
		prefix++
	}
	nodes := p.visibleNodes()
	if len(p.nodes) > 0 && len(nodes) == 0 {
		prefix++
	}
	row -= prefix
	if row < 0 {
		return
	}
	rowsH := max0(p.height - 2 - prefix)
	if row >= rowsH {
		return
	}
	start := 0
	if p.sel >= rowsH {
		start = p.sel - rowsH + 1
	}
	i := start + row
	if i < 0 || i >= len(nodes) || i == p.sel {
		return
	}
	p.sel = i
	p.focus = 0
}

const trendChartH = 4

func (p nodesPage) rightLines() []string {
	if p.mode == 2 || p.mode == 3 {
		return p.modalLines()
	}
	n := p.cur()
	if n == nil {
		return []string{ui.HelpStyle.Render(i18n.T("（无节点）"))}
	}
	lines := []string{
		detailLabel("名称") + ui.SpaceAfterFlag(n.Name),
		detailLabel("协议") + n.Protocol,
		detailLabel("地址") + ui.Truncate(n.Address, max0(p.rightW-10)),
	}
	if n.Tag != "" {
		lines = append(lines, detailLabel("标签")+n.Tag)
	}
	if n.Link != "" {
		lines = append(lines, detailLabel("链接")+ui.Truncate(n.Link, max0(p.rightW-10)))
	}

	lines = append(lines, detailLabel("群组")+p.groupMembership(n.ID))
	if row, ok := latencyDetail(p.lat, n.ID); ok {
		line := detailLabel("延迟") + row

		if l := p.lat[n.ID]; !l.Alive && l.Message != "" {
			line += ui.HelpStyle.Render("  " + ui.Truncate(firstLine(l.Message), max0(p.rightW-26)))
		}
		lines = append(lines, line+ui.HelpStyle.Render("  · "+ui.TimeAgo(p.lat[n.ID].TestedAt)))
	}
	lines = append(lines, p.trendLines(n.ID)...)

	if p.importOK > 0 || len(p.importFail) > 0 {
		lines = append(lines, "", ui.SelectedStyle.Render(i18n.T("上次导入 "))+
			ui.HelpStyle.Render(i18n.T("%d 成功 / %d 失败", p.importOK, len(p.importFail))))
		for _, r := range p.importFail {

			lines = append(lines, ui.ErrorStyle.Render(" ✗ "+ui.Truncate(r.Link, max0(p.rightW-7))))
			lines = append(lines, ui.ErrorStyle.Render("    "+ui.Truncate(r.Error, max0(p.rightW-8))))
		}
	}
	return lines
}

func (p nodesPage) groupMembership(id string) string {
	var names []string
	for _, g := range p.groups {
		for _, n := range g.Nodes {
			if n.ID == id {
				names = append(names, g.Name)
				break
			}
		}
	}
	if len(names) == 0 {
		return ui.WarnStyle.Render(i18n.T("未加入任何群组（G 加入）"))
	}
	return strings.Join(names, "、")
}

func (p nodesPage) trendLines(id string) []string {
	w := max0(p.rightW - 4)
	empty := []string{ui.SelectedStyle.Render(i18n.T("趋势  ")) +
		ui.HelpStyle.Render(i18n.T("（暂无数据：按 t 测速后这里显示约 3 分钟的曲线）"))}
	if p.hist == nil || w < 12 {
		return empty
	}
	series := p.hist.series(id)
	if len(series) < 2 {
		return empty
	}
	lo, hi := series[0], series[0]
	for _, v := range series {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	head := ui.SelectedStyle.Render(i18n.T("趋势  ")) + ui.HelpStyle.Render(fmt.Sprintf(
		i18n.T("近 %s · %d 次采样 · 最低 %dms · 最高 %dms"),
		ui.Span(p.hist.span(id)), len(series), int(lo), int(hi)))
	l := p.lat[id]
	st := ui.LatencyStyle(l.Ms, l.Alive, !l.TestedAt.IsZero())
	return append([]string{head},
		strings.Split(ui.Sparkline(series, w, trendChartH, st), "\n")...)
}
