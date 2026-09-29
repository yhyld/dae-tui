package app

import (
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"dae-tui/internal/i18n"
	"dae-tui/internal/ui"
)

// The command palette (ctrl+p, k9s-style): a fuzzy list over the app's
// jump-and-switch surface — pages, groups, config/dns/routing profiles —
// reachable without leaving the current page or remembering which page
// hides the action. Deliberately read-only surface only: destructive or
// confirming actions (reload A, presets, pin, deletes) stay behind their
// own gates on their own pages — a palette hit is too easy to fire to be
// allowed to rewrite a routing DSL or restart the proxy.

// paletteItem is one palette row: a searchable title and the action Enter
// fires. run receives the live model pointer (the palette is handled from
// the root model, which owns drv/page).
type paletteItem struct {
	title string
	hint  string
	run   func(m *Model) tea.Cmd
}

type paletteState struct {
	open bool
	q    string
	cur  int
}

const paletteWin = 12

// buildPaletteItems snapshots the current groups and selections into items.
// Built on open (and cheap): the palette lives for seconds, so translating
// titles here follows the same rule as toasts.
func buildPaletteItems(m *Model) []paletteItem {
	items := []paletteItem{}
	for i, name := range tabLabels() {
		page := i
		items = append(items, paletteItem{
			title: i18n.T("跳到 ") + name,
			hint:  i18n.T("页面"),
			run: func(m *Model) tea.Cmd {
				m.page = page
				return nil
			},
		})
	}
	for _, g := range m.groups.groups {
		id := g.ID
		gn := g.Name
		items = append(items, paletteItem{
			title: i18n.T("群组 ") + gn,
			hint:  i18n.T("%d节点 · 跳到群组页", len(g.Members())),
			run: func(m *Model) tea.Cmd {
				return func() tea.Msg { return gotoGroupMsg{ID: id} }
			},
		})
	}
	sections := []struct{ section, label string }{
		{"config", "全局配置"},
		{"dns", "DNS"},
		{"routing", "路由"},
	}
	for _, sec := range sections {
		for _, it := range itemsOf(m.configs.sel, sec.section) {
			id, name, selected := it.ID, it.Name, it.Selected
			hint := i18n.T("切换")
			if selected {
				hint = i18n.T("当前生效")
			}
			items = append(items, paletteItem{
				title: sec.label + i18n.T("方案 ") + name,
				hint:  hint,
				run: func(m *Model) tea.Cmd {
					return selectCmd(m.drv, sec.section, id)
				},
			})
		}
	}
	return items
}

// paletteItems returns the items matching the query, best match first. An
// empty query keeps the build order (pages, groups, profiles).
func paletteItems(m *Model, q string) []paletteItem {
	all := buildPaletteItems(m)
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return all
	}
	type hit struct {
		item paletteItem
		rank int
	}
	hits := make([]hit, 0, len(all))
	for _, it := range all {
		if pos := fuzzyPositions(it.title, q); pos != nil {
			spread := 0
			if len(pos) > 1 {
				spread = pos[len(pos)-1] - pos[0] - len(pos) + 1
			}
			hits = append(hits, hit{it, pos[0]*4 + spread*2})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].rank < hits[j].rank })
	out := make([]paletteItem, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.item)
	}
	return out
}

// openPalette arms the palette over the model's current data.
func (m *Model) openPalette() {
	m.palette = paletteState{open: true}
}

func (m *Model) paletteKey(msg tea.KeyMsg) tea.Cmd {
	// The cursor is clamped against the live filtered list on every key:
	// the overlay's display clamp runs on a value copy and cannot persist.
	items := paletteItems(m, m.palette.q)
	switch msg.String() {
	case "esc":
		m.palette = paletteState{}
		return nil
	case "up":
		if m.palette.cur > 0 {
			m.palette.cur--
		}
		m.clampPaletteCursor(len(items))
		return nil
	case "down":
		m.palette.cur++
		m.clampPaletteCursor(len(items))
		return nil
	case "backspace":
		if r := []rune(m.palette.q); len(r) > 0 {
			m.palette.q = string(r[:len(r)-1])
			m.palette.cur = 0
		}
		return nil
	case "enter":
		if m.palette.cur < 0 || m.palette.cur >= len(items) {
			return nil
		}
		run := items[m.palette.cur].run
		m.palette = paletteState{}
		return run(m)
	}
	// Plain runes (and space) type into the filter; everything else is
	// swallowed so a stray j/k/o while the palette is open cannot reach
	// the page beneath.
	if r := msg.Runes; msg.Type == tea.KeyRunes && len(r) > 0 {
		m.palette.q += string(r)
		m.palette.cur = 0
		return nil
	}
	if msg.String() == " " {
		m.palette.q += " "
		m.palette.cur = 0
	}
	return nil
}

func (m *Model) clampPaletteCursor(n int) {
	if m.palette.cur >= n {
		m.palette.cur = max0(n - 1)
	}
}

// paletteOverlay renders the palette: filter line, the matching window and
// the footer. Styled per row via HiRow; title/hint ride the cursor style.
func (m Model) paletteOverlay() *overlaySpec {
	items := paletteItems(&m, m.palette.q)
	if m.palette.cur >= len(items) {
		m.palette.cur = max0(len(items) - 1)
	}
	lines := []string{ui.TitleStyle.Render(" " + i18n.T(" 命令面板"))}
	lines = append(lines, " / "+m.palette.q)
	if len(items) == 0 {
		lines = append(lines, ui.HelpStyle.Render(centerLine(i18n.T("没有匹配的动作"), 36)))
	}
	start := 0
	if n := len(items); n > paletteWin {
		if m.palette.cur >= paletteWin {
			start = m.palette.cur - paletteWin + 1
		}
		if end := start + paletteWin; end > n {
			start = n - paletteWin
		}
	}
	for i := start; i < len(items) && i < start+paletteWin; i++ {
		mark, style := "  ", ui.HelpStyle
		if i == m.palette.cur {
			mark, style = "❯ ", ui.CursorStyle
		}
		hint := ui.HelpStyle.Render("  " + items[i].hint)
		lines = append(lines, ui.HiRow(style.Render(mark+items[i].title)+hint, 44, i == m.palette.cur))
	}
	return &overlaySpec{lines: append(lines, "",
		ui.HelpStyle.Render(i18n.T(" 输入过滤 · ↑↓ 选择 · Enter 执行 · esc 关闭")))}
}
