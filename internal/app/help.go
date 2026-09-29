package app

import (
	"dae-tui/internal/i18n"

	"fmt"

	"dae-tui/internal/ui"
)

// helpSection is one titled block of the help overlay. The table is the
// single source of truth for both the renderer and the open-at-current-page
// locator — two hand-kept lists would drift the moment a section grows.
type helpSection struct {
	title string
	rows  [][2]string
}

var helpSections = []helpSection{
	{"全局", [][2]string{
		{"1", "首页：开关/流量/各组当前节点"},
		{"2/3/4/5", "群组 · 订阅 · 手动节点 · 配置（均为 左列表 + 右详情）"},
		{"Tab/l/h", "在左右两栏之间切换焦点（配置页的 Tab 在左栏三个分区盒间切换）"},
		{"A r q", "重载(run，全局) · 刷新(重拉全部列表数据) · 退出"},
		{"?", "帮助浮窗（任意页面按 ? 打开，j/k 滚动，esc 关闭）"},
		{"P", "设置浮窗（任意页面）：账户、主题、语言、快捷键与版本信息"},
		{"-", "键位可改：P → 快捷键 选中动作 Enter 改键（d 恢复默认）；也可直接编辑 ~/.config/dae-tui/keys.toml 后按 r 重载"},
		{"-", "测速按需触发（t/T，只测当前列表/组/订阅）；流量每秒刷新"},
		{"-", "延迟数据每 3 秒轮询当前页可见节点"},
	}},
	{"首页", [][2]string{
		{"s", "启动/停止代理 (run；停止=dry，需 y 确认)"},
		{"j/k + Enter", "路由快速切换：选预设替换当前路由方案（先展示将写入的 DSL，需 y 确认）"},
		{"Tab", "在「路由快速切换」与「各组当前节点」之间切换焦点"},
		{"Enter(组行)", "跳到群组页并定位该组——切换某组节点的最短路径（固定节点见群组页 f）"},
		{"g", "切换预设使用的代理组（默认取当前路由已在用的组；固定节点专用组不参与）"},
		{"x", "解除固定节点（固定中才响应；路由引用改回原组，固定组与节点保留）"},
		{"S(组行)", "把路由正在用的组切换为该组（确认框报引用处数；固定中会连带解除，切到固定组则复用其节点重建固定，A 重载生效）"},
		{"●", "组行前的绿点 = 该组被当前路由方案引用（即流量正在走的组）"},
		{"L", "查看 daed 日志（应用内浮窗：j/k 滚动、f 跟随开关、r 重载、q 关闭；仅 daed 跑在本机 systemd 时可用）"},
		{"r", "刷新状态/流量/组"},
		{"-", "自动策略组显示 ≈ 已测最优节点；没有测量数据时标注「未测速」（估算值，不含毫秒——逐节点延迟去群组页看）"},
		{"-", "预设只覆盖规则，切换后需 A 重载才生效；自定义规则显示为「自定义规则」"},
		{"-", "环境盒显示订阅摘要、选中 DNS 方案及其上游（逐行）、各网卡地址/默认路由/网关；配置里写了的网卡不存在时会告警"},
		{"-", "「规则速览」= 选中路由的解析规则摘要（引用的群组不存在时 ⚠ 点名），装不下时尾部给「其余 N 条」；全量规则在配置页"},
		{"-", "宽终端下流量图与路由切换并排两栏，窄终端上下堆叠"},
		{"-", "流量：速率行末尾的「峰值」= 该方向近 10s 窗口的最高速率，图就画在它下面"},
		{"-", "「API Nms」= GraphQL 往返耗时，SSH 隧道卡顿时会变大；累计自 daed 启动起算"},
	}},
	{"群组页", [][2]string{
		{"j/k ↑↓", "左栏移动组：右栏上下两盒跟随显示该组信息与节点分区"},
		{"Tab/l/Enter", "切到右栏：上盒为组信息（策略/成员/引用），下盒为 订阅/直接添加节点 分区"},
		{"j/k (右栏)", "移动节点，窗口自动滚动"},
		{"Enter(分区)", "展开/收起该订阅或节点分区（默认收起）"},
		{"space", "标记/取消标记节点：t 只测已标记、x 批量移除；选择器内 Enter 批量添加"},
		{"c / R / D", "创建群组 / 重命名 / 删除（确认）"},
		{"p / a", "修改群组策略 / 快捷切回自动策略"},
		{"f(节点行)", "固定该节点：专用固定组承担 fixed 策略，本组成员/订阅原样保留；路由中本组引用改指固定组（先计数、需 y 确认、A 重载生效）；解除在首页 x"},
		{"S", "把路由正在用的组切换为当前查看的组（左右两栏均可；确认框报引用处数，固定中会连带解除，切到固定组则复用其节点重建固定，A 重载生效）"},
		{"t / T", "测速：整组或订阅 / 仅选中节点"},
		{"s / n / x", "挂订阅 / 加节点(手动+订阅内节点) / 移除订阅或节点"},
		{"●", "节点行的 ● = 当前组实际使用的节点（fixed 组只读显示）；左栏组行的 ● = 该组被当前路由方案引用"},
		{"-", "组被路由方案引用时，R/D 确认框会点名——改名/删除会让那些规则静默失效"},
		{"-", "固定节点专用组（默认名 pinned）由 TUI 管理：改名/删除/挂订阅等操作会被拒绝，换固定节点去来源组按 f、复用固定组按 S 切到它、解除去首页按 x"},
	}},
	{"订阅页", [][2]string{
		{"Tab/l/Enter", "切到右栏：上盒为订阅详情，下盒为全部节点（选中即自动拉取）"},
		{"右栏聚焦", "上盒改显选中节点（名称/协议/地址/延迟），Tab 回左栏恢复订阅卡"},
		{"t / T", "测速：全部可见节点 / 仅选中节点"},
		{"y", "复制链接（OSC 52，SSH 可用）：右栏=节点链接，否则=订阅链接"},
		{"u / n / x / c", "更新 / 新增 / 删除（确认）/ 定时刷新 cron 编辑"},
		{"e", "编辑订阅标签与链接（保留 ID：群组挂载关系不会丢；改链接不重新拉节点）"},
	}},
	{"手动节点页", [][2]string{
		{"a", "批量导入：每行一个分享链接，可整段粘贴；坏链接逐条报错，不影响其他链接"},
		{"e", "编辑节点标签与链接（保留 ID：删了重导会丢掉群组挂载）"},
		{"x", "删除选中节点（确认）"},
		{"t / T", "全部 / 单节点测速"},
		{"y", "复制节点分享链接到剪贴板（OSC 52）"},
		{"-", "详情页「趋势」是该节点最近约 3 分钟的延迟曲线，能看出是否在持续变慢"},
		{"Tab 后 G", "把选中节点加入某个群组"},
	}},
	{"配置页", [][2]string{
		{"Tab / shift+Tab", "在左栏三个分区盒（全局配置/DNS/路由规则）间循环切换，每盒记住自己的光标"},
		{"j/k", "在当前分区盒内移动"},
		{"l/Enter", "进入右栏查看内容（全局配置=可点选的字段表 / DSL 原文等）；h/esc 返回左栏"},
		{"Enter", "切换选中的 config/dns/routing"},
		{"c / R / D", "新建 / 重命名 / 删除（确认）当前分区的条目"},
		{"e", "config 进右栏字段表逐字段修改（j/k 选中、Enter 编辑，浮窗含默认值与说明、按类型预校验）；DNS/路由默认调 $EDITOR 改 DSL（config.toml 里 editor = \"builtin\" 换内置浮窗编辑器），先后端语法校验、再展示 diff，y 确认后才提交"},
		{"v", "DNS/路由右栏切换：DSL 原文 ↔ 解析后的结构概览（规则清单）"},
		{"y", "复制当前方案 DSL 到剪贴板（OSC 52）"},
		{"A (全局)", "重载 (run，需确认)：方案切换、订阅更新、群组改动都要重载才生效；停止代理在首页 s"},
	}},
	{"节点列表（群组/订阅/手动节点页通用）", [][2]string{
		{"/", "打开过滤框：按 名称/协议/标签/地址 子串实时过滤（不区分大小写）"},
		{"enter", "关闭过滤框并保留过滤结果（之后 j/k 在结果里移动）"},
		{"esc", "过滤框内按 esc 关闭并清空过滤"},
		{"o", "循环排序：默认顺序 → 延迟↑ → 延迟↓（未测/死亡节点始终排最后）"},
		{"t", "测速只测当前可见的节点（过滤后剩下几个就测几个）"},
		{"-", "延迟列 = 微型条 + 毫秒值（同色阶，500ms 打满）；未测显示 -，死亡显示 超时"},
	}},
	{"鼠标", [][2]string{
		{"滚轮", "滚动当前列表 / 页面（等价 j/k，每格 3 行）"},
		{"点击页签", "切换页面；点击左栏行选中该项"},
		{"双击行", "等同 Enter（群组/订阅/手动节点页的列表；配置页与首页预设的 Enter 有副作用，不响应双击）"},
	}},
	{"关于", [][2]string{
		{"后端", "daed GraphQL API (v2.1.1, 项目已归档、schema 冻结)"},
		{"固定节点", "专用固定组方案：不重组来源组，路由改指/改回（群组页 f、首页 x）；复用固定组=按 S 切到它；fixed 组在 daed 里只允许一个成员，固定组因此常驻单节点"},
		{"配置文件", "~/.config/dae-tui/config.toml"},
		{"架构", "可插拔 driver：后续可加裸 dae / clash-api 后端"},
	}},
}

// helpKeyColW fits the widest key ("Tab / shift+Tab"); at 14 it truncated
// into "Tab / shift-…" and read as a broken entry.
const helpKeyColW = 16

// sectionLines renders one help section: the key column carries the accent
// (it is what the eye scans for), descriptions stay dim, and the "-"
// continuation rows keep the dim dash.
// sectionLines translates the section title and every description at render
// time — the table stores the Chinese source copy as the key (see i18n).
func sectionLines(s helpSection) []string {
	lines := []string{ui.SelectedStyle.Render(" " + i18n.T(s.title))}
	for _, r := range s.rows {
		// The key column holds CJK entries too (滚轮, 右栏聚焦, …) — they are
		// catalog keys like the descriptions.
		key := ui.SelectedStyle.Render(ui.PadRight(i18n.T(r[0]), helpKeyColW))
		if r[0] == "-" {
			key = ui.HelpStyle.Render(ui.PadRight(r[0], helpKeyColW))
		}
		lines = append(lines, "  "+key+ui.HelpStyle.Render(i18n.T(r[1])))
	}
	return append(lines, "")
}

// helpLines builds the full help block from the section table.
func helpLines() []string {
	lines := []string{ui.TitleStyle.Render(i18n.T(" dae-tui 帮助")), ""}
	for _, s := range helpSections {
		lines = append(lines, sectionLines(s)...)
	}
	return lines
}

// helpSectionStart returns the line offset of the current page's section in
// helpLines(), so ? opens the help already scrolled to the page at hand —
// same content, a much shorter trip. j/k still reaches everything.
func helpSectionStart(page int) int {
	idx := -1
	switch page {
	case pageHome:
		idx = 1
	case pageTree:
		idx = 2
	case pageSubs:
		idx = 3
	case pageNodes:
		idx = 4
	case pageConfigs:
		idx = 5
	}
	if idx < 0 {
		return 0
	}
	off := 2 // the header title and its blank line
	for i := 0; i < idx; i++ {
		off += len(helpSections[i].rows) + 2 // section title + trailing blank
	}
	return off
}

// helpWinBody is the number of help body lines the overlay shows for an
// avail-line body area: box borders, title, footer and breathing room take
// the rest.
func helpWinBody(avail int) int {
	n := avail - 8
	if n < 4 {
		n = 4
	}
	return n
}

// clampHelpScroll bounds the help overlay's scroll offset to its content.
func clampHelpScroll(scroll, win int) int {
	if max := len(helpLines()) - win; scroll > max {
		scroll = max
	}
	if scroll < 0 {
		scroll = 0
	}
	return scroll
}

// helpOverlayBox renders the help as a floating window over any page,
// windowed to what fits and with a position footer. The content width is
// capped well below the terminal so the page stays visible on both sides.
// helpMarginW keeps 8 columns of terminal on each side of the floating box;
// helpMaxW caps it on very wide terminals so descriptions stay readable.
const (
	helpMarginW = 16
	helpMaxW    = 96
)

func helpOverlayBox(w, avail, scroll int) string {
	lines := helpLines()
	win := helpWinBody(avail)
	scroll = clampHelpScroll(scroll, win)
	end := scroll + win
	if end > len(lines) {
		end = len(lines)
	}
	// Pre-truncate to the box's content width so a long row widens nothing
	// and loses only its own tail (overlayBox would stamp an ellipsis onto
	// every padded line otherwise).
	// helpLines already opens with its own title row; the box stays well
	// under the terminal width so the page peeks out on both sides.
	helpW := w - helpMarginW
	if helpW > helpMaxW {
		helpW = helpMaxW
	}
	body := make([]string, 0, end-scroll+2)
	for _, l := range lines[scroll:end] {
		body = append(body, ui.Truncate(l, helpW-6))
	}
	body = append(body, "",
		ui.HelpStyle.Render(fmt.Sprintf(i18n.T(" %d-%d / %d   j/k 滚动   esc 关闭"), scroll+1, end, len(lines))))
	return overlayBox(&overlaySpec{lines: body}, helpW)
}
