package app

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"dae-tui/internal/ui"
)

func helpView() string {
	section := func(title string, rows [][2]string) string {
		var b strings.Builder
		b.WriteString(ui.SelectedStyle.Render(" "+title) + "\n")
		for _, r := range rows {
			b.WriteString("  " + ui.PadRight(r[0], 14) + ui.HelpStyle.Render(r[1]) + "\n")
		}
		return b.String() + "\n"
	}

	var b strings.Builder
	b.WriteString(ui.TitleStyle.Render(" dae-tui 帮助") + "\n\n")
	b.WriteString(section("全局", [][2]string{
		{"1", "首页：开关/流量/各组当前节点"},
		{"2/3/4/5", "群组 · 订阅 · 手动节点 · 配置（均为 左列表 + 右详情）"},
		{"Tab/l/h", "在左右两栏之间切换焦点"},
		{"A r q", "应用配置(run，全局) · 刷新当前页 · 退出"},
		{"-", "启动时自动测速一次（t/T 手动再测）；延迟数据每 3 秒轮询，流量每秒刷新"},
	}))
	b.WriteString(section("首页", [][2]string{
		{"o", "启动/停止代理 (run；停止=dry，需 y 确认)"},
		{"j/k + Enter", "路由快速切换：选预设替换当前路由方案（先展示将写入的 DSL，需 y 确认）"},
		{"g", "切换预设使用的代理组（默认取当前路由已在用的组）"},
		{"r", "刷新状态/流量/组"},
		{"-", "自动策略组显示 ≈ 最优节点（按已测延迟估计）"},
		{"-", "预设只覆盖规则，切换后需 A 应用才生效；自定义规则显示为「自定义规则」"},
	}))
	b.WriteString(section("群组页", [][2]string{
		{"j/k ↑↓", "左栏移动组（默认折叠，不占屏）"},
		{"Tab/l/Enter", "展开群组：右侧显示 订阅/直接添加节点 分区"},
		{"j/k (右栏)", "移动节点，窗口自动滚动"},
		{"Enter(分区)", "展开/收起该订阅或节点分区（默认收起）"},
		{"c / R / D", "创建群组 / 重命名 / 删除（确认）"},
		{"p / a", "修改群组策略 / 快捷切回自动策略"},
		{"t / T", "测速：整组或订阅 / 仅选中节点"},
		{"s / n / x", "挂订阅 / 加节点(手动+订阅内节点) / 移除订阅或节点"},
		{"●", "当前组实际使用的节点（fixed 组只读显示）"},
	}))
	b.WriteString(section("订阅页", [][2]string{
		{"Tab/l/Enter", "右侧显示订阅详情与全部节点（自动拉取）"},
		{"t (右栏)", "对该订阅全部节点测速"},
		{"u / n / x / c", "更新 / 新增 / 删除（确认）/ 定时刷新 cron 编辑"},
	}))
	b.WriteString(section("手动节点页", [][2]string{
		{"a", "导入节点（粘贴分享链接，可带标签）"},
		{"x", "删除选中节点（确认）"},
		{"t / T", "全部 / 单节点测速"},
		{"Tab 后 G", "把选中节点加入某个群组"},
	}))
	b.WriteString(section("配置页", [][2]string{
		{"Tab/l", "进入右栏滚动查看内容（字段列表 / DSL 原文等）"},
		{"Enter", "切换选中的 config/dns/routing"},
		{"c / R / D", "新建 / 重命名 / 删除（确认）当前分区的条目"},
		{"e", "config 逐字段修改（全部字段，含默认值与说明）；DNS/路由调 $EDITOR 改 DSL，保存前过后端语法校验"},
		{"v", "DNS/路由右栏切换：DSL 原文 ↔ 解析后的结构概览（规则清单）"},
		{"A (全局)", "应用 (run，需确认)；停止代理在首页 o"},
	}))
	b.WriteString(section("关于", [][2]string{
		{"后端", "daed GraphQL API (v2.1.1, 项目已归档、schema 冻结)"},
		{"固定节点", "暂未提供：daed v2 的 fixed 组只允许一个成员，固定会静默重组整个组"},
		{"配置文件", "~/.config/dae-tui/config.toml"},
		{"架构", "可插拔 driver：后续可加裸 dae / clash-api 后端"},
	}))
	return lipgloss.NewStyle().Padding(0, 1).Render(strings.TrimRight(b.String(), "\n"))
}
