// Package storetest opens migrated databases for tests.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	neturl "net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/kmdn-app/kmdn/internal/store"
)

// Open returns a migrated database: SQLite in t.TempDir(), or, when
// KMDN_TEST_POSTGRES_URL is set, a fresh Postgres schema dropped after the test.
func Open(t testing.TB) *store.DB {
	t.Helper()
	ctx := context.Background()
	url := "sqlite://" + filepath.Join(t.TempDir(), "kmdn.db")
	if PostgresEnabled() {
		url = PostgresURL(t)
	}
	db, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}

// PostgresEnabled reports whether tests run against Postgres.
func PostgresEnabled() bool { return os.Getenv("KMDN_TEST_POSTGRES_URL") != "" }

// Role is the ordinary (not superuser) role Postgres tests connect as, so
// row-level security applies to them as it does in production.
const Role = "kmdn_test"

// PostgresURL creates a schema owned by Role, dropped after the test, and
// returns a db URL that connects to it as Role.
func PostgresURL(t testing.TB) string {
	t.Helper()
	ctx := context.Background()
	pg := os.Getenv("KMDN_TEST_POSTGRES_URL")
	var b [6]byte
	_, _ = rand.Read(b[:])
	schema := "t_" + hex.EncodeToString(b[:])
	admin, err := store.Open(ctx, pg)
	if err != nil {
		t.Fatal(err)
	}
	// Roles are shared by every schema; parallel test binaries race to create it.
	if _, err := admin.ExecContext(ctx, `DO $$ BEGIN CREATE ROLE `+Role+` LOGIN PASSWORD '`+Role+`' NOSUPERUSER NOBYPASSRLS; EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema+" AUTHORIZATION "+Role); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	u, err := neturl.Parse(pg)
	if err != nil {
		t.Fatal(err)
	}
	u.User = neturl.UserPassword(Role, Role)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}
