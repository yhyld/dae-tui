package app

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/yhyld/dae-tui/internal/driver"
	"github.com/yhyld/dae-tui/internal/i18n"
	"github.com/yhyld/dae-tui/internal/ui"
)

const (
	latCellWide = 15
	latCellMid  = 13
	latCellSlim = 9
)

// latCellW sizes the latency column for a pane's content width: a 5-cell
// bar plus the figure on wide panes, a 3-cell bar on mid-width panes (the
// bar degrades instead of vanishing), the bare right-aligned figure below
// that.
func latCellW(w int) int {
	switch {
	case w >= 56:
		return latCellWide
	case w >= 46:
		return latCellMid
	}
	return latCellSlim
}

func latencyCell(lat map[string]driver.Latency, id string, cellW int) string {
	latStr, st := "-", ui.LatencyStyle(0, false, false)
	ms := 0
	if l, ok := lat[id]; ok && !l.TestedAt.IsZero() {
		if l.Alive && l.Ms > 0 {
			latStr = strconv.Itoa(l.Ms) + "ms"
			ms = l.Ms
		} else if !l.Alive {
			latStr = i18n.T("超时")
		} else {
			latStr = "…"
		}
		st = ui.LatencyStyle(l.Ms, l.Alive, true)
	}
	barW := 0
	switch {
	case cellW >= latCellWide:
		barW = 5
	case cellW >= latCellMid:
		barW = 3
	}
	if barW > 0 {
		return st.Render(ui.LatencyBarN(ms, barW)) + " " + st.Render(ui.PadLeft(latStr, 9))
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
	if q != "" && v.sortBy == sortBackend {
		// While filtering, relevance is the order (the fzf contract): best
		// matches on top. An explicit latency sort (o) still overrides it.
		sort.SliceStable(out, func(i, j int) bool {
			return fuzzyNodeScore(out[i], q) < fuzzyNodeScore(out[j], q)
		})
	}
	switch v.sortBy {
	case sortLatencyAsc:
		sort.SliceStable(out, func(i, j int) bool { return lessByLatency(out[i], out[j], lat, false) })
	case sortLatencyDesc:
		sort.SliceStable(out, func(i, j int) bool { return lessByLatency(out[i], out[j], lat, true) })
	}
	return out
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
