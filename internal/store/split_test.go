package store

import "testing"

func TestSplitStatements(t *testing.T) {
	got := splitStatements("-- c\nCREATE TABLE a (x INT);\n\nCREATE INDEX i ON a (x);\n")
	if len(got) != 2 || got[1] != "CREATE INDEX i ON a (x)" {
		t.Fatalf("%q", got)
	}
}
