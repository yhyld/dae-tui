package app

import (
	"strings"

	"github.com/yhyld/dae-tui/internal/driver"
	"github.com/yhyld/dae-tui/internal/ui"
)

// The node filter speaks fzf: a query matches when its runes occur as a
// case-insensitive subsequence of any recognizable field (a substring is the
// special case of a packed subsequence), the survivors reorder by how well
// they matched, and the name column highlights the runes the query consumed
// — the three together are why "typing a few chars finds the node" works on
// a thousand-node subscription.

// fuzzyFieldGap separates the per-field ranks in fuzzyNodeScore; it exceeds
// any position/spread penalty a realistic name can accumulate.
const fuzzyFieldGap = 10000

// fuzzyPositions finds q as a case-insensitive subsequence of s and returns
// the rune indices of one greedy-leftmost choice, or nil when q does not
// fit. Greedy-leftmost packs the hits as early — and therefore as
// contiguous — as the text allows, which is also the span the highlight
// wants to paint.
func fuzzyPositions(s, q string) []int {
	if q == "" {
		return nil
	}
	sr := []rune(strings.ToLower(s))
	qr := []rune(strings.ToLower(q))
	pos := make([]int, 0, len(qr))
	next := 0
	for _, qc := range qr {
		found := -1
		for i := next; i < len(sr); i++ {
			if sr[i] == qc {
				found = i
				break
			}
		}
		if found < 0 {
			return nil
		}
		pos = append(pos, found)
		next = found + 1
	}
	return pos
}

// matchNode reports whether the query fits any recognizable field. It keeps
// the old multi-field contract; only the per-field test changed from
// substring to subsequence (a superset, so no old query stops matching).
func matchNode(n driver.Node, q string) bool {
	if q == "" {
		return true
	}
	for _, f := range []string{n.Name, n.Protocol, n.Tag, n.Address} {
		if fuzzyPositions(f, q) != nil {
			return true
		}
	}
	return false
}

// fuzzyNodeScore ranks how well q matches a node; lower is better. A name
// hit outranks a protocol hit, then tag, then address; within a field an
// earlier first hit and a tighter spread win. A substring hit is a
// subsequence hit with spread zero, so exact fragments sort above scattered
// ones without a separate code path.
func fuzzyNodeScore(n driver.Node, q string) int {
	for rank, field := range []string{n.Name, n.Tag, n.Protocol, n.Address} {
		if pos := fuzzyPositions(field, q); pos != nil {
			spread := pos[len(pos)-1] - pos[0] - len(pos) + 1
			return rank*fuzzyFieldGap + pos[0]*4 + spread*2
		}
	}
	// Unreachable for nodes that passed matchNode; keeps the sort total.
	return fuzzyFieldGap * 4
}

// nodeName renders a node's name for a list row — SpaceAfterFlag applied —
// with the filter's matched runes accented (bold accent, the same slot the
// rest of the UI marks emphasis with). Only name-field matches highlight: a
// hit on protocol or tag leaves the name plain, honestly saying nothing in
// it matched. The four node-list call sites share this helper so the
// highlight lands wherever a filter can; the filter arrives raw (the
// nodeView's live or applied text) and is normalized exactly like
// visible() does, so what highlights is what matched.
func nodeName(n driver.Node, filter string) string {
	disp := ui.SpaceAfterFlag(n.Name)
	pos := fuzzyPositions(disp, strings.TrimSpace(filter))
	if len(pos) == 0 {
		return disp
	}
	hit := make(map[int]bool, len(pos))
	for _, p := range pos {
		hit[p] = true
	}
	rs := []rune(disp)
	var b strings.Builder
	for i := 0; i < len(rs); {
		if !hit[i] {
			b.WriteRune(rs[i])
			i++
			continue
		}
		j := i
		for j < len(rs) && hit[j] {
			j++
		}
		b.WriteString(ui.SelectedStyle.Render(string(rs[i:j])))
		i = j
	}
	return b.String()
}
