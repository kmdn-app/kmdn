package consistency

import (
	"strings"
	"testing"
)

func TestChunk(t *testing.T) {
	long := strings.Repeat("Every team member files expenses within thirty days of the purchase date. ", 30)
	md := "---\ntitle: Travel\n---\n# Travel policy\n\nShort intro.\n\n## Working abroad\n\nEmployees can work abroad for up to **30 working days** per year. Ask [your manager](people.md) first.\n\n- Tell HR before you travel, and keep your receipts for the trip.\n\n```\ncode here\n```\n\n### Expenses\n\n" + long + "\n"
	ps := Chunk("docs/travel.md", md)
	if len(ps) < 3 {
		t.Fatalf("passages: %d %+v", len(ps), ps)
	}
	a := ps[0]
	if a.Heading != "Working abroad" || a.Slug != "working-abroad" || a.Line != 10 || a.Context != "Travel policy › Working abroad" {
		t.Fatalf("first passage: %+v", a)
	}
	if !strings.Contains(a.Text, "up to 30 working days per year. Ask your manager first.") || !strings.Contains(a.Text, "Tell HR before") || strings.Contains(a.Text, "code here") {
		t.Fatalf("text not cleaned: %q", a.Text)
	}
	// "Short intro." is too short to state anything.
	for _, p := range ps {
		if strings.Contains(p.Text, "Short intro") {
			t.Fatalf("short passage kept: %+v", p)
		}
		if len(p.Text) > maxChars+100 {
			t.Fatalf("passage too long: %d", len(p.Text))
		}
	}
	if ps[1].Context != "Travel policy › Working abroad › Expenses" || ps[1].Seq != 1 {
		t.Fatalf("nested context: %+v", ps[1])
	}
	// Hashes are stable and cover the context.
	again := Chunk("docs/travel.md", md)
	if again[0].Hash != a.Hash {
		t.Fatal("unstable hash")
	}
	moved := Chunk("docs/travel.md", strings.Replace(md, "## Working abroad", "## Abroad", 1))
	if moved[0].Hash == a.Hash {
		t.Fatal("hash ignores the heading")
	}
	if PairKey("b", "a") != PairKey("a", "b") {
		t.Fatal("pair key order")
	}
}

func TestFlatNear(t *testing.T) {
	f := Flat{normalize([]float32{1, 0}), normalize([]float32{1, 0.1}), normalize([]float32{0, 1}), normalize([]float32{1, 0.5})}
	hits := f.Near(normalize([]float32{1, 0}), 2, 0.8, func(i int) bool { return i == 0 })
	if len(hits) != 2 || hits[0].I != 1 || hits[1].I != 3 {
		t.Fatalf("hits: %+v", hits)
	}
	v := []float32{0.25, -1.5, 3}
	if got := decode(encode(v)); got[1] != -1.5 || len(got) != 3 {
		t.Fatalf("round trip: %v", got)
	}
}
