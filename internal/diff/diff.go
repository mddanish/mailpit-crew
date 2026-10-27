// Package diff produces a simple line diff for change previews.
package diff

import "strings"

// Op is a diff line operation.
type Op string

// Diff operations.
const (
	Same   Op = " "
	Add    Op = "+"
	Remove Op = "-"
	Skip   Op = "…" // marks hidden unchanged lines
)

// Line is one line of a diff.
type Line struct {
	Op   Op
	Text string
}

// Lines diffs a and b line by line and keeps context unchanged lines around
// each change.
func Lines(a, b string, context int) []Line {
	x, y := split(a), split(b)
	n, m := len(x), len(y)
	// longest common subsequence table
	t := make([][]int, n+1)
	for i := range t {
		t[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if x[i] == y[j] {
				t[i][j] = t[i+1][j+1] + 1
			} else if t[i+1][j] >= t[i][j+1] {
				t[i][j] = t[i+1][j]
			} else {
				t[i][j] = t[i][j+1]
			}
		}
	}
	var all []Line
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case x[i] == y[j]:
			all = append(all, Line{Same, x[i]})
			i++
			j++
		case t[i+1][j] >= t[i][j+1]:
			all = append(all, Line{Remove, x[i]})
			i++
		default:
			all = append(all, Line{Add, y[j]})
			j++
		}
	}
	for ; i < n; i++ {
		all = append(all, Line{Remove, x[i]})
	}
	for ; j < m; j++ {
		all = append(all, Line{Add, y[j]})
	}
	return trim(all, context)
}

func split(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func trim(all []Line, context int) []Line {
	keep := make([]bool, len(all))
	for i, l := range all {
		if l.Op == Same {
			continue
		}
		for k := i - context; k <= i+context; k++ {
			if k >= 0 && k < len(all) {
				keep[k] = true
			}
		}
	}
	var out []Line
	skipped := false
	for i, l := range all {
		if keep[i] {
			out = append(out, l)
			skipped = false
		} else if !skipped {
			out = append(out, Line{Skip, ""})
			skipped = true
		}
	}
	return out
}
