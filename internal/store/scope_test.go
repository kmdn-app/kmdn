package store_test

import (
	"context"
	"testing"

	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/storetest"
)

// A connection keeps the scope it last set; a statement with another scope
// sets it again, and a rollback of a transaction that changed it is noticed.
func TestOrgScope(t *testing.T) {
	if !storetest.PostgresEnabled() {
		t.Skip("Postgres only")
	}
	ctx := context.Background()
	db := storetest.Open(t)
	db.SetMaxOpenConns(1) // one connection, so its remembered scope matters
	for _, o := range []string{"org_a", "org_b"} {
		if _, err := store.Exec(ctx, db, `INSERT INTO orgs (id, slug, name, status, created_at) VALUES (?, ?, ?, 'active', 0)`, o, o, o); err != nil {
			t.Fatal(err)
		}
	}
	count := func(ctx context.Context) int {
		var n int
		if err := store.QueryRow(ctx, db, `SELECT COUNT(*) FROM orgs`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	a, b := store.WithOrg(ctx, "org_a"), store.WithOrg(ctx, "org_b")
	for i, c := range []struct {
		ctx  context.Context
		want int
	}{{ctx, 3}, {a, 1}, {a, 1}, {b, 1}, {ctx, 3}, {store.Unscoped(a), 3}} {
		if got := count(c.ctx); got != c.want {
			t.Fatalf("step %d: %d orgs, want %d", i, got, c.want)
		}
	}
	// Scope changed inside a transaction that rolls back: the connection's
	// setting reverts, and the next statement must not trust the cache.
	_ = db.InTx(a, func(tx *store.Tx) error {
		var n int
		_ = store.QueryRow(b, tx, `SELECT COUNT(*) FROM orgs`).Scan(&n)
		if n != 1 {
			t.Errorf("scope b inside a's transaction: %d", n)
		}
		return context.Canceled
	})
	if got := count(b); got != 1 {
		t.Fatalf("scope b after rollback: %d", got)
	}
	if got := count(a); got != 1 {
		t.Fatalf("scope a after rollback: %d", got)
	}
}

// BenchmarkOrgScope compares a primary-key lookup unscoped, scoped to the
// same org every time (the setting is remembered), and alternating orgs (a
// set_config round trip per statement, the worst case).
func BenchmarkOrgScope(b *testing.B) {
	if !storetest.PostgresEnabled() {
		b.Skip("Postgres only")
	}
	ctx := context.Background()
	db := storetest.Open(b)
	db.SetMaxOpenConns(1)
	for _, o := range []string{"org_a", "org_b"} {
		if _, err := store.Exec(ctx, db, `INSERT INTO orgs (id, slug, name, status, created_at) VALUES (?, ?, ?, 'active', 0)`, o, o, o); err != nil {
			b.Fatal(err)
		}
	}
	run := func(b *testing.B, ctxFor func(i int) context.Context) {
		var name string
		for i := 0; b.Loop(); i++ {
			if err := store.QueryRow(ctxFor(i), db, `SELECT name FROM orgs WHERE id = ?`, "org_a").Scan(&name); err != nil {
				b.Fatal(err)
			}
		}
	}
	a, other := store.WithOrg(ctx, "org_a"), store.WithOrg(ctx, "org_b")
	b.Run("unscoped", func(b *testing.B) { run(b, func(int) context.Context { return ctx }) })
	b.Run("same-org", func(b *testing.B) { run(b, func(int) context.Context { return a }) })
	b.Run("alternating", func(b *testing.B) {
		var name string
		for i := 0; b.Loop(); i++ {
			c := a
			if i%2 == 1 {
				c = other
			}
			// org_b's scope can't see org_a: count instead of scanning a row.
			_ = store.QueryRow(c, db, `SELECT COUNT(*) FROM orgs WHERE id = ?`, "org_a").Scan(&name)
		}
	})
}
