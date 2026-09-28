package app

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"dae-tui/internal/driver"
	"dae-tui/internal/i18n"
	"dae-tui/internal/ui"
)

const (
	latCellWide = 15
	latCellSlim = 9
)

func latCellW(w int) int {
	if w >= 56 {
		return latCellWide
	}
	return latCellSlim
}

func latencyCell(lat map[string]driver.Latency, id string, cellW int) string {
	latStr, bar, st := "-", ui.LatencyBar(0), ui.LatencyStyle(0, false, false)
	if l, ok := lat[id]; ok && !l.TestedAt.IsZero() {
		if l.Alive && l.Ms > 0 {
			latStr = strconv.Itoa(l.Ms) + "ms"
			bar = ui.LatencyBar(l.Ms)
		} else if !l.Alive {
			latStr = i18n.T("超时")
		} else {
			latStr = "…"
		}
		st = ui.LatencyStyle(l.Ms, l.Alive, true)
	}
	if cellW >= latCellWide {
		return st.Render(bar) + " " + st.Render(ui.PadLeft(latStr, 9))
	}
	return st.Render(ui.PadLeft(latStr, 9))
}

func latencyDetail(lat map[string]driver.Latency, id string) (string, bool) {
	l, ok := lat[id]
	if !ok || l.TestedAt.IsZero() {
		return "", false
	}
	st := ui.LatencyStyle(l.Ms, l.Alive, true)
	switch {
	case !l.Alive:
		return st.Render(i18n.T("超时")), true
	case l.Ms <= 0:
		return st.Render("…"), true
	}
	return st.Render(strconv.Itoa(l.Ms)+"ms") + "  " + st.Render(ui.LatencyBar(l.Ms)), true
}

type nodeView struct {
	open    bool
	input   textinput.Model
	applied string
	sortBy  int
}

const (
	sortBackend = iota
	sortLatencyAsc
	sortLatencyDesc
)

func newNodeView() nodeView {
	ti := textinput.New()
	ti.CharLimit = 64
	ti.Width = 20
	return nodeView{input: ti}
}

// handleKey consumes the filter's keys. Open, it captures everything raw
// (typing must never be remapped); closed, "/" and "o" are page-scope
// actions and go through the keymap like the rest of the page.
func (v *nodeView) handleKey(msg tea.KeyMsg, scope string) (tea.Cmd, bool) {
	if v.open {
		switch msg.String() {
		case "esc":
			v.close("")
			return nil, true
		case "enter":
			v.close(v.input.Value())
			return nil, true
		}
		var cmd tea.Cmd
		v.input, cmd = v.input.Update(msg)
		return cmd, true
	}
	switch tk(scope, msg.String()) {
	case "/":
		v.open = true
		v.input.SetValue(v.applied)
		v.input.Focus()
		return textinput.Blink, true
	case "o":
		v.sortBy = (v.sortBy + 1) % 3
		return nil, true
	}
	return nil, false
}

func (v *nodeView) close(q string) {
	v.applied = q
	v.open = false
	v.input.Blur()
}

func (v *nodeView) filter() string {
	if v.open {
		return v.input.Value()
	}
	return v.applied
}

func (v *nodeView) visible(nodes []driver.Node, lat map[string]driver.Latency) []driver.Node {
	q := strings.ToLower(strings.TrimSpace(v.filter()))
	out := make([]driver.Node, 0, len(nodes))
	for _, n := range nodes {
		if q != "" && !matchNode(n, q) {
			continue
		}
		out = append(out, n)
	}
	switch v.sortBy {
	case sortLatencyAsc:
		sort.SliceStable(out, func(i, j int) bool { return lessByLatency(out[i], out[j], lat, false) })
	case sortLatencyDesc:
		sort.SliceStable(out, func(i, j int) bool { return lessByLatency(out[i], out[j], lat, true) })
	}
	return out
}

func matchNode(n driver.Node, q string) bool {
	if q == "" {
		return true
	}
	for _, f := range []string{n.Name, n.Protocol, n.Tag, n.Address} {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}

func lessByLatency(a, b driver.Node, lat map[string]driver.Latency, desc bool) bool {
	ma, oka := measured(a, lat)
	mb, okb := measured(b, lat)
	switch {
	case oka && okb:
		if desc {
			return ma > mb
		}
		return ma < mb
	case oka:
		return true
	case okb:
		return false
	default:
		return false
	}
}

func measured(n driver.Node, lat map[string]driver.Latency) (int, bool) {
	l, ok := lat[n.ID]
	if !ok || !l.Alive || l.Ms <= 0 || l.TestedAt.IsZero() {
		return 0, false
	}
	return l.Ms, true
}

func (v *nodeView) countTitle(shown, total int) string {
	if v.filter() != "" {
		return fmt.Sprintf(" (%d/%d)", shown, total)
	}
	return fmt.Sprintf(" (%d)", total)
}

func (v *nodeView) sortTitle() string {
	switch v.sortBy {
	case sortLatencyAsc:
		return i18n.T(" · 延迟↑")
	case sortLatencyDesc:
		return i18n.T(" · 延迟↓")
	}
	return ""
}

func (v *nodeView) prompt() string {
	if v.open {
		// Re-translate per render: the input outlives a language switch.
		v.input.Placeholder = i18n.T("名称/协议/标签/地址")
		return " / " + v.input.View()
	}
	if v.applied != "" {
		return i18n.T(" 过滤: ") + v.applied + i18n.T("  (/ 重新编辑)")
	}
	return ""
}
