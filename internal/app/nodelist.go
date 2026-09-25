package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"dae-tui/internal/driver"
)

// Node lists are the one place where the volume of data hurts: a single
// airport subscription routinely carries hundreds of nodes, and scrolling
// to find one — or eyeballing which of them is fastest — is the difference
// between a tool that is usable and one that is not. nodeView is the shared
// filter/sort state every node list embeds: group members, subscription
// nodes, manual nodes and the add-node picker all behave the same way.
//
//	/      open the filter box (live as you type)
//	enter  close the box, keep the filter applied
//	esc    close the box and clear the filter
//	o      cycle the sort: backend order → latency ↑ → latency ↓
type nodeView struct {
	open    bool // the filter box takes every keystroke
	input   textinput.Model
	applied string // filter in effect (live from the input while open)
	sortBy  int
}

const (
	sortBackend = iota
	sortLatencyAsc
	sortLatencyDesc
)

func newNodeView() nodeView {
	ti := textinput.New()
	ti.Placeholder = "名称/协议/标签/地址"
	ti.CharLimit = 64
	ti.Width = 20
	return nodeView{input: ti}
}

// handleKey runs the filter box and the sort toggle. It reports whether the
// keystroke was consumed; when it was, the page must not act on it — the box
// swallows every key, the same rule the other modals follow.
func (v *nodeView) handleKey(msg tea.KeyMsg) (tea.Cmd, bool) {
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
	switch msg.String() {
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

// filter is the query currently in effect.
func (v *nodeView) filter() string {
	if v.open {
		return v.input.Value()
	}
	return v.applied
}

// visible returns the nodes matching the filter, in the current sort order.
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

// matchNode matches the filter against everything a user might recognize a
// node by, so "ss" or "hk" both narrow the list usefully.
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

// lessByLatency orders measured nodes by latency. Dead and untested nodes
// have no latency to sort by and always trail, in their original order.
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

// countTitle is the pane-title suffix: matched/total while filtering.
func (v *nodeView) countTitle(shown, total int) string {
	if v.filter() != "" {
		return fmt.Sprintf(" (%d/%d)", shown, total)
	}
	return fmt.Sprintf(" (%d)", total)
}

// sortTitle names the sort mode for the pane title ("" in backend order).
func (v *nodeView) sortTitle() string {
	switch v.sortBy {
	case sortLatencyAsc:
		return " · 延迟↑"
	case sortLatencyDesc:
		return " · 延迟↓"
	}
	return ""
}

// prompt is the filter line to render above a list: the live input while the
// box is open, otherwise a reminder of the applied filter.
func (v *nodeView) prompt() string {
	if v.open {
		return " / " + v.input.View()
	}
	if v.applied != "" {
		return " 过滤: " + v.applied + "  (/ 重新编辑)"
	}
	return ""
}
