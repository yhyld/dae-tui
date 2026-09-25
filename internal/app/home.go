package app

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"dae-tui/internal/driver"
	"dae-tui/internal/ui"
)

// homePage is page 1: proxy on/off switch, live traffic charts, the routing
// quick-switch, and the currently used node of every group (fixed → exact,
// auto → inferred from the best latency we have seen).
type homePage struct {
	groups []driver.Group
	lat    map[string]driver.Latency
	trafficPage

	// network state: the NICs the backend sees, plus the interfaces the
	// selected config binds to. daed binds by interface name, so a config
	// pointing at a NIC that no longer exists is a classic silent breakage.
	ifaces []driver.NetworkInterface
	lanIf  []string
	wanIf  []string

	confirmSwitch bool

	// account menu (P): password change and logout. daed allows exactly one
	// user, so the menu only needs those two entries.
	acct     int // 0 none, 1 menu, 2 password form, 3 logout confirm
	acctCur  int
	user     string
	pwFocus  int // 0 current, 1 new, 2 confirm
	pwCur    textinput.Model
	pwNew    textinput.Model
	pwRepeat textinput.Model
	pwErr    string

	// routing quick-switch: the selected routing profile plus the presets
	// that can replace it.
	routingID    string
	routingName  string
	routingMode  string // detected preset id ("" = custom rules)
	routingBody  string // kept to re-derive the proxy group when groups load
	presets      []driver.RoutingPreset
	presetCursor int
	groupIdx     int  // proxy group the presets send traffic to
	groupChosen  bool // the user picked the group with `g`
	// confirmPreset is the preset index awaiting y/n, or -1.
	confirmPreset int
	presetText    string // rendered DSL shown in the confirmation
	presetErr     error  // rendering failure (e.g. unusable group name)

	width, height int
}

func newHomePage() homePage {
	p := homePage{lat: map[string]driver.Latency{}, confirmPreset: -1}
	p.pwCur = newPasswordInput("当前密码")
	p.pwNew = newPasswordInput("新密码 (至少6位, 含字母和数字)")
	p.pwRepeat = newPasswordInput("确认新密码")
	return p
}

func newPasswordInput(placeholder string) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.EchoMode = textinput.EchoPassword
	ti.EchoCharacter = '•'
	ti.CharLimit = 128
	ti.Width = 28
	return ti
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
	p.rederiveGroupIdx()
}

// handleSelections tracks the selected routing profile: which preset (if
// any) it currently is, and which proxy group the presets should target. It
// also records the interfaces the selected config binds to, so the home page
// can flag a NIC that no longer exists.
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
		break
	}
	for _, r := range sel.Routings {
		if !r.Selected {
			continue
		}
		p.routingID, p.routingName, p.routingBody = r.ID, r.Name, r.Body
		p.routingMode = d.DetectRoutingPreset(r.Body)
		p.rederiveGroupIdx()
		return
	}
	p.routingID, p.routingName, p.routingBody, p.routingMode = "", "", "", ""
}

func (p *homePage) setInterfaces(ifaces []driver.NetworkInterface) {
	p.ifaces = ifaces
}

// ifaceNames lists the NICs an interface field binds to ("auto" excluded).
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

// netSection shows the NICs the backend sees (with addresses and default
// routes) and warns when the selected config binds to an interface that is
// gone — DHCP renames and replugged USB NICs make that common, and the
// symptom (proxy silently passing nothing) is invisible without this.
func (p homePage) netSection() string {
	if len(p.ifaces) == 0 {
		return ""
	}
	parts := make([]string, 0, len(p.ifaces))
	for _, i := range p.ifaces {
		s := i.Name + " "
		if i.Up {
			s += "↑"
		} else {
			s += "↓"
		}
		if ips := strings.Join(i.IPs, ","); ips != "" {
			s += " " + ips
		}
		if i.Default {
			s += " 默认路由"
		}
		parts = append(parts, s)
	}
	var b strings.Builder
	b.WriteString(ui.TitleStyle.Render(" 网络") + "  " +
		ui.Truncate(strings.Join(parts, "   "), max0(p.width-8)) + "\n")
	for _, w := range []struct {
		label string
		names []string
	}{{"WAN", p.wanIf}, {"LAN", p.lanIf}} {
		for _, n := range w.names {
			if p.hasIface(n) {
				continue
			}
			b.WriteString(ui.ErrorStyle.Render(" ⚠ 配置的 "+w.label+" 接口 "+n+
				" 不存在（daed 按网卡名绑定，DHCP 改名或换网口后需更新配置）") + "\n")
		}
	}
	return b.String()
}

// rederiveGroupIdx points the proxy group at one the current routing already
// uses, so switching presets keeps traffic on the group the user chose. It
// skips a group the user picked with `g`, and only runs when the routing
// changes or the stored index went stale — never on a plain refresh.
func (p *homePage) rederiveGroupIdx() {
	if p.groupChosen && p.groupIdx < len(p.groups) {
		return
	}
	if p.routingBody == "" || len(p.groups) == 0 {
		p.groupIdx = 0
		return
	}
	// A group the routing references (rule outbound or fallback) wins.
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
	// Otherwise the first group that is not the conventional direct one.
	for i, g := range p.groups {
		if !strings.EqualFold(g.Name, "direct") {
			p.groupIdx = i
			return
		}
	}
	p.groupIdx = 0
}

// proxyGroup is the group the presets send traffic to, or "" when there is
// nothing to target.
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

// renderPresetText regenerates the confirmed preset's DSL after the proxy
// group changed, so the preview always matches what y would write.
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
	if p.acct != 0 {
		return p.acctKey(msg, d)
	}
	if p.confirmPreset >= 0 {
		switch msg.String() {
		case "y":
			idx := p.confirmPreset
			if p.presetErr != nil {
				// The group changed to one that cannot be written as DSL.
				err := p.presetErr
				p.confirmPreset, p.presetText, p.presetErr = -1, "", nil
				return func() tea.Msg { return opDoneMsg{Op: "切换路由", Err: err} }
			}
			text := p.presetText
			label := "路由方案 " + p.routingName + " → " + presetLabel(p.presets[idx].ID)
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
		switch msg.String() {
		case "y":
			p.confirmSwitch = false
			return runToggleCmd(d, running)
		case "n", "esc":
			p.confirmSwitch = false
		}
		return nil
	}
	switch msg.String() {
	case "o":
		p.confirmSwitch = true
	case "P":
		p.acct = 1
		p.acctCur = 0
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
			return func() tea.Msg { return opDoneMsg{Op: "切换路由", Err: err} }
		}
		p.presetText, p.presetErr, p.confirmPreset = text, nil, idx
	case "g":
		p.cycleGroup()
	}
	return nil
}

// handleValidated submits a preset the backend parser accepted; a rejection
// (only possible when the group name cannot be interpolated) is surfaced as
// a toast, since the home page has no detail pane.
func (p *homePage) handleValidated(msg presetValidatedMsg, d driver.Driver) tea.Cmd {
	if msg.Err != nil {
		return func() tea.Msg { return opDoneMsg{Op: "切换路由", Err: msg.Err} }
	}
	return configTextCmd(d, msg.Section, msg.ID, msg.Text, msg.Label)
}

// acctKey drives the account menu (P), the password form and the logout
// confirmation.
func (p *homePage) acctKey(msg tea.KeyMsg, d driver.Driver) tea.Cmd {
	switch p.acct {
	case 1: // menu
		switch msg.String() {
		case "esc":
			p.acct = 0
		case "j", "down":
			if p.acctCur < 1 {
				p.acctCur++
			}
		case "k", "up":
			if p.acctCur > 0 {
				p.acctCur--
			}
		case "enter":
			if p.acctCur == 0 {
				p.acct = 2
				p.pwFocus = 0
				p.pwErr = ""
				p.pwCur.SetValue("")
				p.pwNew.SetValue("")
				p.pwRepeat.SetValue("")
				p.pwCur.Focus()
				p.pwNew.Blur()
				p.pwRepeat.Blur()
				return textinput.Blink
			}
			p.acct = 3
		}
		return nil

	case 2: // password form
		switch msg.String() {
		case "esc":
			p.acct = 1
			p.pwCur.Blur()
			p.pwNew.Blur()
			p.pwRepeat.Blur()
			return nil
		case "tab", "shift+tab", "up", "down":
			delta := 1
			if msg.String() == "shift+tab" || msg.String() == "up" {
				delta = -1
			}
			p.pwFocus = (p.pwFocus + delta + 3) % 3
			p.setPwFocus()
			return textinput.Blink
		case "enter":
			cur := p.pwCur.Value()
			nw := p.pwNew.Value()
			if cur == "" || nw == "" {
				p.pwErr = "当前密码和新密码不能为空"
				return nil
			}
			if nw != p.pwRepeat.Value() {
				p.pwErr = "两次输入的新密码不一致"
				return nil
			}
			if len(nw) < 6 {
				p.pwErr = "新密码至少 6 位"
				return nil
			}
			p.pwErr = ""
			p.acct = 0
			p.pwCur.Blur()
			p.pwNew.Blur()
			p.pwRepeat.Blur()
			return passwordCmd(d, cur, nw)
		}
		var cmd tea.Cmd
		switch p.pwFocus {
		case 0:
			p.pwCur, cmd = p.pwCur.Update(msg)
		case 1:
			p.pwNew, cmd = p.pwNew.Update(msg)
		case 2:
			p.pwRepeat, cmd = p.pwRepeat.Update(msg)
		}
		return cmd

	case 3: // logout confirm
		switch msg.String() {
		case "y":
			p.acct = 0
			return func() tea.Msg { return logoutMsg{} }
		case "n", "esc":
			p.acct = 1
		}
		return nil
	}
	return nil
}

func (p *homePage) setPwFocus() {
	p.pwCur.Blur()
	p.pwNew.Blur()
	p.pwRepeat.Blur()
	switch p.pwFocus {
	case 0:
		p.pwCur.Focus()
	case 1:
		p.pwNew.Focus()
	case 2:
		p.pwRepeat.Focus()
	}
}

// acctSection renders the account UI inside the home page.
func (p homePage) acctSection() string {
	switch p.acct {
	case 1:
		lines := []string{ui.TitleStyle.Render(" 账户"), "",
			ui.HelpStyle.Render(" 当前用户  " + p.user), ""}
		items := []string{"修改密码", "退出登录"}
		for i, it := range items {
			mark, style := "  ", ui.HelpStyle
			if i == p.acctCur {
				mark, style = "❯ ", ui.CursorStyle
			}
			lines = append(lines, style.Render(mark+it))
		}
		return strings.Join(lines, "\n") + "\n" +
			ui.HelpStyle.Render(" j/k 选择  Enter 确认  esc 返回") + "\n"
	case 2:
		var b strings.Builder
		b.WriteString(ui.TitleStyle.Render(" 修改密码") + "\n\n")
		for i, f := range []struct {
			label string
			input textinput.Model
		}{
			{"当前密码", p.pwCur}, {"新密码", p.pwNew}, {"确认新密码", p.pwRepeat},
		} {
			style := ui.HelpStyle
			if i == p.pwFocus {
				style = ui.SelectedStyle
			}
			b.WriteString(style.Render(" "+ui.PadRight(f.label, 10)) + " " + f.input.View() + "\n")
		}
		if p.pwErr != "" {
			b.WriteString("\n" + ui.ErrorStyle.Render(" ✗ "+p.pwErr) + "\n")
		}
		b.WriteString("\n" + ui.HelpStyle.Render(" Tab 切换  Enter 提交  esc 返回") + "\n")
		return b.String()
	case 3:
		return ui.ErrorStyle.Render(" ▸ 确认退出登录? 将清除本机保存的密码与 token (y/n)") + "\n"
	}
	return ""
}

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
		return l
	}
	return id
}

// isBuiltinOutbound reports whether a routing target is a dae built-in
// rather than a user group.
func isBuiltinOutbound(target string) bool {
	switch target {
	case "direct", "must_direct", "block", "must_proxy":
		return true
	}
	return false
}

// currentNode describes what a group is using right now.
func (p *homePage) currentNode(g driver.Group) (label string, style lipgloss.Style) {
	if sel := g.SelectedNode(); sel != nil && g.Policy == "fixed" {
		s := "手动: " + ui.SpaceAfterFlag(sel.Name)
		if l, ok := p.lat[sel.ID]; ok && l.Alive && l.Ms > 0 {
			s += fmt.Sprintf("  (%dms)", l.Ms)
		}
		return s, ui.OKStyle
	}
	// Auto policy: the actual pick lives inside the dae core and is not
	// exposed via the API — show the best-latency node as an estimate.
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
	}
	if best != "" {
		return fmt.Sprintf("%s ≈ %s  (%dms)", auto, best, bestMs), ui.SelectedStyle
	}
	return auto, ui.HelpStyle
}

func (p homePage) View(status driver.Status) string {
	var b strings.Builder

	// --- status & switch line ---
	run := ui.OKStyle.Render("● 代理运行中")
	hint := ui.HelpStyle.Render("  o 停止")
	if !status.Running {
		run = ui.ErrorStyle.Render("○ 代理已停止")
		hint = ui.HelpStyle.Render("  o 启动")
	}
	mod := ""
	if status.Modified {
		mod = ui.ErrorStyle.Render("  ⚠ 配置改动未应用（4 配置页 A 应用）")
	}
	b.WriteString(" " + run + hint + ui.HelpStyle.Render("   dae "+status.Version) + mod + "\n")

	if p.confirmSwitch {
		verb := "启动"
		if status.Running {
			verb = "停止"
		}
		b.WriteString(ui.ErrorStyle.Render(" ▸ 确认"+verb+"代理? (y/n)") + "\n")
	}
	if acct := p.acctSection(); acct != "" {
		b.WriteString(acct)
	}
	b.WriteString("\n")

	// --- traffic charts (smaller than the old full page) ---
	chartW := max0(p.width/2 - 16)
	if chartW < 20 {
		chartW = 20
	}
	chartH := 4
	green := lipgloss.NewStyle().Foreground(ui.Green)
	yellow := lipgloss.NewStyle().Foreground(ui.Yellow)
	s := p.snap
	up := ui.Sparkline(s.UpSeries, chartW, chartH, green, "↑")
	down := ui.Sparkline(s.DownSeries, chartW, chartH, yellow, "↓")
	left := "↑ 上行  " + green.Render(ui.Rate(s.UpRate)) + "\n" + up +
		"\n\n↓ 下行  " + yellow.Render(ui.Rate(s.DownRate)) + "\n" + down
	right := "连接 " + strconv.Itoa(s.Conns) + "   UDP " + strconv.Itoa(s.UDPSessions) +
		"\n累计 ↑ " + ui.Bytes(s.UpTotal) + "\n累计 ↓ " + ui.Bytes(s.DownTotal) +
		"\n\n" + ui.HelpStyle.Render("每秒自动刷新 (runtimeOverview)")
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, left, "    ", right))
	b.WriteString("\n\n")

	// --- network state ---
	if net := p.netSection(); net != "" {
		b.WriteString(net)
		b.WriteString("\n")
	}

	// --- routing quick-switch ---
	b.WriteString(p.routingSection())

	// --- current node per group ---
	b.WriteString("\n" + ui.TitleStyle.Render(" 各组当前节点") + "\n")
	if len(p.groups) == 0 {
		b.WriteString(ui.HelpStyle.Render(" （加载中…）") + "\n")
	}
	for _, g := range p.groups {
		label, style := p.currentNode(g)
		b.WriteString("  " + ui.PadRight(g.Name, 16) + style.Render(label) + "\n")
	}

	return b.String()
}

// routingSection is the home page's preset picker: the routing profile the
// presets replace, the proxy group they target, and — while confirming — the
// exact DSL that y would write.
func (p homePage) routingSection() string {
	if len(p.presets) == 0 {
		return ""
	}
	var b strings.Builder
	head := ui.TitleStyle.Render(" 路由快速切换") + ui.HelpStyle.Render("  "+p.routingName)
	if g := p.proxyGroup(); g != "" {
		head += ui.HelpStyle.Render("   代理组: " + g + " (g 换)")
	}
	b.WriteString(head + "\n")
	if p.routingID == "" {
		b.WriteString(ui.HelpStyle.Render(" （无路由方案：在 5 配置页创建后可用）") + "\n")
		return b.String()
	}
	if p.proxyGroup() == "" {
		b.WriteString(ui.HelpStyle.Render(" （无群组：预设需要一个代理组，请在 2 群组页创建）") + "\n")
	}
	for i, preset := range p.presets {
		cursor := " "
		if i == p.presetCursor {
			cursor = ui.CursorStyle.Render("❯")
		}
		mark := "  "
		if preset.ID == p.routingMode {
			mark = ui.OKStyle.Render("● ")
		}
		style := ui.HelpStyle
		if i == p.presetCursor {
			style = ui.CursorStyle
		}
		b.WriteString(cursor + " " + mark + style.Render(ui.PadRight(presetLabel(preset.ID), 14)) +
			ui.HelpStyle.Render(presetDescs[preset.ID]) + "\n")
	}
	switch {
	case p.confirmPreset >= 0 && p.presetErr != nil:
		b.WriteString(ui.ErrorStyle.Render(" ✗ 无法生成: "+shortErr(p.presetErr)) + "\n")
	case p.confirmPreset >= 0:
		b.WriteString(ui.ErrorStyle.Render(" ▸ 将路由方案 "+p.routingName+" 替换为「"+
			presetLabel(p.presets[p.confirmPreset].ID)+"」? (y 确认 / n 取消, g 换组)") + "\n")
		for _, l := range strings.Split(p.presetText, "\n") {
			b.WriteString(ui.HelpStyle.Render("    "+l) + "\n")
		}
	case p.routingMode != "":
		b.WriteString(ui.HelpStyle.Render(" 当前: "+presetLabel(p.routingMode)+"   Enter 切换") + "\n")
	default:
		b.WriteString(ui.HelpStyle.Render(" 当前: 自定义规则   Enter 切换为预设") + "\n")
	}
	return b.String()
}
