package app

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"dae-tui/internal/driver"
	"dae-tui/internal/i18n"
	"dae-tui/internal/keymap"
	"dae-tui/internal/ui"
)

type subsPage struct {
	subs  []driver.Subscription
	sel   int
	focus int

	subNodes map[string][]driver.Node
	loading  string
	subErr   map[string]error

	stale map[string]bool
	nc    int

	nodeView

	err  error
	busy bool

	mode      int
	link      textinput.Model
	tag       textinput.Model
	ifld      int
	cronInput textinput.Model
	cronOn    bool
	// opID is the subscription the open modal (delete confirm, cron form,
	// edit form) targets, pinned for the same reason as nodesPage.opID: a
	// refresh can move p.sel under an open modal, and the submit must act
	// on the subscription the user opened it for.
	opID string

	lat map[string]driver.Latency

	caps driver.Caps

	testing   bool
	testIDs   []string
	testStart time.Time
	baseline  map[string]time.Time

	leftW, rightW, height int
}

func newSubsPage(caps driver.Caps) subsPage {
	l := textinput.New()
	l.CharLimit = 2048
	l.Width = 48
	t := textinput.New()
	t.CharLimit = 64
	t.Width = 32
	c := textinput.New()
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

	p.busy = false
	if err != nil {
		p.err = err
		return
	}
	p.err = nil
	p.subs = subs

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

func (p *subsPage) handleLatencies(lats []driver.Latency, err error) {
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

func (p *subsPage) ensureNodes(d driver.Driver) tea.Cmd {
	s := p.cur()
	if s == nil {
		return nil
	}
	if _, ok := p.subNodes[s.ID]; ok || p.loading == s.ID {
		return nil
	}
	p.loading = s.ID
	return loadSubNodesCmd(d, s.ID)
}

const testIDLimit = 500

func (p *subsPage) startTest(d driver.Driver, ids []string) tea.Cmd {
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

func (p *subsPage) visibleNodeIDs(limit int) []string {
	nodes := p.visibleNodes()
	ids := make([]string, 0, len(nodes))
	for i, n := range nodes {
		if i >= limit {
			break
		}
		ids = append(ids, n.ID)
	}
	return ids
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
			s := p.opSub()
			if s == nil {
				p.mode = 0
				return opGone(i18n.T("删除订阅"))
			}
			p.mode = 0
			return subMutateCmd(d, subMutation{kind: 2, ids: []string{s.ID}}, i18n.T("删除订阅"))
		case "n", "esc", "enter":
			p.mode = 0
		}
		return nil
	}
	if p.mode == 3 {
		return p.cronKey(msg, d)
	}

	if cmd, consumed := p.nodeView.handleKey(msg, keymap.Subs); consumed {
		p.clampNodeCursor()
		return cmd
	}

	// Remap layer: the forms and the filter input above stay raw; the
	// action switch below dispatches through the translated key.
	key := tk(keymap.Subs, msg.String())

	switch key {
	case "u":
		if !p.caps.Subscriptions {
			return unsupportedCmd(i18n.T("更新订阅"))
		}
		// Busy guard: pressing u again mid-update would fire a second
		// server-side refresh for the same subscription.
		if p.busy {
			return nil
		}
		if s := p.cur(); s != nil {
			p.busy = true

			p.stale[s.ID] = true
			return subMutateCmd(d, subMutation{kind: 1, id: s.ID}, i18n.T("更新订阅 ")+s.Tag)
		}
	case "c":
		if !p.caps.Subscriptions {
			return unsupportedCmd(i18n.T("定时刷新"))
		}
		if s := p.cur(); s != nil && p.focus == 0 {
			p.mode = 3
			p.ifld = 0
			p.opID = s.ID
			p.cronInput.SetValue(s.CronExp)
			p.cronOn = s.CronEnable
			p.cronInput.Focus()
			return textinput.Blink
		}
	case "n":
		if !p.caps.Subscriptions {
			return unsupportedCmd(i18n.T("新增订阅"))
		}
		p.mode = 1
		p.ifld = 0
		p.link.SetValue("")
		p.tag.SetValue("")
		p.link.Focus()
		p.tag.Blur()
		return textinput.Blink
	case "y":

		if p.focus == 1 {
			if n := p.selectedNode(); n != nil && n.Link != "" {
				return osc52CopyCmd(n.Link, i18n.T("节点链接"))
			}
		}
		if s := p.cur(); s != nil && s.Link != "" {
			return osc52CopyCmd(s.Link, i18n.T("订阅链接"))
		}
	case "t":

		if !p.caps.TestLatency {
			return unsupportedCmd(i18n.T("测速"))
		}
		return p.startTest(d, p.visibleNodeIDs(testIDLimit))
	case "T":

		if !p.caps.TestLatency {
			return unsupportedCmd(i18n.T("测速"))
		}
		if n := p.selectedNode(); n != nil {
			return p.startTest(d, []string{n.ID})
		}
	}

	if p.focus == 0 {
		switch key {
		case "j", "down":
			if p.sel < len(p.subs)-1 {
				p.sel++
				p.nc = 0
				return p.ensureNodes(d)
			}
		case "k", "up":
			if p.sel > 0 {
				p.sel--
				p.nc = 0
				return p.ensureNodes(d)
			}
		case "g":
			p.sel = 0
			p.nc = 0
			return p.ensureNodes(d)
		case "G":
			p.sel = len(p.subs) - 1
			p.nc = 0
			return p.ensureNodes(d)
		case "tab", "l", "right", "enter":

			if s := p.cur(); s != nil {
				p.focus = 1
				return p.ensureNodes(d)
			}
		case "x":
			if !p.caps.Subscriptions {
				return unsupportedCmd(i18n.T("删除订阅"))
			}
			if s := p.cur(); s != nil {
				p.mode = 2
				p.opID = s.ID
			}
		case "e":
			if !p.caps.Subscriptions {
				return unsupportedCmd(i18n.T("编辑订阅"))
			}
			if s := p.cur(); s != nil {
				return p.openEdit(s)
			}
		}
		return nil
	}

	nodes := p.visibleNodes()
	switch key {
	case "e":
		if !p.caps.Subscriptions {
			return unsupportedCmd(i18n.T("编辑订阅"))
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
	case " ":

		if p.ifld == 1 {
			p.cronOn = !p.cronOn
			return nil
		}
	case "enter":
		if p.ifld == 0 {

			p.ifld = 1
			p.cronInput.Blur()
			return nil
		}
		exp := strings.TrimSpace(p.cronInput.Value())
		s := p.opSub()
		p.mode = 0
		p.cronInput.Blur()
		if s == nil {
			return opGone(i18n.T("定时刷新"))
		}
		if exp == "" {
			return nil
		}
		p.busy = true
		return subMutateCmd(d, subMutation{kind: 3, id: s.ID, cronExp: exp, cronOn: p.cronOn},
			i18n.T("更新 ")+s.Tag+i18n.T(" 定时刷新 (")+onOff(p.cronOn)+")")
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

func (p subsPage) selectedNode() *driver.Node {
	if p.mode != 0 {
		return nil
	}
	nodes := p.visibleNodes()
	if p.nc < 0 || p.nc >= len(nodes) {
		return nil
	}
	n := nodes[p.nc]
	return &n
}

func (p subsPage) showNodeInfo() bool {
	return p.focus == 1 && p.selectedNode() != nil
}

func (p subsPage) nodeInfoLines(n *driver.Node) []string {
	lines := []string{
		detailLabel("名称") + ui.SpaceAfterFlag(n.Name),
		detailLabel("协议") + n.Protocol,
		detailLabel("地址") + ui.Truncate(n.Address, max0(p.rightW-10)),
	}
	if n.Link != "" {
		lines = append(lines, detailLabel("链接")+ui.Truncate(n.Link, max0(p.rightW-10)))
	}
	if row, ok := latencyDetail(p.lat, n.ID); ok {
		lines = append(lines, detailLabel("延迟")+row)
	}
	return lines
}

func (p subsPage) infoBoxLen() int {
	n := len(p.infoLines())
	if sel := p.selectedNode(); sel != nil {
		if m := len(p.nodeInfoLines(sel)); m > n {
			n = m
		}
	}
	return n
}

func (p *subsPage) visibleNodes() []driver.Node {
	return p.nodeView.visible(p.curNodes(), p.lat)
}

func (p *subsPage) visibleLatencyIDs() []string {
	nodes := p.visibleNodes()
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	return ids
}

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
		return subMutateCmd(d, subMutation{kind: 0, link: link, tag: tag}, i18n.T("新增订阅"))
	}
	var cmd tea.Cmd
	if p.ifld == 0 {
		p.link, cmd = p.link.Update(msg)
	} else {
		p.tag, cmd = p.tag.Update(msg)
	}
	return cmd
}

func (p *subsPage) openEdit(s *driver.Subscription) tea.Cmd {
	p.mode = 4
	p.ifld = 0
	p.opID = s.ID
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
		s := p.opSub()
		if s == nil {
			p.mode = 0
			return opGone(i18n.T("编辑订阅"))
		}
		tag := strings.TrimSpace(p.tag.Value())
		link := strings.TrimSpace(p.link.Value())
		p.mode = 0
		p.busy = true
		m := subMutation{kind: 4, id: s.ID, tag: tag, link: link}
		if link != "" && link != s.Link {
			m.doLink = true
		}
		return subMutateCmd(d, m, i18n.T("编辑订阅 ")+s.Tag)
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

	info := p.infoLines()
	topTitle := i18n.T("订阅")
	if s := p.cur(); s != nil && s.Tag != "" {
		topTitle = s.Tag
	}
	if sel := p.selectedNode(); p.showNodeInfo() {
		info, topTitle = p.nodeInfoLines(sel), ui.SpaceAfterFlag(sel.Name)
	}
	topH, bottomInner := stackedDetail(p.infoBoxLen(), p.height)
	body := p.bodyLines(bottomInner)
	bottomTitle := i18n.T("节点")
	if s := p.cur(); s != nil {
		if p.mode == 2 {
			bottomTitle = i18n.T("删除确认")
		} else if all := p.subNodes[s.ID]; all != nil {
			bottomTitle += p.nodeView.countTitle(len(p.visibleNodes()), len(all)) +
				p.nodeView.sortTitle()
		}
	}

	rightFooter := K(keymap.Subs, "t") + "/" + K(keymap.Subs, "T") + " " + i18n.T("测速") +
		" · " + kb(keymap.Subs, "y", "复制") + " · / " + i18n.T("过滤") + " · " + kb(keymap.Subs, "o", "排序")
	if p.mode == 2 {
		rightFooter = i18n.T("y 确认 · n/esc 取消")
	}
	return strings.Join(ui.PaneRowColumn(
		ui.PaneSpec{Title: i18n.T("订阅 (") + strconv.Itoa(len(p.subs)) + ")",
			Footer: i18n.T("Tab 切栏 · ") + kb(keymap.Subs, "c", "定时刷新") + " · " + kb(keymap.Subs, "x", "删除"), Lines: p.leftLines(),
			Focused: p.focus == 0, W: p.leftW, H: p.height},
		ui.PaneSpec{Title: topTitle, Lines: info, W: p.rightW, H: topH},
		ui.PaneSpec{Title: bottomTitle, Footer: rightFooter, Lines: body,
			Focused: p.focus == 1, W: p.rightW},
	), "\n")
}

func (p subsPage) overlay() *overlaySpec {
	// Placeholders re-translate here rather than living in the constructor:
	// the inputs outlive a settings-window language switch.
	p.link.Placeholder = i18n.T("https://example.com/sub 或 data:… 链接")
	p.tag.Placeholder = i18n.T("标签 (可空)")
	p.cronInput.Placeholder = i18n.T("cron 表达式，如 0 */6 * * *")
	switch p.mode {
	case 1:
		return &overlaySpec{lines: []string{
			ui.TitleStyle.Render(i18n.T(" 新增订阅")),
			"",
			i18n.T(" 链接  ") + p.link.View(),
			i18n.T(" 标签  ") + p.tag.View(),
			"",
			ui.HelpStyle.Render(i18n.T(" Tab 切换字段  Enter 提交  esc 取消")),
		}}
	case 3:
		if s := p.opSub(); s != nil {
			on := ui.ErrorStyle.Render(i18n.T("停用"))
			if p.cronOn {
				on = ui.OKStyle.Render(i18n.T("启用"))
			}
			cursor := "  "
			if p.ifld == 1 {
				cursor = ui.CursorStyle.Render("❯ ")
			}
			return &overlaySpec{lines: []string{
				ui.TitleStyle.Render(i18n.T(" 定时刷新 ") + s.Tag),
				"",
				i18n.T(" 表达式  ") + p.cronInput.View(),
				i18n.T(" 启用    ") + cursor + on + ui.HelpStyle.Render(i18n.T("  (space 切换)")),
				"",
				ui.HelpStyle.Render(i18n.T(" Tab 切换  space 开关  Enter 提交  esc 取消")),
			}}
		}
	case 4:
		if s := p.opSub(); s != nil {
			return &overlaySpec{lines: []string{
				ui.TitleStyle.Render(i18n.T(" 编辑订阅 · ") + s.Tag),
				"",
				i18n.T(" 标签  ") + p.tag.View(),
				i18n.T(" 链接  ") + p.link.View(),
				"",
				ui.HelpStyle.Render(i18n.T(" 改链接不会重新拉取节点（u 才会）")),
				ui.HelpStyle.Render(i18n.T(" Tab 切换字段  Enter 提交  esc 取消")),
			}}
		}
	}
	return nil
}

func (p subsPage) modalLines() []string {
	if s := p.opSub(); s != nil {
		return ui.BoxLines(true, i18n.T("确认删除订阅 \"")+s.Tag+"\"? (y/n)")
	}
	return []string{ui.HelpStyle.Render(i18n.T("（无订阅）"))}
}

// opSub resolves the modal's locked target ID against the current
// subscription list.
func (p *subsPage) opSub() *driver.Subscription {
	for i := range p.subs {
		if p.subs[i].ID == p.opID {
			return &p.subs[i]
		}
	}
	return nil
}

func (p subsPage) leftLines() []string {
	var lines []string
	if !p.caps.Subscriptions {
		lines = append(lines, ui.ErrorStyle.Render(i18n.T("✗ 当前后端不支持订阅管理")))
	}
	if p.err != nil {
		lines = append(lines, ui.ErrorStyle.Render("✗ "+shortErr(p.err)))
	}
	if len(p.subs) == 0 {
		lines = append(lines, ui.HelpStyle.Render(centerLine(i18n.T("无订阅（按 n 添加）"), p.leftW-4)))
	}
	rowsH := max0(p.height - 2)
	start := 0
	if p.sel >= rowsH {
		start = p.sel - rowsH + 1
	}
	var list []string
	for i := start; i < len(p.subs) && i < start+rowsH; i++ {
		s := p.subs[i]
		cursor := " "
		if i == p.sel {
			cursor = ui.CursorStyle.Render("❯")
		}

		failed := containsFold(s.Status, "fail") || containsFold(s.Status, "error")
		st, stStyle := s.Status, ui.OKStyle
		switch {
		case failed:
			stStyle = ui.ErrorStyle
		case st == "":
			st, stStyle = "—", ui.HelpStyle
		}
		row := cursor + " " + ui.PadRight(s.Tag, max0(p.leftW-26)) +
			stStyle.Render(ui.Truncate(st, 10)) +
			ui.HelpStyle.Render(" "+i18n.T("%d节点", s.NodeCount))
		list = append(list, ui.HiRow(row, p.leftW-4, i == p.sel))
		if failed && s.Info != "" {
			list = append(list, "    "+ui.ErrorStyle.Render(
				ui.Truncate(firstLine(s.Info), max0(p.leftW-8))))
		}
	}
	return append(lines, ui.WithScrollbar(list, p.leftW-4, len(p.subs), start, p.focus == 0)...)
}

func (p *subsPage) leftClick(row int, d driver.Driver) tea.Cmd {
	prefix := 0
	if !p.caps.Subscriptions {
		prefix++
	}
	if p.err != nil {
		prefix++
	}
	if len(p.subs) == 0 {
		prefix++
	}
	row -= prefix
	if row < 0 {
		return nil
	}
	rowsH := max0(p.height - 2)
	if row >= rowsH {
		return nil
	}
	start := 0
	if p.sel >= rowsH {
		start = p.sel - rowsH + 1
	}

	i := start
	for i < len(p.subs) {
		s := p.subs[i]
		if row == 0 {
			break
		}
		row--
		if (containsFold(s.Status, "fail") || containsFold(s.Status, "error")) && s.Info != "" {
			if row == 0 {
				break
			}
			row--
		}
		i++
	}
	if i >= len(p.subs) || i == p.sel {
		return nil
	}
	p.sel = i
	p.nc = 0
	p.focus = 0
	return p.ensureNodes(d)
}

func (p *subsPage) rightClick(row int) {
	if p.mode != 0 {
		return
	}
	topH, inner := stackedDetail(p.infoBoxLen(), p.height)
	head := 0
	if p.nodeView.prompt() != "" {
		head = 1
	}
	d := row - topH - head
	rowsH := max0(inner - head)
	if d < 0 || d >= rowsH {
		return
	}
	nodes := p.visibleNodes()
	start := 0
	if p.nc >= rowsH {
		start = p.nc - rowsH + 1
	}
	i := start + d
	if i >= 0 && i < len(nodes) {
		p.focus = 1
		p.nc = i
	}
}

func (p subsPage) infoLines() []string {
	s := p.cur()
	if s == nil {
		return []string{ui.HelpStyle.Render(i18n.T("（无订阅）"))}
	}

	failed := containsFold(s.Status, "fail") || containsFold(s.Status, "error")
	status := ui.OKStyle.Render(s.Status)
	switch {
	case failed:
		status = ui.ErrorStyle.Render(s.Status)
	case s.Status == "":
		status = ui.HelpStyle.Render("—")
	}
	cron := onOff(s.CronEnable)
	if s.CronExp != "" {
		cron += ui.HelpStyle.Render(" (" + s.CronExp + ")")
	}
	lines := []string{
		detailLabel("标签") + s.Tag,
		detailLabel("状态") + status,
		detailLabel("定时") + cron,
		detailLabel("链接") + ui.Truncate(s.Link, max0(p.rightW-10)),
	}
	if s.Info != "" {
		lines = append(lines, detailLabel("信息")+ui.Truncate(firstLine(s.Info), max0(p.rightW-10)))
	}
	return append(lines, detailLabel("更新")+ui.TimeAgo(s.UpdatedAt))
}

func (p subsPage) bodyLines(inner int) []string {
	if p.mode == 2 {
		return p.modalLines()
	}
	s := p.cur()
	if s == nil {
		return []string{ui.HelpStyle.Render(i18n.T("（无订阅）"))}
	}
	if err := p.subErr[s.ID]; err != nil {
		return []string{ui.ErrorStyle.Render(i18n.T("✗ 拉取节点失败: ") + shortErr(err))}
	}
	all := p.subNodes[s.ID]
	if p.loading == s.ID && all == nil {
		return []string{ui.HelpStyle.Render(i18n.T(" 拉取节点中…"))}
	}
	if len(all) == 0 {
		return []string{ui.HelpStyle.Render(i18n.T(" 无节点（u 更新订阅后重试）"))}
	}
	var lines []string
	if prompt := p.nodeView.prompt(); prompt != "" {
		lines = append(lines, ui.HelpStyle.Render(prompt))
	}
	nodes := p.visibleNodes()
	if len(nodes) == 0 {
		return append(lines, ui.ErrorStyle.Render(i18n.T(" 没有匹配的节点（/ 重新编辑，框内 esc 清空）")))
	}
	rowsH := max0(inner - len(lines))
	start := 0
	if p.nc >= rowsH {
		start = p.nc - rowsH + 1
	}
	var window []string
	for i := start; i < len(nodes) && i < start+rowsH; i++ {
		n := nodes[i]
		cursor := " "
		if i == p.nc && p.focus == 1 {
			cursor = ui.CursorStyle.Render("❯")
		}
		nameW := max0(p.rightW - 17 - latCellW(p.rightW-4))
		row := cursor + " " + ui.PadRight(ui.SpaceAfterFlag(n.Name), nameW) +
			ui.HelpStyle.Render(ui.PadRight(n.Protocol, 8)) +
			latencyCell(p.lat, n.ID, latCellW(p.rightW-4))
		window = append(window, ui.HiRow(row, p.rightW-4, i == p.nc && p.focus == 1))
	}
	return append(lines, ui.WithScrollbar(window, p.rightW-4, len(nodes), start, p.focus == 1)...)
}

func onOff(b bool) string {
	if b {
		return i18n.T("开")
	}
	return i18n.T("关")
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
