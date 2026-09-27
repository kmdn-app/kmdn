// Package textdiff computes line diffs (Myers' O(ND) algorithm). It backs the
// +/− counts in revision manifests and the source diff view.
package textdiff

import "strings"

// Op is an edit operation.
type Op int8

// Edit operations.
const (
	Equal Op = iota
	Insert
	Delete
)

// MaxEdits bounds the search; inputs further apart are diffed as a full
// replacement of the differing middle.
var MaxEdits = 4000

// Edit is one line of an edit script. For Delete, A indexes the old lines; for
// Insert, B indexes the new lines; Equal sets both.
type Edit struct {
	Op   Op
	A, B int
}

// Lines splits text into lines without their terminators. A trailing newline
// does not produce an empty last line.
func Lines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	return strings.Split(s, "\n")
}

// Diff returns the shortest edit script turning a into b.
func Diff(a, b []string) []Edit {
	// Trim the common prefix and suffix: most edits are local, and this keeps
	// the quadratic worst case away from typical documents.
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	out := make([]Edit, 0, len(a)+len(b))
	for i := range pre {
		out = append(out, Edit{Equal, i, i})
	}
	out = append(out, myers(a[pre:len(a)-suf], b[pre:len(b)-suf], pre)...)
	for i := range suf {
		out = append(out, Edit{Equal, len(a) - suf + i, len(b) - suf + i})
	}
	return out
}

func myers(a, b []string, off int) []Edit {
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return nil
	}
	maxD := n + m
	v := make([]int, 2*maxD+2)
	// trace[d] is the window [-d, d] of v at the start of round d, i.e. the
	// furthest x per diagonal after d-1 edits; backtracking reads it to find
	// each step's origin. Memory is O(D²), so D is capped.
	var trace [][]int
	for d := 0; d <= min(maxD, MaxEdits); d++ {
		trace = append(trace, append([]int(nil), v[maxD-d:maxD+d+1]...))
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[maxD+k-1] < v[maxD+k+1]) {
				x = v[maxD+k+1]
			} else {
				x = v[maxD+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[maxD+k] = x
			if x >= n && y >= m {
				return backtrack(trace, n, m, off)
			}
		}
	}
	// Too different: replace the whole middle.
	out := make([]Edit, 0, n+m)
	for i := range n {
		out = append(out, Edit{Delete, off + i, -1})
	}
	for j := range m {
		out = append(out, Edit{Insert, -1, off + j})
	}
	return out
}

func backtrack(trace [][]int, x, y, off int) []Edit {
	var rev []Edit
	for d := len(trace) - 1; d >= 0; d-- {
		w := trace[d] // w[k+d] is v[k]
		k := x - y
		var pk int
		if k == -d || (k != d && w[k-1+d] < w[k+1+d]) {
			pk = k + 1
		} else {
			pk = k - 1
		}
		var px int
		if d > 0 {
			px = w[pk+d]
		}
		py := px - pk
		if d == 0 {
			px, py = 0, 0
		}
		for x > px && y > py {
			x--
			y--
			rev = append(rev, Edit{Equal, off + x, off + y})
		}
		if d > 0 {
			if x == px {
				rev = append(rev, Edit{Insert, -1, off + py})
			} else {
				rev = append(rev, Edit{Delete, off + px, -1})
			}
		}
		x, y = px, py
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}

// Stats counts inserted and deleted lines between two texts.
func Stats(a, b string) (additions, deletions int) {
	for _, e := range Diff(Lines(a), Lines(b)) {
		switch e.Op {
		case Insert:
			additions++
		case Delete:
			deletions++
		}
	}
	return additions, deletions
}
