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

// nodesPage manages manual (subscription-less) nodes: list, import from a
// share link, remove, latency-test, and attach to a group.
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

	mode       int // 0 list, 1 add form, 2 delete confirm, 3 group picker
	link       textinput.Model
	tag        textinput.Model
	ifld       int
	pickCursor int

	leftW, rightW, height int
}

func newNodesPage() nodesPage {
	l := textinput.New()
	l.Placeholder = "vmess://… / ss://… / trojan://… 分享链接"
	l.CharLimit = 4096
	l.Width = 48
	t := textinput.New()
	t.Placeholder = "标签 (可空)"
	t.CharLimit = 64
	t.Width = 32
	return nodesPage{
		link: l, tag: t,
		lat:      map[string]driver.Latency{},
		baseline: map[string]time.Time{},
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

func (p *nodesPage) cur() *driver.Node {
	if p.sel < 0 || p.sel >= len(p.nodes) {
		return nil
	}
	return &p.nodes[p.sel]
}

func (p *nodesPage) startTest(d driver.Driver, onlySelected bool) tea.Cmd {
	ids := make([]string, 0, len(p.nodes))
	if onlySelected {
		if n := p.cur(); n != nil {
			ids = append(ids, n.ID)
		}
	} else {
		for i, n := range p.nodes {
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
	if p.mode == 2 {
		switch msg.String() {
		case "y":
			n := p.cur()
			p.mode = 0
			if n == nil {
				return nil
			}
			p.busy = true
			return nodeMutateCmd(d, nodeMutation{kind: 1, ids: []string{n.ID}}, "删除节点 "+n.Name)
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
				"添加节点 "+n.Name+" 到组 "+g.Name)
		}
		return nil
	}

	switch msg.String() {
	case "a":
		p.mode = 1
		p.ifld = 0
		p.link.SetValue("")
		p.tag.SetValue("")
		p.link.Focus()
		p.tag.Blur()
		return textinput.Blink
	case "t":
		return p.startTest(d, false)
	case "T":
		return p.startTest(d, true)
	}

	if p.focus == 0 {
		switch msg.String() {
		case "j", "down":
			if p.sel < len(p.nodes)-1 {
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
			p.sel = len(p.nodes) - 1
		case "tab", "l", "right", "enter":
			if p.cur() != nil {
				p.focus = 1
				p.expanded = true
			}
		case "x":
			if p.cur() != nil {
				p.mode = 2
			}
		}
		return nil
	}

	switch msg.String() {
	case "tab", "h", "left", "esc":
		p.focus = 0
		p.expanded = false
	case "G":
		// attach to group
		if p.cur() != nil && len(p.groups) > 0 {
			p.mode = 3
			p.pickCursor = 0
		}
	}
	return nil
}

func (p *nodesPage) addFormKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
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
		return nodeMutateCmd(d, nodeMutation{kind: 0, link: link, tag: tag}, "导入节点")
	}
	var cmd tea.Cmd
	if p.ifld == 0 {
		p.link, cmd = p.link.Update(msg)
	} else {
		p.tag, cmd = p.tag.Update(msg)
	}
	return cmd
}

func (p nodesPage) View() string {
	lv := ui.Pane(" 手动节点 ("+strconv.Itoa(len(p.nodes))+") ", p.focus == 0, p.leftW, p.height, p.leftLines())
	rv := ui.Pane(" 详情 ", p.focus == 1, p.rightW, p.height, p.rightLines())
	body := lipgloss.JoinHorizontal(lipgloss.Top, lv, " ", rv)

	if p.mode == 1 {
		var f strings.Builder
		f.WriteString(ui.TitleStyle.Render(" 导入手动节点 (分享链接)") + "\n\n")
		f.WriteString(" 链接  " + p.link.View() + "\n")
		f.WriteString(" 标签  " + p.tag.View() + "\n\n")
		f.WriteString(ui.HelpStyle.Render(" Tab 切换字段  Enter 提交  esc 取消"))
		body += "\n" + lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(ui.Accent).Padding(1, 2).Render(f.String())
	}
	if p.mode == 2 {
		if n := p.cur(); n != nil {
			body += "\n" + ui.ErrorStyle.Render(" 确认删除节点 \""+n.Name+"\"? (y/n)")
		}
	}
	return body
}

func (p nodesPage) leftLines() []string {
	var lines []string
	if p.err != nil {
		lines = append(lines, ui.ErrorStyle.Render("✗ "+shortErr(p.err)))
	}
	if len(p.nodes) == 0 {
		lines = append(lines, ui.HelpStyle.Render("无手动节点（按 a 导入）"))
	}
	rowsH := max0(p.height - 2)
	start := 0
	if p.sel >= rowsH {
		start = p.sel - rowsH + 1
	}
	for i := start; i < len(p.nodes) && i < start+rowsH; i++ {
		n := p.nodes[i]
		cursor := " "
		if i == p.sel {
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
		lines = append(lines, cursor+" "+ui.PadRight(n.Name, max0(p.leftW-24))+
			ui.HelpStyle.Render(ui.PadRight(n.Protocol, 8))+
			latStyle.Render(ui.PadLeft(latStr, 9)))
	}
	if p.busy {
		lines = append(lines, ui.HelpStyle.Render("⏳ 操作进行中…"))
	}
	return lines
}

func (p nodesPage) rightLines() []string {
	if p.mode == 3 {
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
	}
	n := p.cur()
	if n == nil {
		return []string{ui.HelpStyle.Render("（无节点）")}
	}
	lines := []string{
		ui.SelectedStyle.Render("名称  ") + n.Name,
		ui.SelectedStyle.Render("协议  ") + n.Protocol,
		ui.SelectedStyle.Render("地址  ") + ui.Truncate(n.Address, max0(p.rightW-8)),
	}
	if n.Tag != "" {
		lines = append(lines, ui.SelectedStyle.Render("标签  ")+n.Tag)
	}
	if l, ok := p.lat[n.ID]; ok && !l.TestedAt.IsZero() {
		v := "dead"
		if l.Alive && l.Ms > 0 {
			v = strconv.Itoa(l.Ms) + "ms"
		}
		lines = append(lines, ui.SelectedStyle.Render("延迟  ")+v)
	}
	lines = append(lines, "",
		ui.HelpStyle.Render(" G 加入群组   x 删除   T 单节点测速"))
	return lines
}
