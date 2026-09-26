package app

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"dae-tui/internal/driver"
	"dae-tui/internal/ui"
)

// nodesPage manages manual (subscription-less) nodes: list, import from
// share links (one or a pasted batch), edit tag/link in place, remove,
// latency-test, and attach to a group.
type nodesPage struct {
	nodes    []driver.Node
	sel      int
	focus    int // 0 left, 1 right
	expanded bool
	groups   []driver.Group // for the attach-to-group picker

	lat map[string]driver.Latency

	// latency test polling state
	testing   bool
	testIDs   []string
	testStart time.Time
	baseline  map[string]time.Time

	err  error
	busy bool

	mode       int            // 0 list, 1 add form, 2 delete confirm, 3 group picker, 4 edit form
	links      textarea.Model // import form: one share link per line
	link       textinput.Model
	tag        textinput.Model
	ifld       int
	pickCursor int

	// last batch import report, shown in the detail pane until the next one
	importOK   int
	importFail []driver.NodeImportResult

	nodeView // filter/sort for the node list

	caps driver.Caps
	hist *latHistory // shared per-node latency samples for the trend sparkline

	leftW, rightW, height int
}

func newNodesPage(caps driver.Caps) nodesPage {
	l := textinput.New()
	l.Placeholder = "vmess://… / ss://… / trojan://… 分享链接"
	l.CharLimit = 4096
	l.Width = 48
	t := textinput.New()
	t.Placeholder = "标签 (可空)"
	t.CharLimit = 64
	t.Width = 32
	ta := textarea.New()
	ta.Placeholder = "每行一个分享链接，可整段粘贴\nvmess://…\nss://…"
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
	if err != nil {
		p.err = err
		return
	}
	p.err = nil
	p.busy = false
	p.nodes = nodes
	if p.sel >= len(nodes) {
		p.sel = max0(len(nodes) - 1)
	}
}

// handleImport stores a batch import's per-link outcomes: the failures stay
// visible in the detail pane, because a toast line cannot name them.
func (p *nodesPage) handleImport(msg importDoneMsg) {
	p.busy = false
	if msg.Err != nil {
		p.err = msg.Err
		return
	}
	p.err = nil
	p.nodes = msg.Nodes
	if p.sel >= len(p.nodes) {
		p.sel = max0(len(p.nodes) - 1)
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

// importToast summarizes the last batch import for the toast line.
func (p *nodesPage) importToast() string {
	total := p.importOK + len(p.importFail)
	switch {
	case total == 0:
		return "✓ 没有可导入的链接"
	case len(p.importFail) == 0:
		return fmt.Sprintf("✓ 导入 %d/%d 成功", p.importOK, total)
	default:
		return fmt.Sprintf("✗ 导入 %d/%d 成功，%d 条失败（详情见右栏）",
			p.importOK, total, len(p.importFail))
	}
}

func (p *nodesPage) setGroups(groups []driver.Group) {
	p.groups = groups
}

func (p *nodesPage) handleLatencies(lats []driver.Latency) {
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

// testProgress reports how many probed nodes have reported back.
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

// visibleNodes is the manual node list as the filter and sort present it.
func (p *nodesPage) visibleNodes() []driver.Node {
	return p.nodeView.visible(p.nodes, p.lat)
}

// visibleLatencyIDs lists the nodes whose latency the list and the detail
// pane render — the manual node list is always on screen.
func (p *nodesPage) visibleLatencyIDs() []string {
	nodes := p.visibleNodes()
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	return ids
}

// clampCursor keeps the cursor inside the filtered list, which can shrink
// under it while the user types.
func (p *nodesPage) clampCursor() {
	if n := len(p.visibleNodes()); p.sel >= n {
		p.sel = max0(n - 1)
	}
}

func (p *nodesPage) startTest(d driver.Driver, onlySelected bool) tea.Cmd {
	// Probing covers exactly what is on screen: the filtered, sorted view.
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
			n := p.cur()
			p.mode = 0
			if n == nil {
				return nil
			}
			p.busy = true
			return nodeMutateCmd(d, nodeMutation{kind: 1, ids: []string{n.ID}}, "删除节点 "+ui.SpaceAfterFlag(n.Name))
		case "n", "esc", "enter":
			p.mode = 0
		}
		return nil
	}
	if p.mode == 3 {
		switch msg.String() {
		case "esc":
			p.mode = 0
		case "j", "down":
			if p.pickCursor < len(p.groups)-1 {
				p.pickCursor++
			}
		case "k", "up":
			if p.pickCursor > 0 {
				p.pickCursor--
			}
		case "enter":
			n := p.cur()
			if n == nil || p.pickCursor >= len(p.groups) {
				p.mode = 0
				return nil
			}
			g := p.groups[p.pickCursor]
			p.mode = 0
			return groupMutateCmd(d, groupMutation{kind: 2, groupID: g.ID, ids: []string{n.ID}},
				"添加节点 "+ui.SpaceAfterFlag(n.Name)+" 到组 "+g.Name)
		}
		return nil
	}

	// The node filter box swallows every key while it is open.
	if cmd, consumed := p.nodeView.handleKey(msg); consumed {
		p.clampCursor()
		return cmd
	}

	switch msg.String() {
	case "a":
		p.mode = 1
		p.ifld = 0
		p.links.Reset()
		p.tag.SetValue("")
		p.tag.Blur()
		return p.links.Focus()
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
	case "y":
		if n := p.cur(); n != nil && n.Link != "" {
			return osc52CopyCmd(n.Link)
		}
	}

	if p.focus == 0 {
		nodes := p.visibleNodes()
		switch msg.String() {
		case "j", "down":
			if p.sel < len(nodes)-1 {
				p.sel++
				p.expanded = false
			}
		case "k", "up":
			if p.sel > 0 {
				p.sel--
				p.expanded = false
			}
		case "g":
			p.sel = 0
		case "G":
			p.sel = len(nodes) - 1
		case "tab", "l", "right", "enter":
			if p.cur() != nil {
				p.focus = 1
				p.expanded = true
			}
		case "x":
			if p.cur() != nil {
				p.mode = 2
			}
		case "e":
			if n := p.cur(); n != nil {
				return p.openEdit(n)
			}
		}
		return nil
	}

	switch msg.String() {
	case "tab", "h", "left", "esc":
		p.focus = 0
		p.expanded = false
	case "e":
		if n := p.cur(); n != nil {
			return p.openEdit(n)
		}
	case "G":
		// attach to group
		if p.cur() != nil && len(p.groups) > 0 {
			p.mode = 3
			p.pickCursor = 0
		}
	}
	return nil
}

// openEdit prefills the edit form with the node's tag and link. Editing in
// place is what keeps the node's group memberships: tagNode/updateNode keep
// the ID, while remove + re-import mints a new one.
func (p *nodesPage) openEdit(n *driver.Node) tea.Cmd {
	p.mode = 4
	p.ifld = 0
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
		n := p.cur()
		if n == nil {
			p.mode = 0
			return nil
		}
		tag := strings.TrimSpace(p.tag.Value())
		link := strings.TrimSpace(p.link.Value())
		p.mode = 0
		p.busy = true
		nm := nodeMutation{kind: 2, ids: []string{n.ID}, tag: tag, link: link}
		if link != "" && link != n.Link {
			nm.doLink = true
		}
		return nodeMutateCmd(d, nm, "编辑节点 "+ui.SpaceAfterFlag(n.Name))
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
		// Enter submits from the tag field; inside the link box it is a
		// newline, which is what a pasted batch needs.
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

// submitImport imports every non-blank line of the link box in one mutation.
// rollbackError is off on the backend side, so a bad link is reported per
// link instead of discarding the batch.
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
	return nodeMutateCmd(d, nodeMutation{kind: 0, links: links, tag: tag}, "导入节点")
}

// splitLines turns a pasted block into links: one per line, blanks dropped.
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
	// Modals render inside the right pane: the panes always fill the app
	// frame, so anything appended below them would be pushed off screen.
	lv := ui.Pane(false, " 手动节点"+p.nodeView.countTitle(len(p.visibleNodes()), len(p.nodes))+
		p.nodeView.sortTitle()+" ", p.focus == 0, p.leftW, p.height, p.leftLines())
	rv := ui.Pane(true, p.modalTitle(), p.focus == 1, p.rightW, p.height, p.rightLines())
	return lipgloss.JoinHorizontal(lipgloss.Top, lv, " ", rv)
}

func (p nodesPage) modalTitle() string {
	switch p.mode {
	case 1:
		return " 批量导入 "
	case 2:
		return " 删除确认 "
	case 3:
		return " 加入群组 "
	case 4:
		return " 编辑节点 "
	}
	return " 详情 "
}

// modalLines renders the active modal (import / delete / group picker /
// edit) inside the right pane.
func (p nodesPage) modalLines() []string {
	switch p.mode {
	case 1:
		lines := []string{
			ui.TitleStyle.Render(" 导入手动节点 (分享链接)"),
			"",
		}
		lines = append(lines, strings.Split(p.links.View(), "\n")...)
		lines = append(lines,
			"",
			" 标签  "+p.tag.View(),
			"",
			ui.HelpStyle.Render(" Tab 切换字段  标签框内 Enter 或 ctrl+s 提交  esc 取消"),
			ui.HelpStyle.Render(" 批量粘贴：每行一条，坏链接会逐条报错，不影响其他链接"))
		return ui.BoxLines(false, lines...)
	case 2:
		if n := p.cur(); n != nil {
			return ui.BoxLines(true, "确认删除节点 \""+ui.SpaceAfterFlag(n.Name)+"\"? (y/n)")
		}
	case 3:
		lines := []string{ui.TitleStyle.Render(" 选择要加入的群组")}
		for i, g := range p.groups {
			mark, style := "  ", ui.HelpStyle
			if i == p.pickCursor {
				mark, style = "❯ ", ui.CursorStyle
			}
			lines = append(lines, style.Render(mark+ui.PadRight(g.Name, 24))+
				ui.HelpStyle.Render(strconv.Itoa(len(g.Nodes))+"节点"))
		}
		return append(lines, ui.HelpStyle.Render(" Enter 确认  esc 取消"))
	case 4:
		title := " 编辑手动节点"
		if n := p.cur(); n != nil {
			title += " · " + ui.SpaceAfterFlag(n.Name)
		}
		return ui.BoxLines(false,
			ui.TitleStyle.Render(title),
			"",
			" 标签  "+p.tag.View(),
			" 链接  "+p.link.View(),
			"",
			ui.HelpStyle.Render(" Tab 切换字段  Enter 提交  esc 取消"))
	}
	return nil
}

func (p nodesPage) leftLines() []string {
	var lines []string
	if p.err != nil {
		lines = append(lines, ui.ErrorStyle.Render("✗ "+shortErr(p.err)))
	}
	if len(p.nodes) == 0 {
		lines = append(lines, ui.HelpStyle.Render("无手动节点（按 a 导入）"))
	}
	if prompt := p.nodeView.prompt(); prompt != "" {
		lines = append(lines, ui.HelpStyle.Render(prompt))
	}
	nodes := p.visibleNodes()
	if len(p.nodes) > 0 && len(nodes) == 0 {
		lines = append(lines, ui.ErrorStyle.Render(" 没有匹配的节点（/ 重新编辑，框内 esc 清空）"))
	}
	rowsH := max0(p.height - 2 - len(lines))
	start := 0
	if p.sel >= rowsH {
		start = p.sel - rowsH + 1
	}
	for i := start; i < len(nodes) && i < start+rowsH; i++ {
		n := nodes[i]
		cursor := " "
		if i == p.sel {
			cursor = ui.CursorStyle.Render("❯")
		}
		lines = append(lines, cursor+" "+ui.PadRight(ui.SpaceAfterFlag(n.Name), max0(p.leftW-15-latCellW(p.leftW)))+
			ui.HelpStyle.Render(ui.PadRight(n.Protocol, 8))+
			latencyCell(p.lat, n.ID, latCellW(p.leftW)))
	}
	if p.busy {
		lines = append(lines, ui.HelpStyle.Render("⏳ 操作进行中…"))
	}
	return lines
}

// leftClick selects the row-th displayed node, mirroring leftLines' prefix
// and window math so the click lands on the row the user saw.
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
	start := 0
	if p.sel >= rowsH {
		start = p.sel - rowsH + 1
	}
	i := start + row
	if i < 0 || i >= len(nodes) || i == p.sel {
		return
	}
	p.sel = i
	p.expanded = false
	p.focus = 0
}

func (p nodesPage) rightLines() []string {
	if p.mode != 0 {
		return p.modalLines()
	}
	n := p.cur()
	if n == nil {
		return []string{ui.HelpStyle.Render("（无节点）")}
	}
	var report []string
	if p.importOK > 0 || len(p.importFail) > 0 {
		report = append(report, ui.SelectedStyle.Render("上次导入 ")+
			ui.HelpStyle.Render(fmt.Sprintf("%d 成功 / %d 失败", p.importOK, len(p.importFail))))
		for _, r := range p.importFail {
			report = append(report, ui.ErrorStyle.Render(" ✗ "+
				ui.Truncate(r.Link, max0(p.rightW-24))+" — "+ui.Truncate(r.Error, 12)))
		}
		report = append(report, "")
	}
	lines := []string{
		ui.SelectedStyle.Render("名称  ") + ui.SpaceAfterFlag(n.Name),
		ui.SelectedStyle.Render("协议  ") + n.Protocol,
		ui.SelectedStyle.Render("地址  ") + ui.Truncate(n.Address, max0(p.rightW-8)),
	}
	if n.Tag != "" {
		lines = append(lines, ui.SelectedStyle.Render("标签  ")+n.Tag)
	}
	if n.Link != "" {
		lines = append(lines, ui.SelectedStyle.Render("链接  ")+ui.Truncate(n.Link, max0(p.rightW-8)))
	}
	if l, ok := p.lat[n.ID]; ok && !l.TestedAt.IsZero() {
		v := "超时"
		bar := ""
		if l.Alive && l.Ms > 0 {
			v = strconv.Itoa(l.Ms) + "ms"
			bar = ui.LatencyStyle(l.Ms, l.Alive, true).Render(ui.LatencyBar(l.Ms))
		}
		lines = append(lines, ui.SelectedStyle.Render("延迟  ")+v+"  "+bar)
	}
	// Trend: the accumulated samples tell "slowing down" from "one bad
	// probe", which a single number cannot.
	if p.hist != nil {
		if series := p.hist.series(n.ID); len(series) >= 2 {
			w := max0(p.rightW - 12)
			if w > 48 {
				w = 48
			}
			lines = append(lines, ui.SelectedStyle.Render("趋势  ")+
				ui.Sparkline(series, w, 1, lipgloss.NewStyle().Foreground(ui.Green), ""))
		}
	}
	lines = append(lines, "",
		ui.HelpStyle.Render(" e 编辑   y 复制链接   G 加入群组   x 删除   T 单节点测速"))
	return append(report, lines...)
}
