package app

import (
	"strconv"
	"strings"
)

// diffLine is one rendered line of a line-oriented diff.
type diffLine struct {
	kind int
	text string
}

const (
	diffEqual = iota // unchanged context
	diffAdd          // present only in the new text
	diffDel          // present only in the old text
	diffGap          // a collapsed run of unchanged lines
)

// diffLines compares old → new line by line and returns display lines.
// Unchanged runs longer than a few lines collapse into a gap marker, so the
// interesting part of a DSL edit stays on screen instead of scrolling past
// pages of context.
func diffLines(old, new string) []diffLine {
	ops := lcsDiff(splitDiffLines(old), splitDiffLines(new))
	var out []diffLine
	for i := 0; i < len(ops); {
		if ops[i].kind != diffEqual {
			out = append(out, ops[i])
			i++
			continue
		}
		j := i
		for j < len(ops) && ops[j].kind == diffEqual {
			j++
		}
		if run := j - i; run > 4 {
			out = append(out, diffLine{kind: diffGap, text: "… " + strconv.Itoa(run) + " 行相同"})
		} else {
			out = append(out, ops[i:j]...)
		}
		i = j
	}
	return out
}

// maxDiffCells caps the LCS table: the builtin editor accepts arbitrarily
// large pastes, and a quadratic table over a multi-thousand-line diff is
// real memory for a confirmation the user answers with y/n.
const maxDiffCells = 1 << 21

// lcsDiff computes the edit script between a and b. The common head and
// tail are trimmed first — a one-rule change inside a long profile then
// pays an LCS table for the changed middle only — and a middle that is
// still too large to diff pairwise degrades to a wholesale replacement
// (everything deleted, everything added) instead of trying to align it.
func lcsDiff(a, b []string) []diffLine {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ops := make([]diffLine, 0, pre+suf+len(a)+len(b))
	for i := 0; i < pre; i++ {
		ops = append(ops, diffLine{kind: diffEqual, text: a[i]})
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]
	if len(ma)*len(mb) > maxDiffCells {
		for _, l := range ma {
			ops = append(ops, diffLine{kind: diffDel, text: l})
		}
		for _, l := range mb {
			ops = append(ops, diffLine{kind: diffAdd, text: l})
		}
	} else {
		ops = append(ops, lcsTable(ma, mb)...)
	}
	for i := len(a) - suf; i < len(a); i++ {
		ops = append(ops, diffLine{kind: diffEqual, text: a[i]})
	}
	return ops
}

// lcsTable is the classic longest-common-subsequence edit script. Routing/
// DNS profiles are tens of lines (and lcsDiff trims the unchanged frame
// first), so the quadratic table is not a concern here.
func lcsTable(a, b []string) []diffLine {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		var ops []diffLine
		for _, l := range a {
			ops = append(ops, diffLine{kind: diffDel, text: l})
		}
		for _, l := range b {
			ops = append(ops, diffLine{kind: diffAdd, text: l})
		}
		return ops
	}
	table := make([][]int, n+1)
	for i := range table {
		table[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				table[i][j] = table[i+1][j+1] + 1
			case table[i+1][j] >= table[i][j+1]:
				table[i][j] = table[i+1][j]
			default:
				table[i][j] = table[i][j+1]
			}
		}
	}
	var ops []diffLine
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffLine{kind: diffEqual, text: a[i]})
			i++
			j++
		case table[i+1][j] >= table[i][j+1]:
			ops = append(ops, diffLine{kind: diffDel, text: a[i]})
			i++
		default:
			ops = append(ops, diffLine{kind: diffAdd, text: b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffLine{kind: diffDel, text: a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffLine{kind: diffAdd, text: b[j]})
	}
	return ops
}

// splitDiffLines drops the trailing newline and blank-only edges so a diff
// does not open with a spurious empty line.
func splitDiffLines(s string) []string {
	s = strings.Trim(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
