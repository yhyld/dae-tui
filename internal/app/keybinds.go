package app

import (
	"dae-tui/internal/i18n"
	"dae-tui/internal/keymap"
)

// appKeys is the live binding set: loaded once in New from keys.toml (next
// to config.toml), reloaded with r inside the settings keys viewer. Pages
// reach it through tk/K — they are value types that never see the Model,
// so a package-level handle is the pragmatic wiring.
var appKeys = keymap.New()

// tk translates a pressed key into the canonical (default) name for the
// scope's dispatch switch: the remap layer stays out of the page code, the
// switches keep reading their default labels.
func tk(scope, pressed string) string {
	return appKeys.Translate(scope, pressed)
}

// K returns the key an action currently lives on — for footers, the frame
// help strip and the keys viewer, which must name the keys that work.
func K(scope, def string) string {
	return appKeys.Key(scope, def)
}

// loadKeys loads keys.toml from the config's directory at startup.
func loadKeys(cfgPath string) {
	appKeys = keymap.Load(keymap.Path(cfgPath))
}

// kb composes a footer fragment: the key an action currently lives on plus
// its translated label — "u 更新" renders as "ctrl+u update" under remap.
func kb(scope, def, label string) string {
	return K(scope, def) + " " + i18n.T(label)
}

// keyEntry is one row of the settings keys viewer: an action identified by
// its default key inside a scope, with a description. The order is the
// viewer's display order.
type keyEntry struct {
	scope, def, desc string
}

var keyCatalog = []keyEntry{
	// global
	{keymap.Global, "q", "退出"},
	{keymap.Global, "A", "重载 (run)"},
	{keymap.Global, "r", "全量刷新"},
	{keymap.Global, "P", "设置浮窗"},
	{keymap.Global, "?", "帮助浮窗"},
	{keymap.Global, "1", "切到首页"},
	{keymap.Global, "2", "切到群组"},
	{keymap.Global, "3", "切到订阅"},
	{keymap.Global, "4", "切到手动节点"},
	{keymap.Global, "5", "切到配置"},
	// home
	{keymap.Home, "s", "启动/停止代理"},
	{keymap.Home, "L", "查看 daed 日志"},
	{keymap.Home, "j", "移动光标"},
	{keymap.Home, "k", "移动光标"},
	{keymap.Home, "g", "切换预设的代理组"},
	// groups
	{keymap.Groups, "j", "移动"},
	{keymap.Groups, "k", "移动"},
	{keymap.Groups, "h", "返回左栏/收起"},
	{keymap.Groups, "l", "进入右栏"},
	{keymap.Groups, "c", "创建群组"},
	{keymap.Groups, "R", "重命名群组"},
	{keymap.Groups, "D", "删除群组"},
	{keymap.Groups, "p", "修改策略"},
	{keymap.Groups, "a", "切回自动策略"},
	{keymap.Groups, "t", "测速（组/订阅）"},
	{keymap.Groups, "T", "测速（选中）"},
	{keymap.Groups, "s", "挂订阅"},
	{keymap.Groups, "n", "加节点"},
	{keymap.Groups, "x", "移除订阅/节点"},
	{keymap.Groups, "o", "排序"},
	{keymap.Groups, "G", "加入群组"},
	// subs
	{keymap.Subs, "j", "移动"},
	{keymap.Subs, "k", "移动"},
	{keymap.Subs, "h", "返回左栏"},
	{keymap.Subs, "l", "进入右栏"},
	{keymap.Subs, "u", "更新订阅"},
	{keymap.Subs, "e", "编辑订阅"},
	{keymap.Subs, "n", "新增订阅"},
	{keymap.Subs, "x", "删除订阅"},
	{keymap.Subs, "c", "定时刷新 cron"},
	{keymap.Subs, "y", "复制链接"},
	{keymap.Subs, "t", "测速（全部可见）"},
	{keymap.Subs, "T", "测速（选中）"},
	{keymap.Subs, "o", "排序"},
	// nodes
	{keymap.Nodes, "j", "移动"},
	{keymap.Nodes, "k", "移动"},
	{keymap.Nodes, "h", "返回左栏"},
	{keymap.Nodes, "l", "进入右栏"},
	{keymap.Nodes, "a", "批量导入"},
	{keymap.Nodes, "e", "编辑节点"},
	{keymap.Nodes, "x", "删除节点"},
	{keymap.Nodes, "y", "复制链接"},
	{keymap.Nodes, "t", "测速（全部）"},
	{keymap.Nodes, "T", "测速（选中）"},
	{keymap.Nodes, "o", "排序"},
	{keymap.Nodes, "G", "加入群组"},
	// configs
	{keymap.Configs, "j", "移动"},
	{keymap.Configs, "k", "移动"},
	{keymap.Configs, "h", "返回左栏"},
	{keymap.Configs, "l", "进入右栏"},
	{keymap.Configs, "g", "移到盒首"},
	{keymap.Configs, "G", "移到盒尾"},
	{keymap.Configs, "c", "新建"},
	{keymap.Configs, "R", "重命名"},
	{keymap.Configs, "D", "删除"},
	{keymap.Configs, "e", "编辑"},
	{keymap.Configs, "v", "概览/原文"},
	{keymap.Configs, "y", "复制 DSL"},
}

// scopeTitle renders a scope's display name for the viewer.
func scopeTitle(scope string) string {
	switch scope {
	case keymap.Global:
		return i18n.T("全局")
	case keymap.Home:
		return i18n.T("首页")
	case keymap.Groups:
		return i18n.T("群组")
	case keymap.Subs:
		return i18n.T("订阅")
	case keymap.Nodes:
		return i18n.T("手动节点")
	case keymap.Configs:
		return i18n.T("配置")
	}
	return scope
}
