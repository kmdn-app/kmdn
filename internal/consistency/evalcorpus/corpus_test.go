package evalcorpus

import (
	"strings"
	"testing"
)

func TestGenerate(t *testing.T) {
	c := Generate(300, 20, 10, 42)
	if len(c.Files) != 300 || len(c.Contradictions) != 20 || len(c.Duplicates) != 10 || len(c.Traps) < 4 {
		t.Fatalf("files %d, contradictions %d, duplicates %d, traps %d", len(c.Files), len(c.Contradictions), len(c.Duplicates), len(c.Traps))
	}
	for _, f := range facts() {
		if !strings.Contains(f.Say(f.Value), f.Value) || !strings.Contains(f.Say(f.Other), f.Other) {
			t.Fatalf("%s/%s doesn't state its value", f.Topic, f.Key)
		}
	}
	// Each plant states the other value, and some page still states the canonical one.
	byKey := map[string]Fact{}
	for _, f := range facts() {
		byKey[f.Topic+"/"+f.Key] = f
	}
	for _, p := range c.Contradictions {
		f := byKey[p.Fact]
		if !strings.Contains(c.Files[p.Path], f.Say(f.Other)) {
			t.Fatalf("%s doesn't contradict %s", p.Path, p.Fact)
		}
		if len(c.FactPages[p.Fact]) == 0 || !strings.Contains(c.Files[c.FactPages[p.Fact][0]], f.Say(f.Value)) {
			t.Fatalf("nothing states %s canonically", p.Fact)
		}
	}
	// Deterministic.
	d := Generate(300, 20, 10, 42)
	for p, body := range c.Files {
		if d.Files[p] != body {
			t.Fatalf("not deterministic: %s", p)
		}
	}
}
