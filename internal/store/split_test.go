package store

import (
	"strings"
	"testing"
)

func TestSplitStatements(t *testing.T) {
	got := splitStatements("-- c\nCREATE TABLE a (x INT);\n\nCREATE INDEX i ON a (x);\n")
	if len(got) != 2 || got[1] != "CREATE INDEX i ON a (x)" {
		t.Fatalf("%q", got)
	}
}

func TestDialectBlocks(t *testing.T) {
	src := "CREATE TABLE a (x BLOB);\n-- +sqlite\nCREATE VIRTUAL TABLE f USING fts5(x);\n-- +end\n-- +postgres\nCREATE INDEX g ON a (x);\n-- +end\n"
	sq := dialectSQL(SQLite, src)
	pg := dialectSQL(Postgres, src)
	if !strings.Contains(sq, "fts5") || strings.Contains(sq, "CREATE INDEX g") || !strings.Contains(sq, "BLOB") {
		t.Fatalf("sqlite: %s", sq)
	}
	if strings.Contains(pg, "fts5") || !strings.Contains(pg, "CREATE INDEX g") || !strings.Contains(pg, "BYTEA") {
		t.Fatalf("postgres: %s", pg)
	}
}
