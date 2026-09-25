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

// lcsDiff computes the edit script between a and b with the classic
// longest-common-subsequence table. Routing/DNS profiles are tens of lines,
// so the quadratic table is not a concern here.
func lcsDiff(a, b []string) []diffLine {
	n, m := len(a), len(b)
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
