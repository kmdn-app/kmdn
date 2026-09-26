package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/storetest"
)

func openTest(t *testing.T) *store.DB { return storetest.Open(t) }

func TestMigrateIsIdempotent(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	n, err := db.Migrate(ctx)
	if err != nil || n != 0 {
		t.Fatalf("second migrate applied %d, err %v", n, err)
	}
	st, err := db.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range st {
		if s.AppliedAt == nil {
			t.Fatalf("migration %d not applied", s.Version)
		}
	}
}

func TestRebind(t *testing.T) {
	pg := &store.DB{Dialect: store.Postgres}
	if got := pg.Rebind(`SELECT * FROM t WHERE a = ? AND b = '?' AND c = ?`); got != `SELECT * FROM t WHERE a = $1 AND b = '?' AND c = $2` {
		t.Fatal(got)
	}
	sq := &store.DB{Dialect: store.SQLite}
	if got := sq.Rebind(`a = ?`); got != `a = ?` {
		t.Fatal(got)
	}
}

func TestInTxRollsBack(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	boom := errors.New("boom")
	err := db.InTx(ctx, func(tx *store.Tx) error {
		if _, err := store.Exec(ctx, tx, `INSERT INTO instance_settings (key, value, updated_at) VALUES (?, ?, ?)`, "k", "v", 1); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	var n int
	if err := store.QueryRow(ctx, db, `SELECT COUNT(*) FROM instance_settings`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rollback failed: n=%d err=%v", n, err)
	}
}

func TestUniqueViolationAndNotFound(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	ins := `INSERT INTO users (id, email, name, created_at) VALUES (?, ?, ?, ?)`
	if _, err := store.Exec(ctx, db, ins, "usr_1", "a@example.com", "A", 1); err != nil {
		t.Fatal(err)
	}
	_, err := store.Exec(ctx, db, ins, "usr_2", "a@example.com", "B", 1)
	if !store.IsUniqueViolation(err) {
		t.Fatalf("expected unique violation, got %v", err)
	}
	var name string
	err = store.NotFound(store.QueryRow(ctx, db, `SELECT name FROM users WHERE id = ?`, "nope").Scan(&name))
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestOpenRejectsUnknownScheme(t *testing.T) {
	if _, err := store.Open(context.Background(), "mysql://x"); err == nil {
		t.Fatal("expected error")
	}
	p := filepath.Join(t.TempDir(), "sub", "kmdn.db")
	db, err := store.Open(context.Background(), "sqlite://"+p)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
}
