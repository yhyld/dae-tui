package app

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"dae-tui/internal/driver"
	"dae-tui/internal/ui"
)

// subsPage: master-detail. Left lists subscriptions; the right column is
// two stacked boxes — the selected subscription's metadata on top, its
// full node list below (fetched on selection, cached), with a node cursor
// for latency testing.
type subsPage struct {
	subs  []driver.Subscription
	sel   int
	focus int // 0 left, 1 right (node list)

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
// nodes are not cached yet (or were just invalidated by an update). The
// node list is always visible, so plain selection already fetches; any
// caller is safe — the cache and loading state decide.
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
	case "t":
		// The node list is always on screen, so `t` probes exactly what it
		// shows — the filtered, sorted view — from either focus.
		if !p.caps.TestLatency {
			return unsupportedCmd("测速")
		}
		nodes := p.visibleNodes()
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

	if p.focus == 0 {
		switch msg.String() {
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
			// The node list is always rendered; this just moves focus.
			if s := p.cur(); s != nil {
				p.focus = 1
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

	// Right-pane navigation over the node list.
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

// visibleLatencyIDs lists the nodes whose latency the right column renders:
// whatever the node list currently shows (nothing before it is fetched).
func (p *subsPage) visibleLatencyIDs() []string {
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
	// The page is a master/detail row whose detail side is two stacked
	// boxes (subscription info on top, node list below), built by
	// PaneRowColumn; the in-pane modal (the delete confirmation) renders in
	// the bottom box — the boxes always fill the page height, so anything
	// appended below them would be pushed off screen.
	info := p.infoLines()
	topH, bottomInner := stackedDetail(len(info), p.height)
	body := p.bodyLines(bottomInner)
	topTitle, bottomTitle := "订阅", "节点"
	if s := p.cur(); s != nil {
		if s.Tag != "" {
			topTitle = s.Tag
		}
		if p.mode == 2 {
			bottomTitle = "删除确认"
		} else if all := p.subNodes[s.ID]; all != nil {
			bottomTitle += p.nodeView.countTitle(len(p.visibleNodes()), len(all)) +
				p.nodeView.sortTitle()
		}
	}
	// Key hints ride the edges of the boxes they belong to: the left box
	// carries the left-focused actions, the bottom box the node-list keys
	// (and the delete confirmation's y/n while it is open); page-wide keys
	// stay on the app frame.
	rightFooter := "t 测速 · / 过滤 · o 排序"
	if p.mode == 2 {
		rightFooter = "y 确认 · n/esc 取消"
	}
	return strings.Join(ui.PaneRowColumn(
		ui.PaneSpec{Title: "订阅 (" + strconv.Itoa(len(p.subs)) + ")",
			Footer: "Tab 切栏 · c 定时刷新 · x 删除", Lines: p.leftLines(),
			Focused: p.focus == 0, W: p.leftW, H: p.height},
		ui.PaneSpec{Title: topTitle, Lines: info, W: p.rightW, H: topH},
		ui.PaneSpec{Title: bottomTitle, Footer: rightFooter, Lines: body,
			Focused: p.focus == 1, W: p.rightW},
	), "\n")
}

// overlay returns the page's floating window: the add / cron / edit forms.
// The delete confirmation stays in the right pane — it belongs next to the
// item it names.
func (p subsPage) overlay() *overlaySpec {
	switch p.mode {
	case 1:
		return &overlaySpec{lines: []string{
			ui.TitleStyle.Render(" 新增订阅"),
			"",
			" 链接  " + p.link.View(),
			" 标签  " + p.tag.View(),
			"",
			ui.HelpStyle.Render(" Tab 切换字段  Enter 提交  esc 取消"),
		}}
	case 3:
		if s := p.cur(); s != nil {
			on := ui.ErrorStyle.Render("停用")
			if p.cronOn {
				on = ui.OKStyle.Render("启用")
			}
			cursor := "  "
			if p.ifld == 1 {
				cursor = ui.CursorStyle.Render("❯ ")
			}
			return &overlaySpec{lines: []string{
				ui.TitleStyle.Render(" 定时刷新 " + s.Tag),
				"",
				" 表达式  " + p.cronInput.View(),
				" 启用    " + cursor + on + ui.HelpStyle.Render("  (space 切换)"),
				"",
				ui.HelpStyle.Render(" Tab 切换  space 开关  Enter 提交  esc 取消"),
			}}
		}
	case 4:
		if s := p.cur(); s != nil {
			return &overlaySpec{lines: []string{
				ui.TitleStyle.Render(" 编辑订阅 · " + s.Tag),
				"",
				" 标签  " + p.tag.View(),
				" 链接  " + p.link.View(),
				"",
				ui.HelpStyle.Render(" 改链接不会重新拉取节点（u 才会）"),
				ui.HelpStyle.Render(" Tab 切换字段  Enter 提交  esc 取消"),
			}}
		}
	}
	return nil
}

// modalLines renders the in-pane modal: only the delete confirmation
// (the forms float, see overlay).
func (p subsPage) modalLines() []string {
	if s := p.cur(); s != nil {
		return ui.BoxLines(true, "确认删除订阅 \""+s.Tag+"\"? (y/n)")
	}
	return []string{ui.HelpStyle.Render("（无订阅）")}
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
		// daed never writes the subscription's status field (created as ""
		// and the refresh flow does not touch it), so emptiness is not a
		// failure signal — render a dim dash and judge by freshness. A
		// status that does name a failure gets its info first line below.
		failed := containsFold(s.Status, "fail") || containsFold(s.Status, "error")
		st, stStyle := s.Status, ui.OKStyle
		switch {
		case failed:
			stStyle = ui.ErrorStyle
		case st == "":
			st, stStyle = "—", ui.HelpStyle
		}
		lines = append(lines, cursor+" "+ui.PadRight(s.Tag, max0(p.leftW-26))+
			stStyle.Render(ui.Truncate(st, 10))+
			ui.HelpStyle.Render(" "+strconv.Itoa(s.NodeCount)+"节点"))
		if failed && s.Info != "" {
			lines = append(lines, "    "+ui.ErrorStyle.Render(
				ui.Truncate(firstLine(s.Info), max0(p.leftW-8))))
		}
	}
	return lines
}

// leftClick selects the row-th displayed subscription, mirroring leftLines'
// row layout (prefix lines, failed subs' info rows and the scroll window)
// so the click lands on the row the user saw. Selecting also (re)fetches
// the subscription's node list.
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
	start := 0
	if p.sel >= rowsH {
		start = p.sel - rowsH + 1
	}
	// Walk the same rows leftLines renders: each sub has one row and a
	// failed sub with an info line one more — clicking that info row
	// selects the failed sub it describes.
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
	return p.ensureNodes(d)
}

// infoLines is the right column's top box: the subscription's metadata,
// labels on a shared 6-cell column like every other detail pane.
func (p subsPage) infoLines() []string {
	s := p.cur()
	if s == nil {
		return []string{ui.HelpStyle.Render("（无订阅）")}
	}
	// daed never writes the status field (always ""), so emptiness renders
	// as a dim dash; only a status naming a failure is red.
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
		ui.SelectedStyle.Render("标签  ") + s.Tag,
		ui.SelectedStyle.Render("状态  ") + status,
		ui.SelectedStyle.Render("定时  ") + cron,
		ui.SelectedStyle.Render("链接  ") + ui.Truncate(s.Link, max0(p.rightW-10)),
	}
	if s.Info != "" {
		lines = append(lines, ui.SelectedStyle.Render("信息  ")+ui.Truncate(firstLine(s.Info), max0(p.rightW-10)))
	}
	return append(lines, ui.SelectedStyle.Render("更新  ")+ui.TimeAgo(s.UpdatedAt))
}

// bodyLines is the right column's bottom box: the subscription's node list
// (or the delete confirmation in its place). inner is the box's content
// height; the node list windows itself to it.
func (p subsPage) bodyLines(inner int) []string {
	if p.mode == 2 {
		return p.modalLines() // delete confirmation
	}
	s := p.cur()
	if s == nil {
		return []string{ui.HelpStyle.Render("（无订阅）")}
	}
	if err := p.subErr[s.ID]; err != nil {
		return []string{ui.ErrorStyle.Render("✗ 拉取节点失败: " + shortErr(err))}
	}
	all := p.subNodes[s.ID]
	if p.loading == s.ID && all == nil {
		return []string{ui.HelpStyle.Render(" 拉取节点中…")}
	}
	if len(all) == 0 {
		return []string{ui.HelpStyle.Render(" 无节点（u 更新订阅后重试）")}
	}
	var lines []string
	if prompt := p.nodeView.prompt(); prompt != "" {
		lines = append(lines, ui.HelpStyle.Render(prompt))
	}
	nodes := p.visibleNodes()
	if len(nodes) == 0 {
		return append(lines, ui.ErrorStyle.Render(" 没有匹配的节点（/ 重新编辑，框内 esc 清空）"))
	}
	rowsH := max0(inner - len(lines))
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
		nameW := max0(p.rightW - 17 - latCellW(p.rightW-4))
		lines = append(lines, cursor+" "+ui.PadRight(ui.SpaceAfterFlag(n.Name), nameW)+
			ui.HelpStyle.Render(ui.PadRight(n.Protocol, 8))+
			latencyCell(p.lat, n.ID, latCellW(p.rightW-4)))
	}
	return lines
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
