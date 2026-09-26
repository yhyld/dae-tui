package app

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

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

	// subs feeds the subscription digest — the same list the subs page
	// already fetched; the home page adds no poll of its own.
	subs []driver.Subscription

	// cfgName/dnsName are the selected config/dns profiles; with
	// routingName they make up the "what would A apply" line.
	cfgName string
	dnsName string

	// apiTook is the last traffic round-trip duration: over an SSH tunnel
	// it is the quickest hint at whether the tunnel or the backend is the
	// slow part.
	apiTook time.Duration

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

	// groupCursor/groupFocus drive the per-group node list at the bottom.
	// Tab moves focus between it and the routing picker; Enter on a group
	// row jumps to the groups page with that group expanded, which is the
	// short path to "switch this group's node" (a fixed-group pin is not
	// offered — see the groups page).
	groupCursor int
	groupFocus  bool

	// The home page is taller than many terminals; scroll keeps the
	// "active" line (the cursor row, or an open confirmation) on screen.
	// follow re-centers on the active line after every keystroke; wheel
	// scrolling temporarily turns it off so the user can look around.
	scroll int
	follow bool

	width, height int

	caps driver.Caps
}

func newHomePage(caps driver.Caps) homePage {
	p := homePage{lat: map[string]driver.Latency{}, confirmPreset: -1, caps: caps, follow: true}
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
	if p.groupCursor >= len(p.groups) {
		p.groupCursor = max0(len(p.groups) - 1)
	}
	p.rederiveGroupIdx()
}

// setSubs feeds the subscription digest. On error the last known list is
// kept — the subs page is where fetch failures belong.
func (p *homePage) setSubs(subs []driver.Subscription, err error) {
	if err != nil {
		return
	}
	p.subs = subs
}

// subLines is the subscription digest — the 环境 zone's headline row: how
// many subscriptions, how many nodes they bring, and how fresh the latest
// update is. A subscription whose cron is on but whose last update is over
// a day old is the classic silent killer of "the proxy got slow", so it
// gets a flag of its own.
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
		fresh = "从未"
	}
	lines := []string{" " + fmt.Sprintf("订阅 %d · 节点 %d · 最近更新 ", len(p.subs), nodes) +
		ui.SelectedStyle.Render(fresh)}
	for _, s := range p.subs {
		if s.CronEnable && !s.UpdatedAt.IsZero() && time.Since(s.UpdatedAt) > 24*time.Hour {
			tag := s.Tag
			if tag == "" {
				tag = s.ID
			}
			lines = append(lines, " "+lipgloss.NewStyle().Foreground(ui.Yellow).Render(
				"⚠ "+tag+" 上次更新 "+ui.TimeAgo(s.UpdatedAt)+"，定时刷新已开启"))
		}
	}
	return lines
}

// groupHealth summarizes a group's measured nodes as "alive/measured · age".
// It deliberately uses only data other pages already polled — the home page
// adds no latency poll of its own — which is why the age rides along: a
// stale count must not read as the current state.
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
		return ui.HelpStyle.Render("未测速")
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
		p.cfgName = c.Name
		break
	}
	for _, d := range sel.Dns {
		if d.Selected {
			p.dnsName = d.Name
			break
		}
	}
	for _, r := range sel.Routings {
		if !r.Selected {
			continue
		}
		p.routingID, p.routingName, p.routingBody = r.ID, r.Name, r.Body
		p.routingMode = d.DetectRoutingPreset(r.Body)
		// Aim the preset cursor at the mode actually in effect, so the
		// highlight lands on reality on first paint instead of preset #0.
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
func (p homePage) netLines(w int) []string {
	if len(p.ifaces) == 0 {
		return nil
	}
	// One NIC per line with a hanging indent: the joined form truncated
	// mid-IP and hid exactly the addresses this section exists to show.
	lines := make([]string, 0, len(p.ifaces))
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
			s += " 默认路由"
		}
		label := ui.HelpStyle.Render("网卡 ")
		if len(lines) > 0 {
			label = "     " // hang under the 网卡 label (4 cols + space)
		}
		lines = append(lines, " "+label+ui.Truncate(s, max0(w-6)))
	}
	lines = append(lines, netWarnLines(p)...)
	return lines
}

// netWarnLines flags configured lan/wan interfaces that daed cannot see —
// it binds them by name, so a renamed NIC silently breaks the proxy.
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
			lines = append(lines, " "+ui.ErrorStyle.Render("⚠ 配置的 "+w.label+" 接口 "+n+
				" 不存在（daed 按网卡名绑定，DHCP 改名或换网口后需更新配置）"))
		}
	}
	return lines
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
	// Any keystroke re-arms cursor-follow scrolling: the keyboard user's
	// context is the active line, so the window snaps back to it.
	p.follow = true
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
	// Focus on the per-group node list: j/k moves, Enter jumps to the groups
	// page with that group expanded. Only navigation keys are claimed here —
	// o/P/L and friends still work while the list has focus.
	if p.groupFocus {
		switch msg.String() {
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
	switch msg.String() {
	case "o":
		p.confirmSwitch = true
	case "L":
		return logsCmd()
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
			if !strongEnough(nw) {
				p.pwErr = "新密码至少 6 位，且需包含字母和数字"
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

// logsCmd opens the daed journal in a pager-less follow view. tea.ExecProcess
// hands the real terminal to the child and restores the TUI afterwards; the
// view is read-only and local — journalctl lives on this machine, so it only
// works when daed runs here rather than behind an SSH tunnel. q or ctrl+c
// leaves the viewer.
func logsCmd() tea.Cmd {
	if _, err := exec.LookPath("journalctl"); err != nil {
		return func() tea.Msg {
			return opDoneMsg{Op: "查看日志", Err: errors.New("未找到 journalctl（daed 需以 systemd 服务运行在本机）")}
		}
	}
	c := exec.Command("journalctl", "-u", "daed", "-n", "200", "--no-pager", "-f")
	return tea.ExecProcess(c, func(err error) tea.Msg { return logsDoneMsg{} })
}

// homeTwoColMin is the content width at which the home page lays the
// traffic block and the routing picker side by side instead of stacked.
const homeTwoColMin = 96

// homeRoutingW is the routing column's width in the two-column layout:
// cursor + mark + a 14-cell label + a CJK description fits in 44.
const homeRoutingW = 44

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

// currentNode describes what a group is using right now. Fixed groups are
// exact — daed stores the pinned index in policyParams. Automatic policies
// are an estimate: the actual pick lives inside the dae core and is not
// exposed via the API, so the best-measured member stands in for it. Neither
// carries a millisecond figure: keeping one fresh for every group would mean
// polling every member of every group, which is the full-instance cost the
// per-page latency poll exists to avoid (the groups page shows per-node
// latencies for the group you are actually looking at).
func (p *homePage) currentNode(g driver.Group) (label string, style lipgloss.Style) {
	if sel := g.SelectedNode(); sel != nil && g.Policy == "fixed" {
		return "手动: " + ui.SpaceAfterFlag(sel.Name), ui.OKStyle
	}
	// Auto policy: show the best-latency node as an estimate.
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
		return fmt.Sprintf("%s ≈ %s", auto, best), ui.SelectedStyle
	}
	// No member has been measured: say so instead of showing a bare policy
	// label that reads like a rendering gap. Measurements are created on
	// demand (t/T on the groups page).
	return auto + " · 未测速", ui.HelpStyle
}

// overlay returns the home page's floating windows: the account menu /
// password form / logout confirmation and the routing-preset confirmation
// with its DSL preview. The small proxy on/off confirmation stays inline.
func (p homePage) overlay() *overlaySpec {
	switch {
	case p.acct == 1:
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
		return &overlaySpec{lines: append(lines,
			ui.HelpStyle.Render(" j/k 选择  Enter 确认  esc 返回"))}
	case p.acct == 2:
		lines := []string{ui.TitleStyle.Render(" 修改密码"), ""}
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
			lines = append(lines, style.Render(" "+ui.PadRight(f.label, 10))+" "+f.input.View())
		}
		if p.pwErr != "" {
			lines = append(lines, "", ui.ErrorStyle.Render(" ✗ "+p.pwErr))
		}
		return &overlaySpec{lines: append(lines, "",
			ui.HelpStyle.Render(" Tab 切换  Enter 提交  esc 返回"))}
	case p.acct == 3:
		return &overlaySpec{destructive: true, lines: []string{
			"确认退出登录?",
			"将清除本机保存的密码与 token",
			"",
			ui.OKStyle.Render(" y 确认") + "    " + ui.ErrorStyle.Render("n / esc 取消"),
		}}
	case p.confirmPreset >= 0 && p.presetErr != nil:
		return &overlaySpec{destructive: true, lines: []string{
			ui.ErrorStyle.Render(" ✗ 无法生成: " + shortErr(p.presetErr)),
			"",
			ui.HelpStyle.Render(" n / esc 关闭"),
		}}
	case p.confirmPreset >= 0:
		lines := []string{
			ui.TitleStyle.Render(" 切换路由方案"),
			"",
			"将把 " + p.routingName + " 替换为「" +
				presetLabel(p.presets[p.confirmPreset].ID) + "」",
			ui.HelpStyle.Render(" 代理组: " + p.proxyGroup() + "  (g 换组)"),
			"",
			ui.SelectedStyle.Render(" 将写入的 DSL:"),
		}
		for i, l := range strings.Split(p.presetText, "\n") {
			if i >= 14 {
				lines = append(lines, ui.HelpStyle.Render("    …"))
				break
			}
			lines = append(lines, ui.HelpStyle.Render("    "+l))
		}
		return &overlaySpec{destructive: true, lines: append(lines, "",
			ui.OKStyle.Render(" y 确认")+"    "+ui.ErrorStyle.Render("n / esc 取消")+"    "+
				ui.HelpStyle.Render("g 换组"))}
	}
	return nil
}

// scrollBy moves the window by d lines and detaches it from the cursor
// (follow re-arms on the next keystroke). The wheel calls this.
func (p *homePage) scrollBy(d int) {
	p.scroll += d
	p.follow = false
}

// bodyLines renders the home page as a flat line list plus the index of the
// active line — an open confirmation, the account menu, or the cursor row of
// whichever section holds focus. View windows the list so the active line
// stays on screen; that is the home page's scrolling.
// bodyLines lays the home page out as boxed zones — one box per topic,
// btop-style, on a strict grid: one vertical seam for the whole page
// (homeRoutingW), every row's boxes share top and bottom borders (the
// shorter side is padded inside its box), and zone rows sit flush against
// each other. The left column is the static state (代理, then 环境), the
// right column the live panels (流量, then 路由); the group list spans the
// full width below. Narrow terminals stack the boxes full-width instead.
// The returned active line drives the follow-scroll.
func (p homePage) bodyLines(status driver.Status) ([]string, int) {
	var lines []string
	active := -1
	twoCol := p.width >= homeTwoColMin
	const margin = 1 // keep the boxes off the app frame's border
	full := p.width - margin*2
	// Box widths already reserve the page-wide margins; the root model
	// stamps the left margin on every body line, so zones append bare.
	addZone := func(zone []string) {
		lines = append(lines, zone...)
	}
	// pairedRow builds one grid row through the shared PaneRow helper: both
	// boxes' content padded to the same height so their borders align, then
	// joined at the page seam. Each side carries its own keys in its bottom
	// edge.
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

	// --- zone row 1: proxy state (left) | traffic (right) ---
	onOff := "o 停止"
	if !status.Running {
		onOff = "o 启动"
	}
	if twoCol {
		addZone(pairedRow("代理", onOff, p.proxyRows(status, homeRoutingW-4),
			"流量", "", p.trafficRows(p.width-margin*2-homeRoutingW-1-4), false))
	} else {
		addZone(ui.TitledBoxFooter("代理", onOff, false, full, p.proxyRows(status, full-4)))
		addZone(ui.TitledBox("流量", false, full, p.trafficRows(full-4)))
	}

	// The proxy on/off confirmation interrupts between the zones.
	if p.confirmSwitch {
		verb := "启动"
		if status.Running {
			verb = "停止"
		}
		box := ui.BoxLines(true, "确认"+verb+"代理?  (y/n)")
		addZone(box)
		active = len(lines) - 1
	}

	// --- zone row 2: environment digest (left) | routing presets (right) ---
	routingBoxW := full
	if twoCol {
		routingBoxW = p.width - margin*2 - homeRoutingW - 1
	}
	routingBody, presetOff := p.routingLines(routingBoxW - 4)
	// +1: the first content row sits under the box's top border.
	presetStart := -1
	row2Start := len(lines)
	if presetOff >= 0 {
		presetStart = row2Start + 1 + presetOff
	}
	envW := full - 4
	if twoCol {
		envW = homeRoutingW - 4
	}
	env := append(p.subLines(), p.netLines(envW)...)
	if twoCol && len(env) > 0 {
		addZone(pairedRow("环境", "", env, "路由", "Enter 切换 · g 换组", routingBody, !p.groupFocus))
	} else {
		addZone(ui.TitledBoxFooter("路由", "Enter 切换 · g 换组", !p.groupFocus, routingBoxW, routingBody))
		if len(env) > 0 {
			addZone(ui.TitledBox("环境", false, full, env))
		}
	}

	// --- zone: current node per group ---
	groupRows := make([]string, 0, len(p.groups)+2)
	if len(p.groups) == 0 {
		groupRows = append(groupRows, " "+ui.HelpStyle.Render("（加载中…）"))
	}
	cw := full - 4
	for i, g := range p.groups {
		label, style := p.currentNode(g)
		cursor := " "
		if i == p.groupCursor && p.groupFocus {
			cursor = ui.CursorStyle.Render("❯")
		}
		row := " " + cursor + " " + ui.PadRight(g.Name, 16) + style.Render(label)
		// The health suffix is pinned to the box's right edge — a ragged
		// tail after the node label reads as noise, a column reads as a
		// column. Density only: on narrow boxes the node label wins.
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
	groupFooter := "Tab 切到组列表 · Enter 跳群组页"
	if p.groupFocus {
		groupFooter = "Enter 跳群组页并展开 · Tab 返回路由切换"
	}
	groupStart := len(lines) + 1 // under the box's top border
	addZone(ui.TitledBoxFooter("各组当前节点", groupFooter, p.groupFocus, full, groupRows))

	// Active-line priority: an open confirmation wins above; otherwise it
	// is the cursor row of the focused zone.
	if active < 0 && presetStart >= 0 && !p.groupFocus {
		active = presetStart + p.presetCursor
	}
	if active < 0 && p.groupFocus && groupStart+p.groupCursor < len(lines) {
		active = groupStart + p.groupCursor
	}
	return lines, active
}

// proxyRows is the 代理 zone's content: the on/off badge (its `o` key rides
// the box's bottom edge), the three profiles the global `A` would apply,
// and — when there is one — the unapplied-changes warning. Every row starts
// one cell in, on the badge's text baseline; the routing continuation
// aligns its label under `config`.
func (p homePage) proxyRows(status driver.Status, w int) []string {
	badge := lipgloss.NewStyle().Bold(true).
		Foreground(lipgloss.Color("15")).Background(ui.Green).
		Padding(0, 1).Render("● 代理运行中")
	if !status.Running {
		badge = lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color("15")).Background(ui.Red).
			Padding(0, 1).Render("○ 代理已停止")
	}
	rows := []string{badge, ""}
	if p.caps.ConfigMgmt {
		name := func(s string) string {
			if s == "" {
				return "（无）"
			}
			return s
		}
		// One profile per line with the keys column-aligned (8-col label
		// column): the joined form wrapped or truncated in narrow boxes.
		rows = append(rows,
			" "+ui.HelpStyle.Render("方案 config  ")+name(p.cfgName),
			"      "+ui.HelpStyle.Render("dns     ")+name(p.dnsName),
			"      "+ui.HelpStyle.Render("routing ")+name(p.routingName))
	}
	if status.Modified {
		rows = append(rows, " "+ui.ErrorStyle.Render("⚠ 配置改动未应用（A 应用）"))
	}
	return rows
}

// trafficRows is the 流量 zone's content (w = content width inside the
// box). The rates are the zone's headline — bold, colored, one per half of
// the box with each chart directly under its rate — while the counters and
// cumulative totals stay dim footnotes. The emphasis hierarchy is the
// point: what you glance at (speed) is bright and big, what you look up
// (totals) is quiet.
func (p homePage) trafficRows(w int) []string {
	if !p.caps.TrafficStats {
		return []string{ui.HelpStyle.Render("当前后端不支持流量统计")}
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
	up := ui.HelpStyle.Render("↑ 上行 ") + greenBold.Render(ui.Rate(s.UpRate))
	down := ui.HelpStyle.Render("↓ 下行 ") + yellowBold.Render(ui.Rate(s.DownRate))
	rates := " " + ui.PadRight(up, cw) + " " + down
	charts := lipgloss.JoinHorizontal(lipgloss.Top,
		ui.Sparkline(s.UpSeries, cw, chartH, green, ""),
		" ",
		ui.Sparkline(s.DownSeries, cw, chartH, yellow, ""))

	counters := ui.HelpStyle.Render(fmt.Sprintf("连接 %d · UDP %d", s.Conns, s.UDPSessions))
	if p.apiTook > 0 {
		counters += ui.HelpStyle.Render(fmt.Sprintf(" · API %dms", p.apiTook.Milliseconds()))
	}
	// The cumulative counters reset with the dae core, not monthly.
	total := ui.HelpStyle.Render("累计 ") + green.Render("↑ "+ui.Bytes(s.UpTotal)) +
		ui.HelpStyle.Render(" · ") + yellow.Render("↓ "+ui.Bytes(s.DownTotal)) +
		ui.HelpStyle.Render(" · 自 daed 启动")

	rows := []string{rates}
	for _, l := range strings.Split(charts, "\n") {
		rows = append(rows, " "+l)
	}
	return append(rows, " "+counters, " "+total)
}

// routingLines renders the 路由 zone's content: the current profile and
// proxy group, the presets, and the current-mode footer. It returns the
// rows and the index of the first preset row within them (the switch
// confirmation floats, see overlay).
func (p homePage) routingLines(w int) (body []string, presetStart int) {
	if len(p.presets) == 0 {
		return nil, -1
	}
	name := p.routingName
	if name == "" {
		name = "（无）"
	}
	head := ui.HelpStyle.Render("当前 ") + ui.SelectedStyle.Render(name)
	if g := p.proxyGroup(); g != "" {
		head += ui.HelpStyle.Render(" · 代理组: ") + ui.SelectedStyle.Render(g)
	}
	body = append(body, " "+head)
	if p.routingID == "" {
		body = append(body, " "+ui.HelpStyle.Render("（无路由方案：在 5 配置页创建后可用）"))
	} else if p.proxyGroup() == "" {
		body = append(body, " "+ui.HelpStyle.Render("（无群组：预设需要一个代理组，请在 2 群组页创建）"))
	}
	presetStart = len(body)
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
		body = append(body, " "+cursor+" "+mark+style.Render(ui.PadRight(presetLabel(preset.ID), 14))+
			ui.HelpStyle.Render(presetDescs[preset.ID]))
	}
	switch {
	case p.routingMode != "":
		body = append(body, " "+ui.HelpStyle.Render("当前: "+presetLabel(p.routingMode)))
	default:
		body = append(body, " "+ui.HelpStyle.Render("当前: 自定义规则"))
	}
	return body, presetStart
}

func (p homePage) View(status driver.Status) string {
	lines, active := p.bodyLines(status)
	h := p.height
	if h < 1 {
		h = 1
	}
	if p.follow && active >= 0 {
		if active < p.scroll {
			p.scroll = active
		}
		if active >= p.scroll+h {
			p.scroll = active - h + 1
		}
	}
	if p.scroll > len(lines)-h {
		p.scroll = len(lines) - h
	}
	if p.scroll < 0 {
		p.scroll = 0
	}
	end := p.scroll + h
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[p.scroll:end], "\n")
}
