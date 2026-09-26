package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migration is one numbered SQL file (NNNN_name.sql).
type Migration struct {
	Version int
	Name    string
	SQL     string
}

var migName = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.sql$`)

// Migrations lists the embedded migrations in order.
func Migrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var out []Migration
	for _, e := range entries {
		m := migName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("store: bad migration file name %q", e.Name())
		}
		v, _ := strconv.Atoi(m[1])
		b, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, Migration{Version: v, Name: m[2], SQL: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	for i, m := range out {
		if m.Version != i+1 {
			return nil, fmt.Errorf("store: migration versions must be contiguous, found %04d at position %d", m.Version, i+1)
		}
	}
	return out, nil
}

// dialectSQL adapts portable migration SQL to the engine. Migrations are
// written for SQLite; for Postgres, BLOB becomes BYTEA. Keep migrations to
// types both engines accept (TEXT, BIGINT, INTEGER, BOOLEAN, BLOB).
func dialectSQL(d Dialect, s string) string {
	if d == Postgres {
		s = regexp.MustCompile(`\bBLOB\b`).ReplaceAllString(s, "BYTEA")
	}
	return s
}

// MigrationStatus describes an applied or pending migration.
type MigrationStatus struct {
	Migration
	AppliedAt *time.Time
}

func (db *DB) ensureMigrationsTable(ctx context.Context) error {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at BIGINT NOT NULL
	)`)
	return err
}

// Status reports every migration and whether it has been applied.
func (db *DB) Status(ctx context.Context) ([]MigrationStatus, error) {
	if err := db.ensureMigrationsTable(ctx); err != nil {
		return nil, err
	}
	all, err := Migrations()
	if err != nil {
		return nil, err
	}
	applied := map[int]time.Time{}
	rows, err := db.QueryContext(ctx, `SELECT version, applied_at FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		var at int64
		if err := rows.Scan(&v, &at); err != nil {
			return nil, err
		}
		applied[v] = FromMillis(at)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]MigrationStatus, len(all))
	for i, m := range all {
		out[i] = MigrationStatus{Migration: m}
		if at, ok := applied[m.Version]; ok {
			at := at
			out[i].AppliedAt = &at
		}
	}
	return out, nil
}

// Migrate applies pending migrations. On Postgres an advisory lock keeps
// concurrent starts from racing; SQLite's immediate transactions serialize.
func (db *DB) Migrate(ctx context.Context) (applied int, err error) {
	if err := db.ensureMigrationsTable(ctx); err != nil {
		return 0, err
	}
	if db.Dialect == Postgres {
		conn, err := db.Conn(ctx)
		if err != nil {
			return 0, err
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(7331001)`); err != nil {
			return 0, err
		}
		defer conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(7331001)`) //nolint:errcheck
	}
	status, err := db.Status(ctx)
	if err != nil {
		return 0, err
	}
	for _, st := range status {
		if st.AppliedAt != nil {
			continue
		}
		m := st.Migration
		err := db.InTx(ctx, func(tx *Tx) error {
			var exists int
			if err := QueryRow(ctx, tx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, m.Version).Scan(&exists); err != nil {
				return err
			}
			if exists > 0 {
				return nil
			}
			for _, stmt := range splitStatements(dialectSQL(db.Dialect, m.SQL)) {
				if _, err := tx.ExecContext(ctx, stmt); err != nil {
					return fmt.Errorf("migration %04d_%s: %w\n%s", m.Version, m.Name, err, stmt)
				}
			}
			_, err := Exec(ctx, tx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`, m.Version, m.Name, Millis(time.Now()))
			return err
		})
		if err != nil {
			return applied, err
		}
		applied++
	}
	return applied, nil
}

// splitStatements splits on semicolons at line ends, ignoring -- comments.
// Migrations must not contain semicolons inside string literals.
func splitStatements(s string) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(s, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "--") || trim == "" {
			continue
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
		if strings.HasSuffix(trim, ";") {
			if st := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(cur.String()), ";")); st != "" {
				out = append(out, st)
			}
			cur.Reset()
		}
	}
	if st := strings.TrimSpace(cur.String()); st != "" {
		out = append(out, st)
	}
	return out
}
