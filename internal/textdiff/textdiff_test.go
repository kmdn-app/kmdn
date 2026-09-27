package textdiff

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// apply rebuilds b from a and the script, checking the script is well formed.
func apply(t *testing.T, a, b []string, es []Edit) []string {
	t.Helper()
	var out []string
	ai, bi := 0, 0
	for _, e := range es {
		switch e.Op {
		case Equal:
			if e.A != ai || e.B != bi || a[ai] != b[bi] {
				t.Fatalf("bad equal %+v at a=%d b=%d", e, ai, bi)
			}
			out = append(out, a[ai])
			ai++
			bi++
		case Delete:
			if e.A != ai {
				t.Fatalf("bad delete %+v at a=%d", e, ai)
			}
			ai++
		case Insert:
			if e.B != bi {
				t.Fatalf("bad insert %+v at b=%d", e, bi)
			}
			out = append(out, b[bi])
			bi++
		}
	}
	if ai != len(a) || bi != len(b) {
		t.Fatalf("script stops at a=%d/%d b=%d/%d", ai, len(a), bi, len(b))
	}
	return out
}

func TestStats(t *testing.T) {
	cases := []struct {
		a, b     string
		add, del int
	}{
		{"", "", 0, 0},
		{"a\nb\n", "a\nb\n", 0, 0},
		{"", "a\nb\n", 2, 0},
		{"a\nb\n", "", 0, 2},
		{"a\nb\nc\n", "a\nx\nc\n", 1, 1},
		{"a\nb\nc\n", "a\nc\n", 0, 1},
		{"a\nc", "a\nb\nc\n", 1, 0},
	}
	for _, c := range cases {
		add, del := Stats(c.a, c.b)
		if add != c.add || del != c.del {
			t.Errorf("Stats(%q, %q) = +%d -%d, want +%d -%d", c.a, c.b, add, del, c.add, c.del)
		}
	}
}

func TestDiffRandom(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	words := []string{"a", "b", "c", "d"}
	gen := func() []string {
		n := r.IntN(12)
		out := make([]string, n)
		for i := range out {
			out[i] = words[r.IntN(len(words))]
		}
		return out
	}
	for range 2000 {
		a, b := gen(), gen()
		es := Diff(a, b)
		got := apply(t, a, b, es)
		if strings.Join(got, "") != strings.Join(b, "") {
			t.Fatalf("diff(%v, %v) rebuilt %v", a, b, got)
		}
		// Minimality: the edit count equals n+m-2·LCS.
		edits := 0
		for _, e := range es {
			if e.Op != Equal {
				edits++
			}
		}
		if want := len(a) + len(b) - 2*lcs(a, b); edits != want {
			t.Fatalf("diff(%v, %v) used %d edits, want %d", a, b, edits, want)
		}
	}
}

func lcs(a, b []string) int {
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = max(dp[i+1][j], dp[i][j+1])
			}
		}
	}
	return dp[0][0]
}

func TestDiffFallsBackBeyondMaxEdits(t *testing.T) {
	defer func(v int) { MaxEdits = v }(MaxEdits)
	MaxEdits = 2
	a, b := []string{"x", "a", "b", "c", "y"}, []string{"x", "d", "e", "f", "y"}
	es := Diff(a, b)
	if got := apply(t, a, b, es); strings.Join(got, "") != "xdefy" {
		t.Fatalf("rebuilt %v", got)
	}
}

func TestHunks(t *testing.T) {
	a := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n"
	b := "1\n2\nthree\n4\n5\n6\n7\n8\n9\n10\neleven\n12\nthirteen\n"
	hs := Hunks(a, b, 2)
	if len(hs) != 2 {
		t.Fatalf("hunks: %+v", hs)
	}
	if h := hs[0]; h.OldStart != 1 || h.OldLines != 5 || h.NewStart != 1 || h.NewLines != 5 {
		t.Fatalf("first hunk: %+v", h)
	}
	var ops string
	for _, l := range hs[1].Lines {
		ops += l.Op
	}
	if ops != "  -+ +" {
		t.Fatalf("second hunk ops %q: %+v", ops, hs[1])
	}
	if len(Hunks(a, a, 3)) != 0 {
		t.Fatal("no change should have no hunks")
	}
}
