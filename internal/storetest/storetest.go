// Package storetest opens migrated databases for tests.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/store"
)

// Open returns a migrated database: SQLite in t.TempDir(), or, when
// KMDN_TEST_POSTGRES_URL is set, a fresh Postgres schema dropped after the test.
func Open(t testing.TB) *store.DB {
	t.Helper()
	ctx := context.Background()
	if pg := os.Getenv("KMDN_TEST_POSTGRES_URL"); pg != "" {
		var b [6]byte
		_, _ = rand.Read(b[:])
		schema := "t_" + hex.EncodeToString(b[:])
		admin, err := store.Open(ctx, pg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
			t.Fatal(err)
		}
		sep := "?"
		if strings.Contains(pg, "?") {
			sep = "&"
		}
		db, err := store.Open(ctx, fmt.Sprintf("%s%ssearch_path=%s", pg, sep, schema))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			db.Close()
			_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
			admin.Close()
		})
		if _, err := db.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		return db
	}
	db, err := store.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "kmdn.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}
