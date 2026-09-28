package app

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"dae-tui/internal/driver"
	"dae-tui/internal/i18n"
	"dae-tui/internal/keymap"
	"dae-tui/internal/ui"
)

type homePage struct {
	groups []driver.Group
	lat    map[string]driver.Latency
	trafficPage

	subs []driver.Subscription

	cfgName    string
	dnsName    string
	dnsSummary []string

	apiTook time.Duration

	ifaces []driver.NetworkInterface
	lanIf  []string
	wanIf  []string

	confirmSwitch bool

	routingID      string
	routingName    string
	routingMode    string
	routingBody    string
	routingSummary []string
	routingRefs    []string
	presets        []driver.RoutingPreset
	presetCursor   int
	groupIdx       int
	groupChosen    bool

	confirmPreset int
	presetText    string
	presetErr     error

	groupCursor int
	groupFocus  bool

	scroll int
	follow bool

	width, height int

	caps driver.Caps
}

func newHomePage(caps driver.Caps) homePage {
	return homePage{lat: map[string]driver.Latency{}, confirmPreset: -1, caps: caps, follow: true}
}

func (p *homePage) setSize(w, h int) {
	p.width, p.height = w, h
	p.trafficPage.setSize(w, h)
}

func (p *homePage) handleGroups(groups []driver.Group, err error) {
	if err != nil {
		return
	}
	p.groups = groups
	if p.groupCursor >= len(p.groups) {
		p.groupCursor = max0(len(p.groups) - 1)
	}
	p.rederiveGroupIdx()
}

func (p *homePage) setSubs(subs []driver.Subscription, err error) {
	if err != nil {
		return
	}
	p.subs = subs
}

func (p homePage) subLines() []string {
	if len(p.subs) == 0 || !p.caps.Subscriptions {
		return nil
	}
	nodes := 0
	var newest time.Time
	for _, s := range p.subs {
		nodes += s.NodeCount
		if s.UpdatedAt.After(newest) {
			newest = s.UpdatedAt
		}
	}
	fresh := ui.TimeAgo(newest)
	if newest.IsZero() {
		fresh = i18n.T("从未")
	}
	lines := []string{" " + i18n.T("订阅 %d · 节点 %d · 最近更新 ", len(p.subs), nodes) +
		ui.SelectedStyle.Render(fresh)}
	for _, s := range p.subs {
		if s.CronEnable && !s.UpdatedAt.IsZero() && time.Since(s.UpdatedAt) > 24*time.Hour {
			tag := s.Tag
			if tag == "" {
				tag = s.ID
			}
			lines = append(lines, " "+lipgloss.NewStyle().Foreground(ui.Yellow).Render(
				"⚠ "+tag+i18n.T(" 上次更新 ")+ui.TimeAgo(s.UpdatedAt)+i18n.T("，定时刷新已开启")))
		}
	}
	return lines
}

func (p homePage) groupHealth(g driver.Group) string {
	var measured, alive int
	var newest time.Time
	for _, n := range g.Members() {
		l, ok := p.lat[n.ID]
		if !ok || l.TestedAt.IsZero() {
			continue
		}
		measured++
		if l.Alive {
			alive++
		}
		if l.TestedAt.After(newest) {
			newest = l.TestedAt
		}
	}
	if measured == 0 {
		return ui.HelpStyle.Render(i18n.T("未测速"))
	}
	st := lipgloss.NewStyle().Foreground(ui.Green)
	switch {
	case alive == 0:
		st = lipgloss.NewStyle().Foreground(ui.Red)
	case alive < measured:
		st = lipgloss.NewStyle().Foreground(ui.Yellow)
	}
	return st.Render(fmt.Sprintf("%d/%d", alive, measured)) +
		ui.HelpStyle.Render(" · "+ui.TimeAgo(newest))
}

func (p homePage) missingRefLines(cw int) []string {
	var out []string
	for _, ref := range p.routingRefs {
		if isBuiltinOutbound(ref) {
			continue
		}
		found := false
		for _, g := range p.groups {
			if g.Name == ref {
				found = true
				break
			}
		}
		if found {
			continue
		}
		out = append(out, " "+lipgloss.NewStyle().Foreground(ui.Yellow).Render(
			ui.Truncate(i18n.T("⚠ 路由引用的组 ")+ref+i18n.T(" 不存在，相关规则已失效"), cw-1)))
	}
	return out
}

func (p homePage) rulesDigest(cw int) (warn, rules []string) {
	warn = p.missingRefLines(cw)
	for _, l := range p.routingSummary {
		rules = append(rules, " "+ui.HelpStyle.Render(ui.Truncate(l, cw-1)))
	}
	return warn, rules
}

func (p homePage) dnsLines(w int) []string {
	if p.dnsName == "" || !p.caps.ConfigMgmt {
		return nil
	}
	lines := []string{" " + ui.HelpStyle.Render("DNS  ") +
		ui.Truncate(p.dnsName, max0(w-6))}
	for _, u := range p.dnsSummary {
		lines = append(lines, "     "+ui.HelpStyle.Render(ui.Truncate(u, max0(w-6))))
	}
	return lines
}

func (p homePage) latSummaryRow(cw int) string {
	if len(p.groups) == 0 {
		return ""
	}
	seen := map[string]bool{}
	var total, measured, alive, best int
	var bestName string
	var newest time.Time
	for _, g := range p.groups {
		for _, n := range g.Members() {
			if seen[n.ID] {
				continue
			}
			seen[n.ID] = true
			total++
			l, ok := p.lat[n.ID]
			if !ok || l.TestedAt.IsZero() {
				continue
			}
			measured++
			if l.Alive {
				alive++
				if l.Ms > 0 && (best == 0 || l.Ms < best) {
					best, bestName = l.Ms, ui.SpaceAfterFlag(n.Name)
				}
			}
			if l.TestedAt.After(newest) {
				newest = l.TestedAt
			}
		}
	}
	if total == 0 {
		return ""
	}
	s := i18n.T("全部 %d 节点", total)
	if measured == 0 {
		s += i18n.T(" · 未测速")
	} else {
		s += i18n.T(" · 已测 %d · 存活 %d", measured, alive)
		if bestName != "" {
			s += i18n.T(" · 最快 ") + bestName
		}
		s += " · " + ui.TimeAgo(newest)
	}
	return " " + ui.HelpStyle.Render(ui.Truncate(s, cw-1))
}

func (p *homePage) handleSelections(sel driver.Selections, err error, d driver.Driver) {
	if err != nil {
		return
	}
	for _, c := range sel.Configs {
		if !c.Selected {
			continue
		}
		p.lanIf = ifaceNames(c, "lanInterface")
		p.wanIf = ifaceNames(c, "wanInterface")
		p.cfgName = c.Name
		break
	}
	for _, d := range sel.Dns {
		if d.Selected {
			p.dnsName, p.dnsSummary = d.Name, d.Summary
			break
		}
	}
	for _, r := range sel.Routings {
		if !r.Selected {
			continue
		}
		p.routingID, p.routingName, p.routingBody = r.ID, r.Name, r.Body
		p.routingSummary, p.routingRefs = r.Summary, r.References
		p.routingMode = d.DetectRoutingPreset(r.Body)

		for i, preset := range p.presets {
			if preset.ID == p.routingMode {
				p.presetCursor = i
				break
			}
		}
		p.rederiveGroupIdx()
		return
	}
	p.routingID, p.routingName, p.routingBody, p.routingMode = "", "", "", ""
	p.routingSummary, p.routingRefs = nil, nil
}

func (p *homePage) setInterfaces(ifaces []driver.NetworkInterface) {
	p.ifaces = ifaces
}

func ifaceNames(it driver.ConfigItem, key string) []string {
	for _, f := range it.Fields {
		if f.Name != key {
			continue
		}
		return configuredIfaces(f.Value)
	}
	return nil
}

func (p homePage) hasIface(name string) bool {
	for _, i := range p.ifaces {
		if i.Name == name {
			return true
		}
	}
	return false
}

func (p homePage) netLines(w int) []string {
	if len(p.ifaces) == 0 {
		return nil
	}

	lines := make([]string, 0, len(p.ifaces)*2)
	for _, i := range p.ifaces {
		s := i.Name + " "
		if i.Up {
			s += "↑"
		} else {
			s += "↓"
		}
		if ips := strings.Join(i.IPs, " · "); ips != "" {
			s += " " + ips
		}
		if i.Default {
			s += i18n.T(" 默认路由")
		}
		label := ui.HelpStyle.Render(i18n.T("网卡 "))
		if len(lines) > 0 {
			label = "     "
		}
		lines = append(lines, " "+label+ui.Truncate(s, max0(w-6)))
		if i.Default && i.Gateway != "" {
			lines = append(lines, "       "+ui.HelpStyle.Render(
				ui.Truncate(i18n.T("· 网关 ")+i.Gateway, max0(w-8))))
		}
	}
	lines = append(lines, netWarnLines(p)...)
	return lines
}

func netWarnLines(p homePage) []string {
	var lines []string
	for _, w := range []struct {
		label string
		names []string
	}{{"WAN", p.wanIf}, {"LAN", p.lanIf}} {
		for _, n := range w.names {
			if p.hasIface(n) {
				continue
			}
			lines = append(lines, " "+ui.ErrorStyle.Render(i18n.T("⚠ 配置的 ")+w.label+i18n.T(" 接口 ")+n+
				i18n.T(" 不存在（daed 按网卡名绑定，DHCP 改名或换网口后需更新配置）")))
		}
	}
	return lines
}

func (p *homePage) rederiveGroupIdx() {
	if p.groupChosen && p.groupIdx < len(p.groups) {
		return
	}
	if p.routingBody == "" || len(p.groups) == 0 {
		p.groupIdx = 0
		return
	}

	for _, line := range strings.Split(p.routingBody, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		target := ""
		if i := strings.Index(line, "->"); i >= 0 {
			target = strings.TrimSpace(line[i+2:])
		} else if strings.HasPrefix(line, "fallback:") {
			target = strings.TrimSpace(strings.TrimPrefix(line, "fallback:"))
		}
		if target == "" || isBuiltinOutbound(target) {
			continue
		}
		for i, g := range p.groups {
			if g.Name == target {
				p.groupIdx = i
				return
			}
		}
	}

	for i, g := range p.groups {
		if !strings.EqualFold(g.Name, "direct") {
			p.groupIdx = i
			return
		}
	}
	p.groupIdx = 0
}

func (p *homePage) proxyGroup() string {
	if p.groupIdx < 0 || p.groupIdx >= len(p.groups) {
		return ""
	}
	return p.groups[p.groupIdx].Name
}

func (p *homePage) cycleGroup() {
	if len(p.groups) == 0 {
		return
	}
	p.groupIdx = (p.groupIdx + 1) % len(p.groups)
	p.groupChosen = true
}

func (p *homePage) renderPresetText(d driver.Driver) {
	if p.confirmPreset < 0 || p.confirmPreset >= len(p.presets) {
		return
	}
	p.presetText, p.presetErr = d.BuildRoutingPreset(p.presets[p.confirmPreset].ID, p.proxyGroup())
}

func (p *homePage) handleLatencies(lats []driver.Latency) {
	for _, l := range lats {
		p.lat[l.NodeID] = l
	}
}

func (p *homePage) handleKey(msg tea.KeyMsg, d driver.Driver, running bool) tea.Cmd {
	// Any keystroke re-arms cursor-follow scrolling: the keyboard user's
	// context is the active line, so the window snaps back to it.
	p.follow = true
	// The remap layer: the switches below keep reading their default key
	// names; tk rewrites what the user pressed (home has no text inputs,
	// so the whole function dispatches through the translated name).
	key := tk(keymap.Home, msg.String())
	if p.confirmPreset >= 0 {
		switch key {
		case "y":
			idx := p.confirmPreset
			if p.presetErr != nil {

				err := p.presetErr
				p.confirmPreset, p.presetText, p.presetErr = -1, "", nil
				return func() tea.Msg { return opDoneMsg{Op: i18n.T("切换路由"), Err: err} }
			}
			text := p.presetText
			label := i18n.T("路由方案 ") + p.routingName + " → " + presetLabel(p.presets[idx].ID)
			p.confirmPreset, p.presetText = -1, ""
			return presetTextCmd(d, "routing", p.routingID, text, label)
		case "g":
			p.cycleGroup()
			p.renderPresetText(d)
		case "n", "esc":
			p.confirmPreset, p.presetText, p.presetErr = -1, "", nil
		}
		return nil
	}
	if p.confirmSwitch {
		switch key {
		case "y":
			p.confirmSwitch = false
			return runToggleCmd(d, running)
		case "n", "esc":
			p.confirmSwitch = false
		}
		return nil
	}

	if p.groupFocus {
		switch key {
		case "tab", "esc", "h", "left":
			p.groupFocus = false
			return nil
		case "j", "down":
			if p.groupCursor < len(p.groups)-1 {
				p.groupCursor++
			}
			return nil
		case "k", "up":
			if p.groupCursor > 0 {
				p.groupCursor--
			}
			return nil
		case "enter":
			if p.groupCursor >= 0 && p.groupCursor < len(p.groups) {
				id := p.groups[p.groupCursor].ID
				return func() tea.Msg { return gotoGroupMsg{ID: id} }
			}
			return nil
		}
	}
	switch key {
	case "s":
		p.confirmSwitch = true
	case "L":
		return logsSpawnCmd(logsHistoryLines, true)
	case "tab":
		if len(p.groups) > 0 {
			p.groupFocus = true
		}
	case "j", "down":
		if p.presetCursor < len(p.presets)-1 {
			p.presetCursor++
		}
	case "k", "up":
		if p.presetCursor > 0 {
			p.presetCursor--
		}
	case "enter":
		if p.routingID == "" || len(p.presets) == 0 {
			return nil
		}
		idx := p.presetCursor
		text, err := d.BuildRoutingPreset(p.presets[idx].ID, p.proxyGroup())
		if err != nil {
			return func() tea.Msg { return opDoneMsg{Op: i18n.T("切换路由"), Err: err} }
		}
		p.presetText, p.presetErr, p.confirmPreset = text, nil, idx
	case "g":
		p.cycleGroup()
	}
	return nil
}

func (p *homePage) handleValidated(msg presetValidatedMsg, d driver.Driver) tea.Cmd {
	if msg.Err != nil {
		return func() tea.Msg { return opDoneMsg{Op: i18n.T("切换路由"), Err: msg.Err} }
	}
	return configTextCmd(d, msg.Section, msg.ID, msg.Text, msg.Label)
}

const homeTwoColMin = 96

const homeRoutingW = 44

const rulesDigestMinInner = 4

var presetLabels = map[string]string{
	"gfw":    "GFW 模式",
	"nonCn":  "中国列表以外",
	"cnOnly": "中国列表",
	"global": "全局代理",
}

var presetDescs = map[string]string{
	"gfw":    "仅代理被墙域名，其余直连",
	"nonCn":  "国内直连，其余走代理",
	"cnOnly": "国内走代理，其余直连",
	"global": "全部流量走代理",
}

func presetLabel(id string) string {
	if l, ok := presetLabels[id]; ok {
		return i18n.T(l)
	}
	return id
}

func isBuiltinOutbound(target string) bool {
	switch target {
	case "direct", "must_direct", "block", "must_proxy":
		return true
	}
	return false
}

func (p *homePage) currentNode(g driver.Group) (label string, style lipgloss.Style) {
	if sel := g.SelectedNode(); sel != nil && g.Policy == "fixed" {
		return i18n.T("手动: ") + ui.SpaceAfterFlag(sel.Name), ui.OKStyle
	}

	best, bestMs := "", -1
	for _, n := range g.Members() {
		if l, ok := p.lat[n.ID]; ok && l.Alive && l.Ms > 0 && (bestMs < 0 || l.Ms < bestMs) {
			best, bestMs = ui.SpaceAfterFlag(n.Name), l.Ms
		}
	}
	auto := map[string]string{
		"min_moving_avg": "自动(移动平均)", "min_avg10": "自动(平均)", "min": "自动(最新)",
	}[g.Policy]
	if auto == "" {
		auto = g.Policy
	} else {
		auto = i18n.T(auto)
	}
	if best != "" {
		return fmt.Sprintf("%s ≈ %s", auto, best), ui.SelectedStyle
	}

	return auto + i18n.T(" · 未测速"), ui.HelpStyle
}

func (p homePage) overlay() *overlaySpec {
	switch {
	case p.confirmPreset >= 0 && p.presetErr != nil:
		return &overlaySpec{destructive: true, lines: []string{
			ui.ErrorStyle.Render(i18n.T(" ✗ 无法生成: ") + shortErr(p.presetErr)),
			"",
			ui.HelpStyle.Render(i18n.T(" n / esc 关闭")),
		}}
	case p.confirmPreset >= 0:
		lines := []string{
			ui.TitleStyle.Render(i18n.T(" 切换路由方案")),
			"",
			i18n.T("将把 ") + p.routingName + i18n.T(" 替换为「") +
				presetLabel(p.presets[p.confirmPreset].ID) + i18n.T("」"),
			ui.HelpStyle.Render(i18n.T(" 代理组: ") + p.proxyGroup() + i18n.T("  (g 换组)")),
			"",
			ui.SelectedStyle.Render(i18n.T(" 将写入的 DSL:")),
		}
		for i, l := range strings.Split(p.presetText, "\n") {
			if i >= 14 {
				lines = append(lines, ui.HelpStyle.Render("    …"))
				break
			}
			lines = append(lines, ui.HelpStyle.Render("    "+l))
		}
		return &overlaySpec{destructive: true, lines: append(lines, "",
			ui.OKStyle.Render(i18n.T(" y 确认"))+"    "+ui.ErrorStyle.Render(i18n.T("n / esc 取消"))+"    "+
				ui.HelpStyle.Render(i18n.T("g 换组")))}
	}
	return nil
}

func (p *homePage) scrollBy(d int) {
	p.scroll += d
	p.follow = false
}

type homeAnchors struct {
	presetStart int
	groupStart  int
}

func (p homePage) bodyLines(status driver.Status) ([]string, int, homeAnchors) {
	var lines []string
	active := -1
	var anchors homeAnchors
	twoCol := p.width >= homeTwoColMin
	const margin = 1
	full := p.width - margin*2

	addZone := func(zone []string) {
		lines = append(lines, zone...)
	}

	lw, rw := homeRoutingW, 0
	if twoCol {
		rw = p.width - margin*2 - lw - 1
	}
	pairedRow := func(leftTitle, leftFooter string, left []string,
		rightTitle, rightFooter string, right []string, rightFocused bool) []string {
		return ui.PaneRow(
			ui.PaneSpec{Title: leftTitle, Footer: leftFooter, Lines: left, W: lw},
			ui.PaneSpec{Title: rightTitle, Footer: rightFooter, Lines: right,
				Focused: rightFocused, W: rw})
	}

	onOff := K(keymap.Home, "s") + " " + i18n.T("停止")
	if !status.Running {
		onOff = K(keymap.Home, "s") + " " + i18n.T("启动")
	}
	if twoCol {
		addZone(pairedRow(i18n.T("代理"), onOff, p.proxyRows(status, homeRoutingW-4),
			i18n.T("流量"), "", p.trafficRows(p.width-margin*2-homeRoutingW-1-4), false))
	} else {
		addZone(ui.TitledBoxFooter(i18n.T("代理"), onOff, false, full, p.proxyRows(status, full-4)))
		addZone(ui.TitledBox(i18n.T("流量"), false, full, p.trafficRows(full-4)))
	}

	if p.confirmSwitch {
		verb := i18n.T("启动")
		if status.Running {
			verb = i18n.T("停止")
		}
		box := ui.BoxLines(true, i18n.T("确认")+verb+i18n.T("代理?  (y/n)"))
		addZone(box)
		active = len(lines) - 1
	}

	routingBoxW := full
	if twoCol {
		routingBoxW = p.width - margin*2 - homeRoutingW - 1
	}
	routingBody, presetOff := p.routingLines(routingBoxW - 4)

	presetStart := -1
	row2Start := len(lines)
	if presetOff >= 0 {
		presetStart = row2Start + 1 + presetOff
	}
	anchors.presetStart = presetStart
	envW := full - 4
	if twoCol {
		envW = homeRoutingW - 4
	}
	env := append(p.subLines(), append(p.dnsLines(envW), p.netLines(envW)...)...)
	if twoCol && len(env) > 0 {
		addZone(pairedRow(i18n.T("环境"), "", env, i18n.T("路由"), i18n.T("Enter 切换 · ")+K(keymap.Home, "g")+" "+i18n.T("换组"), routingBody, !p.groupFocus))
	} else {
		addZone(ui.TitledBoxFooter(i18n.T("路由"), i18n.T("Enter 切换 · ")+K(keymap.Home, "g")+" "+i18n.T("换组"), !p.groupFocus, routingBoxW, routingBody))
		if len(env) > 0 {
			addZone(ui.TitledBox(i18n.T("环境"), false, full, env))
		}
	}

	groupRows := make([]string, 0, len(p.groups)+2)
	if len(p.groups) == 0 {
		groupRows = append(groupRows, " "+ui.HelpStyle.Render(i18n.T("（加载中…）")))
	}
	cw := full - 4
	for i, g := range p.groups {
		label, style := p.currentNode(g)
		cursor := " "
		if i == p.groupCursor && p.groupFocus {
			cursor = ui.CursorStyle.Render("❯")
		}
		row := " " + cursor + " " + ui.PadRight(g.Name, 16) + style.Render(label)

		if cw >= 68 {
			if h := p.groupHealth(g); h != "" {
				if pad := cw - 1 - lipgloss.Width(row) - lipgloss.Width(h); pad >= 2 {
					row += strings.Repeat(" ", pad) + h
				} else {
					row += "  " + h
				}
			}
		}
		groupRows = append(groupRows, row)
	}

	if row := p.latSummaryRow(cw); row != "" {
		groupRows = append(groupRows, row)
	}

	if p.groupFocus && p.groupCursor >= 0 && p.groupCursor < len(groupRows) {
		groupRows[p.groupCursor] = ui.HiRow(groupRows[p.groupCursor], cw, true)
	}
	groupFooter := i18n.T("Tab 切到组列表 · Enter 跳群组页")
	if p.groupFocus {
		groupFooter = i18n.T("Enter 跳群组页 · Tab 返回路由切换")
	}
	groupStart := len(lines) + 1
	anchors.groupStart = groupStart

	digestInner := p.height - (len(lines) + len(groupRows) + 2) - 2
	digestWarn, digestRules := p.rulesDigest(cw)
	digestShow := p.caps.ConfigMgmt && digestInner >= rulesDigestMinInner &&
		(len(digestWarn) > 0 || len(digestRules) > 0)
	if !digestShow {
		if leftover := p.height - (len(lines) + len(groupRows) + 2); leftover > 0 {
			for i := 0; i < leftover; i++ {
				groupRows = append(groupRows, "")
			}
		}
	}
	addZone(ui.TitledBoxFooter(i18n.T("各组当前节点"), groupFooter, p.groupFocus, full, groupRows))

	if digestShow {
		rows := digestWarn
		budget := digestInner - len(digestWarn)
		if budget > 0 {
			if len(digestRules) > budget {
				rows = append(rows, digestRules[:budget-1]...)
				rows = append(rows, " "+ui.HelpStyle.Render(
					i18n.T("… 其余 %d 条", len(digestRules))))
			} else {
				rows = append(rows, digestRules...)
			}
		}
		for len(rows) < digestInner {
			rows = append(rows, "")
		}
		rows = rows[:digestInner]
		addZone(ui.TitledBox(i18n.T("规则速览"), false, full, rows))
	}

	if active < 0 && presetStart >= 0 && !p.groupFocus {
		active = presetStart + p.presetCursor
	}
	if active < 0 && p.groupFocus && groupStart+p.groupCursor < len(lines) {
		active = groupStart + p.groupCursor
	}
	return lines, active, anchors
}

func (p *homePage) click(row, cx int, status driver.Status) {
	if row < 0 {
		return
	}
	lines, active, a := p.bodyLines(status)
	h := p.height
	if h < 1 {
		h = 1
	}
	abs := row + p.windowStart(len(lines), active, h) + 1
	if abs >= a.groupStart && abs < a.groupStart+len(p.groups) {
		p.groupFocus = true
		p.groupCursor = abs - a.groupStart
		return
	}
	if a.presetStart < 0 || abs < a.presetStart || abs >= a.presetStart+len(p.presets) {
		return
	}
	if p.width >= homeTwoColMin && cx <= homeRoutingW {
		return
	}
	p.groupFocus = false
	p.presetCursor = abs - a.presetStart
}

func (p homePage) windowStart(lineCount, active, h int) int {
	start := p.scroll
	if p.follow && active >= 0 {
		if active < start {
			start = active
		}
		if active >= start+h {
			start = active - h + 1
		}
	}
	if start > lineCount-h {
		start = lineCount - h
	}
	if start < 0 {
		start = 0
	}
	return start
}

func (p homePage) proxyRows(status driver.Status, w int) []string {

	badge := " " + ui.OKStyle.Render("● ") + lipgloss.NewStyle().Bold(true).Render(i18n.T("代理运行中"))
	if !status.Running {
		badge = lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color("15")).Background(ui.Red).
			Padding(0, 1).Render(i18n.T("○ 代理已停止"))
	}
	rows := []string{badge, ""}
	if p.caps.ConfigMgmt {
		name := func(s string) string {
			if s == "" {
				return i18n.T("（无）")
			}
			return s
		}

		rows = append(rows,
			" "+ui.HelpStyle.Render(i18n.T("方案 config  "))+name(p.cfgName),
			"      "+ui.HelpStyle.Render("dns     ")+name(p.dnsName),
			"      "+ui.HelpStyle.Render("routing ")+name(p.routingName))
	}
	return rows
}

func (p homePage) trafficRows(w int) []string {
	if !p.caps.TrafficStats {
		return []string{ui.HelpStyle.Render(i18n.T("当前后端不支持流量统计"))}
	}
	if w < 24 {
		w = 24
	}
	const chartH = 3
	green := lipgloss.NewStyle().Foreground(ui.Green)
	yellow := lipgloss.NewStyle().Foreground(ui.Yellow)
	greenBold := lipgloss.NewStyle().Bold(true).Foreground(ui.Green)
	yellowBold := lipgloss.NewStyle().Bold(true).Foreground(ui.Yellow)
	s := p.snap

	cw := (w - 3) / 2
	up := ui.HelpStyle.Render(i18n.T("↑ 上行 ")) + greenBold.Render(ui.Rate(s.UpRate))
	down := ui.HelpStyle.Render(i18n.T("↓ 下行 ")) + yellowBold.Render(ui.Rate(s.DownRate))
	peakUp := ui.HelpStyle.Render(i18n.T(" · 峰值 ")) + green.Render(ui.Rate(maxF(s.UpSeries)))
	peakDown := ui.HelpStyle.Render(i18n.T(" · 峰值 ")) + yellow.Render(ui.Rate(maxF(s.DownSeries)))

	peakNote := ""
	if lipgloss.Width(up)+lipgloss.Width(peakUp)+lipgloss.Width(down)+lipgloss.Width(peakDown)+2 <= w {
		up += peakUp
		down += peakDown
	} else {
		peakNote = ui.HelpStyle.Render(i18n.T(" · 峰值 ↑%s ↓%s",
			ui.Rate(maxF(s.UpSeries)), ui.Rate(maxF(s.DownSeries))))
	}

	upW, downW := lipgloss.Width(up), lipgloss.Width(down)
	gap := cw + 2 - 1 - upW
	if gap < 1 || 1+upW+gap+downW > w {
		gap = max0(w - 1 - upW - downW)
		if gap < 1 {
			gap = 1
		}
	}
	rates := " " + up + strings.Repeat(" ", gap) + down
	charts := lipgloss.JoinHorizontal(lipgloss.Top,
		ui.Sparkline(s.UpSeries, cw, chartH, green, ""),
		" ",
		ui.Sparkline(s.DownSeries, cw, chartH, yellow, ""))

	head := ui.HelpStyle.Render(i18n.T("近 10s")) + peakNote
	tail := ui.HelpStyle.Render(i18n.T("连接 %d · UDP %d", s.Conns, s.UDPSessions))
	if p.apiTook > 0 {
		tail += ui.HelpStyle.Render(fmt.Sprintf(" · API %dms", p.apiTook.Milliseconds()))
	}
	sep := ui.HelpStyle.Render(" · ")
	foot := []string{" " + head + sep + tail}
	if lipgloss.Width(head)+lipgloss.Width(sep)+lipgloss.Width(tail) > w {
		foot = []string{" " + head, " " + tail}
	}

	total := ui.HelpStyle.Render(i18n.T("累计 ")) + green.Render("↑ "+ui.Bytes(s.UpTotal)) +
		ui.HelpStyle.Render(" · ") + yellow.Render("↓ "+ui.Bytes(s.DownTotal)) +
		ui.HelpStyle.Render(i18n.T(" · 自 daed 启动"))

	rows := []string{rates}
	for _, l := range strings.Split(charts, "\n") {
		rows = append(rows, " "+l)
	}
	return append(append(rows, foot...), " "+total)
}

func maxF(series []float64) float64 {
	m := 0.0
	for _, v := range series {
		if v > m {
			m = v
		}
	}
	return m
}

func (p homePage) routingLines(w int) (body []string, presetStart int) {
	if len(p.presets) == 0 {
		return nil, -1
	}
	name := p.routingName
	if name == "" {
		name = i18n.T("（无）")
	}
	head := ui.HelpStyle.Render(i18n.T("当前 ")) + ui.SelectedStyle.Render(name)
	if g := p.proxyGroup(); g != "" {
		head += ui.HelpStyle.Render(i18n.T(" · 代理组: ")) + ui.SelectedStyle.Render(g)
	}
	body = append(body, " "+head)
	if p.routingID == "" {
		body = append(body, " "+ui.HelpStyle.Render(i18n.T("（无路由方案：在 5 配置页创建后可用）")))
	} else if p.proxyGroup() == "" {
		body = append(body, " "+ui.HelpStyle.Render(i18n.T("（无群组：预设需要一个代理组，请在 2 群组页创建）")))
	}
	presetStart = len(body)
	for i, preset := range p.presets {
		// The cursor rides the routing box only while that box has focus;
		// with focus on the group list the row keeps just its ● state mark
		// so the page never shows two cursors.
		onCursor := i == p.presetCursor && !p.groupFocus
		cursor := " "
		if onCursor {
			cursor = ui.CursorStyle.Render("❯")
		}
		mark := "  "
		if preset.ID == p.routingMode {
			mark = ui.OKStyle.Render("● ")
		}
		style := ui.HelpStyle
		if onCursor {
			style = ui.CursorStyle
		}
		row := " " + cursor + " " + mark + style.Render(ui.PadRight(presetLabel(preset.ID), 14)) +
			ui.HelpStyle.Render(i18n.T(presetDescs[preset.ID]))
		body = append(body, ui.HiRow(row, w, onCursor))
	}
	switch {
	case p.routingMode != "":
		body = append(body, " "+ui.HelpStyle.Render(i18n.T("当前: ")+presetLabel(p.routingMode)))
	default:
		body = append(body, " "+ui.HelpStyle.Render(i18n.T("当前: 自定义规则")))
	}
	return body, presetStart
}

func (p homePage) View(status driver.Status) string {
	lines, active, _ := p.bodyLines(status)
	h := p.height
	if h < 1 {
		h = 1
	}
	start := p.windowStart(len(lines), active, h)
	end := start + h
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[start:end], "\n")
}
