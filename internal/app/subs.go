package app

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"dae-tui/internal/driver"
	"dae-tui/internal/ui"
)

// subsPage: master-detail. Left lists subscriptions; the right pane shows
// the selected subscription's metadata and its full node list (fetched on
// selection, cached), with a node cursor for latency testing.
type subsPage struct {
	subs     []driver.Subscription
	sel      int
	focus    int  // 0 left, 1 right (node list)
	expanded bool // fetch/show nodes only after expanding

	subNodes map[string][]driver.Node
	loading  string // subID being fetched
	subErr   map[string]error
	// stale lists subscriptions whose nodes were just re-fetched by the
	// backend (an `u` update). Their cache entry is dropped when the reloaded
	// list arrives, instead of throwing away every subscription's nodes.
	stale map[string]bool
	nc    int // node cursor in right pane

	nodeView // filter/sort for the right pane's node list

	err  error
	busy bool

	mode      int // 0 list, 1 add form, 2 delete confirm, 3 cron edit, 4 edit form
	link      textinput.Model
	tag       textinput.Model
	ifld      int
	cronInput textinput.Model
	cronOn    bool

	lat map[string]driver.Latency

	caps driver.Caps

	// latency test polling state
	testing   bool
	testIDs   []string
	testStart time.Time
	baseline  map[string]time.Time

	leftW, rightW, height int
}

func newSubsPage(caps driver.Caps) subsPage {
	l := textinput.New()
	l.Placeholder = "https://example.com/sub 或 data:… 链接"
	l.CharLimit = 2048
	l.Width = 48
	t := textinput.New()
	t.Placeholder = "标签 (可空)"
	t.CharLimit = 64
	t.Width = 32
	c := textinput.New()
	c.Placeholder = "cron 表达式，如 0 */6 * * *"
	c.CharLimit = 64
	c.Width = 32
	return subsPage{
		link: l, tag: t, cronInput: c,
		subNodes: map[string][]driver.Node{},
		subErr:   map[string]error{},
		stale:    map[string]bool{},
		lat:      map[string]driver.Latency{},
		baseline: map[string]time.Time{},
		nodeView: newNodeView(),
		caps:     caps,
	}
}

func (p *subsPage) setSize(leftW, rightW, h int) {
	p.leftW, p.rightW, p.height = leftW, rightW, h
}

func (p *subsPage) handleSubs(subs []driver.Subscription, err error) {
	if err != nil {
		p.err = err
		return
	}
	p.err = nil
	p.busy = false
	p.subs = subs
	// Keep the per-subscription node caches: editing a tag or a cron
	// expression reloads this list too, and dropping every entry would force
	// a full re-fetch of each expanded subscription for a change that
	// touched no nodes at all. Only entries the backend actually refreshed
	// (an `u` update) or that no longer exist are dropped.
	live := make(map[string]bool, len(subs))
	for _, s := range subs {
		live[s.ID] = true
	}
	for id := range p.subNodes {
		if !live[id] || p.stale[id] {
			delete(p.subNodes, id)
			delete(p.subErr, id)
		}
	}
	p.stale = map[string]bool{}
	if p.sel >= len(subs) {
		p.sel = max0(len(subs) - 1)
	}
	// The node list under the cursor may have shrunk; keep the cursor valid
	// without yanking it back to the top on every reload.
	if p.nc >= len(p.visibleNodes()) {
		p.nc = max0(len(p.visibleNodes()) - 1)
	}
}

func (p *subsPage) handleSubNodes(subID string, nodes []driver.Node, err error) {
	if p.loading == subID {
		p.loading = ""
	}
	if err != nil {
		p.subErr[subID] = err
		return
	}
	p.subErr[subID] = nil
	p.subNodes[subID] = nodes
}

func (p *subsPage) handleLatencies(lats []driver.Latency) {
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
func (p *subsPage) testProgress() (done, total int) {
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

func (p *subsPage) cur() *driver.Subscription {
	if p.sel < 0 || p.sel >= len(p.subs) {
		return nil
	}
	return &p.subs[p.sel]
}

// ensureNodes returns a fetch command when the selected subscription's
// nodes are not cached yet (or were just invalidated by an update).
func (p *subsPage) ensureNodes(d driver.Driver) tea.Cmd {
	s := p.cur()
	if s == nil || !p.expanded {
		return nil
	}
	if _, ok := p.subNodes[s.ID]; ok || p.loading == s.ID {
		return nil
	}
	p.loading = s.ID
	return loadSubNodesCmd(d, s.ID)
}

func (p *subsPage) handleKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	if p.mode == 1 {
		return p.addFormKey(msg, d)
	}
	if p.mode == 4 {
		return p.editFormKey(msg, d)
	}
	if p.mode == 2 {
		switch msg.String() {
		case "y":
			s := p.cur()
			if s == nil {
				p.mode = 0
				return nil
			}
			p.mode = 0
			return subMutateCmd(d, subMutation{kind: 2, ids: []string{s.ID}}, "删除订阅")
		case "n", "esc", "enter":
			p.mode = 0
		}
		return nil
	}
	if p.mode == 3 {
		return p.cronKey(msg, d)
	}

	// The node filter box swallows every key while it is open, like any
	// other modal on this page.
	if cmd, consumed := p.nodeView.handleKey(msg); consumed {
		p.clampNodeCursor()
		return cmd
	}

	// Global to this page.
	switch msg.String() {
	case "u":
		if !p.caps.Subscriptions {
			return unsupportedCmd("更新订阅")
		}
		if s := p.cur(); s != nil {
			p.busy = true
			// The backend re-fetches the subscription's nodes; drop the
			// cached list when the reloaded subscriptions arrive.
			p.stale[s.ID] = true
			return subMutateCmd(d, subMutation{kind: 1, id: s.ID}, "更新订阅 "+s.Tag)
		}
	case "c":
		if !p.caps.Subscriptions {
			return unsupportedCmd("定时刷新")
		}
		if s := p.cur(); s != nil && p.focus == 0 {
			p.mode = 3
			p.ifld = 0
			p.cronInput.SetValue(s.CronExp)
			p.cronOn = s.CronEnable
			p.cronInput.Focus()
			return textinput.Blink
		}
	case "n":
		if !p.caps.Subscriptions {
			return unsupportedCmd("新增订阅")
		}
		p.mode = 1
		p.ifld = 0
		p.link.SetValue("")
		p.tag.SetValue("")
		p.link.Focus()
		p.tag.Blur()
		return textinput.Blink
	case "y":
		if s := p.cur(); s != nil && s.Link != "" {
			return osc52CopyCmd(s.Link)
		}
	}

	if p.focus == 0 {
		switch msg.String() {
		case "j", "down":
			if p.sel < len(p.subs)-1 {
				p.sel++
				p.nc = 0
				p.expanded = false
			}
		case "k", "up":
			if p.sel > 0 {
				p.sel--
				p.nc = 0
				p.expanded = false
			}
		case "g":
			p.sel = 0
			p.nc = 0
			p.expanded = false
		case "G":
			p.sel = len(p.subs) - 1
			p.nc = 0
			p.expanded = false
		case "tab", "l", "right", "enter":
			if s := p.cur(); s != nil {
				p.focus = 1
				p.expanded = true
				return p.ensureNodes(d)
			}
		case "x":
			if !p.caps.Subscriptions {
				return unsupportedCmd("删除订阅")
			}
			if p.cur() != nil {
				p.mode = 2
			}
		case "e":
			if !p.caps.Subscriptions {
				return unsupportedCmd("编辑订阅")
			}
			if s := p.cur(); s != nil {
				return p.openEdit(s)
			}
		}
		return nil
	}

	// `t` probes exactly what is on screen: the filtered, sorted view.
	nodes := p.visibleNodes()
	switch msg.String() {
	case "e":
		if !p.caps.Subscriptions {
			return unsupportedCmd("编辑订阅")
		}
		if s := p.cur(); s != nil {
			return p.openEdit(s)
		}
	case "j", "down":
		if p.nc < len(nodes)-1 {
			p.nc++
		}
	case "k", "up":
		if p.nc > 0 {
			p.nc--
		}
	case "g":
		p.nc = 0
	case "G":
		p.nc = len(nodes) - 1
	case "tab", "h", "left", "esc":
		p.focus = 0
		p.expanded = false
	case "t":
		if !p.caps.TestLatency {
			return unsupportedCmd("测速")
		}
		ids := make([]string, 0, len(nodes))
		for i, n := range nodes {
			if i >= 500 {
				break
			}
			ids = append(ids, n.ID)
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
	return nil
}

func (p *subsPage) cronKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	switch msg.String() {
	case "esc":
		p.mode = 0
		p.cronInput.Blur()
		return nil
	case "tab", "shift+tab":
		if p.ifld == 0 {
			p.ifld = 1
			p.cronInput.Blur()
		} else {
			p.ifld = 0
			p.cronInput.Focus()
			return textinput.Blink
		}
		return nil
	case " ", "j", "k": // bubbletea reports the space key as " ", not "space"
		if p.ifld == 1 {
			p.cronOn = !p.cronOn
			return nil
		}
	case "enter":
		if p.ifld == 0 {
			// jump to the toggle instead of submitting immediately
			p.ifld = 1
			p.cronInput.Blur()
			return nil
		}
		exp := strings.TrimSpace(p.cronInput.Value())
		s := p.cur()
		p.mode = 0
		p.cronInput.Blur()
		if s == nil || exp == "" {
			return nil
		}
		p.busy = true
		return subMutateCmd(d, subMutation{kind: 3, id: s.ID, cronExp: exp, cronOn: p.cronOn},
			"更新 "+s.Tag+" 定时刷新 ("+onOff(p.cronOn)+")")
	}
	if p.ifld == 0 {
		var cmd tea.Cmd
		p.cronInput, cmd = p.cronInput.Update(msg)
		return cmd
	}
	return nil
}

func (p *subsPage) curNodes() []driver.Node {
	if s := p.cur(); s != nil {
		return p.subNodes[s.ID]
	}
	return nil
}

// visibleNodes is the subscription's node list as the filter and sort
// currently present it.
func (p *subsPage) visibleNodes() []driver.Node {
	return p.nodeView.visible(p.curNodes(), p.lat)
}

// visibleLatencyIDs lists the nodes whose latency the right pane renders; a
// collapsed subscription shows none.
func (p *subsPage) visibleLatencyIDs() []string {
	if !p.expanded {
		return nil
	}
	nodes := p.visibleNodes()
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	return ids
}

// clampNodeCursor keeps the node cursor inside the filtered list, which can
// shrink under it while the user types.
func (p *subsPage) clampNodeCursor() {
	if n := len(p.visibleNodes()); p.nc >= n {
		p.nc = max0(n - 1)
	}
}

func (p *subsPage) addFormKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	switch msg.String() {
	case "esc":
		p.mode = 0
		return nil
	case "tab", "shift+tab":
		if p.ifld == 0 {
			p.ifld = 1
			p.link.Blur()
			p.tag.Focus()
		} else {
			p.ifld = 0
			p.tag.Blur()
			p.link.Focus()
		}
		return nil
	case "enter":
		link := strings.TrimSpace(p.link.Value())
		if link == "" {
			return nil
		}
		tag := strings.TrimSpace(p.tag.Value())
		p.mode = 0
		p.busy = true
		return subMutateCmd(d, subMutation{kind: 0, link: link, tag: tag}, "新增订阅")
	}
	var cmd tea.Cmd
	if p.ifld == 0 {
		p.link, cmd = p.link.Update(msg)
	} else {
		p.tag, cmd = p.tag.Update(msg)
	}
	return cmd
}

// openEdit prefills the edit form with the subscription's tag and link.
// Editing in place keeps the subscription ID, which is what groups attach
// to — removing and re-adding mints a new ID and silently detaches the
// subscription from every group.
func (p *subsPage) openEdit(s *driver.Subscription) tea.Cmd {
	p.mode = 4
	p.ifld = 0
	p.tag.SetValue(s.Tag)
	p.link.SetValue(s.Link)
	p.tag.Focus()
	p.link.Blur()
	return textinput.Blink
}

func (p *subsPage) editFormKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
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
		s := p.cur()
		if s == nil {
			p.mode = 0
			return nil
		}
		tag := strings.TrimSpace(p.tag.Value())
		link := strings.TrimSpace(p.link.Value())
		p.mode = 0
		p.busy = true
		m := subMutation{kind: 4, id: s.ID, tag: tag, link: link}
		if link != "" && link != s.Link {
			m.doLink = true
		}
		return subMutateCmd(d, m, "编辑订阅 "+s.Tag)
	}
	var cmd tea.Cmd
	if p.ifld == 0 {
		p.tag, cmd = p.tag.Update(msg)
	} else {
		p.link, cmd = p.link.Update(msg)
	}
	return cmd
}

func (p subsPage) View() string {
	rvTitle := " 详情 "
	if s := p.cur(); s != nil && p.expanded {
		rvTitle = " 节点" + p.nodeView.countTitle(len(p.visibleNodes()), len(p.subNodes[s.ID])) +
			p.nodeView.sortTitle() + " "
	}
	lv := ui.Pane(" 订阅 ("+strconv.Itoa(len(p.subs))+") ", p.focus == 0, p.leftW, p.height, p.leftLines())
	rv := ui.Pane(rvTitle, p.focus == 1, p.rightW, p.height, p.rightLines())
	body := lipgloss.JoinHorizontal(lipgloss.Top, lv, " ", rv)

	if p.mode == 1 {
		var f strings.Builder
		f.WriteString(ui.TitleStyle.Render(" 新增订阅") + "\n\n")
		f.WriteString(" 链接  " + p.link.View() + "\n")
		f.WriteString(" 标签  " + p.tag.View() + "\n\n")
		f.WriteString(ui.HelpStyle.Render(" Tab 切换字段  Enter 提交  esc 取消"))
		form := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(ui.Accent).Padding(1, 2).Render(f.String())
		body += "\n" + form
	}
	if p.mode == 4 {
		if s := p.cur(); s != nil {
			var f strings.Builder
			f.WriteString(ui.TitleStyle.Render(" 编辑订阅 · "+s.Tag) + "\n\n")
			f.WriteString(" 标签  " + p.tag.View() + "\n")
			f.WriteString(" 链接  " + p.link.View() + "\n\n")
			f.WriteString(ui.HelpStyle.Render(" 改链接不会重新拉取节点（u 才会）") + "\n")
			f.WriteString(ui.HelpStyle.Render(" Tab 切换字段  Enter 提交  esc 取消"))
			body += "\n" + lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
				BorderForeground(ui.Accent).Padding(1, 2).Render(f.String())
		}
	}
	if p.mode == 2 {
		if s := p.cur(); s != nil {
			body += "\n" + ui.ErrorStyle.Render(" 确认删除订阅 \""+s.Tag+"\"? (y/n)")
		}
	}
	if p.mode == 3 {
		if s := p.cur(); s != nil {
			on := ui.ErrorStyle.Render("停用")
			if p.cronOn {
				on = ui.OKStyle.Render("启用")
			}
			cursor := "  "
			if p.ifld == 1 {
				cursor = ui.CursorStyle.Render("❯ ")
			}
			var f strings.Builder
			f.WriteString(ui.TitleStyle.Render(" 定时刷新 "+s.Tag) + "\n\n")
			f.WriteString(" 表达式  " + p.cronInput.View() + "\n")
			f.WriteString(" 启用    " + cursor + on + ui.HelpStyle.Render("  (space 切换)") + "\n\n")
			f.WriteString(ui.HelpStyle.Render(" Tab 切换  space 开关  Enter 提交  esc 取消"))
			body += "\n" + lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
				BorderForeground(ui.Accent).Padding(1, 2).Render(f.String())
		}
	}
	return body
}

func (p subsPage) leftLines() []string {
	var lines []string
	if !p.caps.Subscriptions {
		lines = append(lines, ui.ErrorStyle.Render("✗ 当前后端不支持订阅管理"))
	}
	if p.err != nil {
		lines = append(lines, ui.ErrorStyle.Render("✗ "+shortErr(p.err)))
	}
	if len(p.subs) == 0 {
		lines = append(lines, ui.HelpStyle.Render("无订阅（按 n 添加）"))
	}
	rowsH := max0(p.height - 2)
	start := 0
	if p.sel >= rowsH {
		start = p.sel - rowsH + 1
	}
	for i := start; i < len(p.subs) && i < start+rowsH; i++ {
		s := p.subs[i]
		cursor := " "
		if i == p.sel {
			cursor = ui.CursorStyle.Render("❯")
		}
		// A bare status string ("failed") never says why; the backend's
		// info field does, so surface its first line under failed rows.
		failed := s.Status == "" || containsFold(s.Status, "fail") || containsFold(s.Status, "error")
		stStyle := ui.OKStyle
		if failed {
			stStyle = ui.ErrorStyle
		}
		lines = append(lines, cursor+" "+ui.PadRight(s.Tag, max0(p.leftW-24))+
			stStyle.Render(ui.Truncate(s.Status, 10))+
			ui.HelpStyle.Render(" "+strconv.Itoa(s.NodeCount)+"节点"))
		if failed && s.Info != "" {
			lines = append(lines, "    "+ui.ErrorStyle.Render(
				ui.Truncate(firstLine(s.Info), max0(p.leftW-6))))
		}
	}
	if p.busy {
		lines = append(lines, ui.HelpStyle.Render("⏳ 操作进行中…"))
	}
	return lines
}

func (p subsPage) rightLines() []string {
	s := p.cur()
	if s == nil {
		return []string{ui.HelpStyle.Render("（无订阅）")}
	}
	var head []string
	cron := ui.HelpStyle.Render("   cron ") + onOff(s.CronEnable)
	if s.CronExp != "" {
		cron += ui.HelpStyle.Render(" (" + s.CronExp + ")")
	}
	head = append(head, ui.SelectedStyle.Render("标签 ")+s.Tag+cron)
	head = append(head, ui.SelectedStyle.Render("状态 ")+s.Status)
	head = append(head, ui.SelectedStyle.Render("链接 ")+ui.Truncate(s.Link, max0(p.rightW-8)))
	if s.Info != "" {
		head = append(head, ui.SelectedStyle.Render("信息 ")+ui.Truncate(firstLine(s.Info), max0(p.rightW-8)))
	}
	head = append(head, ui.SelectedStyle.Render("更新 ")+ui.TimeAgo(s.UpdatedAt))
	if p.focus == 0 {
		head = append(head, ui.HelpStyle.Render(" u 更新  e 编辑标签/链接  x 删除  c 定时刷新"))
	}
	head = append(head, "")

	if !p.expanded {
		return append(head, ui.SelectedStyle.Render("按 Tab/l/Enter 展开查看全部节点"))
	}
	if err := p.subErr[s.ID]; err != nil {
		return append(head, ui.ErrorStyle.Render("✗ 拉取节点失败: "+shortErr(err)))
	}
	all := p.subNodes[s.ID]
	if p.loading == s.ID && all == nil {
		return append(head, ui.HelpStyle.Render(" 拉取节点中…"))
	}
	if len(all) == 0 {
		return append(head, ui.HelpStyle.Render(" 无节点（u 更新订阅后重试）"))
	}
	if prompt := p.nodeView.prompt(); prompt != "" {
		head = append(head, ui.HelpStyle.Render(prompt))
	}
	nodes := p.visibleNodes()
	if len(nodes) == 0 {
		return append(head, ui.ErrorStyle.Render(" 没有匹配的节点（/ 重新编辑，框内 esc 清空）"))
	}

	rowsH := max0(p.height - len(head) - 3)
	start := 0
	if p.nc >= rowsH {
		start = p.nc - rowsH + 1
	}
	for i := start; i < len(nodes) && i < start+rowsH; i++ {
		n := nodes[i]
		cursor := " "
		if i == p.nc && p.focus == 1 {
			cursor = ui.CursorStyle.Render("❯")
		}
		latStr, latStyle := "-", ui.LatencyStyle(0, false, false)
		if l, ok := p.lat[n.ID]; ok && !l.TestedAt.IsZero() {
			if l.Alive && l.Ms > 0 {
				latStr = strconv.Itoa(l.Ms) + "ms"
			} else if !l.Alive {
				latStr = "dead"
			}
			latStyle = ui.LatencyStyle(l.Ms, l.Alive, true)
		}
		nameW := max0(p.rightW - 24)
		head = append(head, cursor+" "+ui.PadRight(ui.SpaceAfterFlag(n.Name), nameW)+
			ui.HelpStyle.Render(ui.PadRight(n.Protocol, 8))+
			latStyle.Render(ui.PadLeft(latStr, 9)))
	}
	return head
}

func onOff(b bool) string {
	if b {
		return "开"
	}
	return "关"
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}
