// Package store owns the database connection, migrations and small helpers
// shared by the repositories in other packages. It supports SQLite (default,
// pure Go via modernc.org/sqlite) and Postgres (pgx). SQL is written once with
// ? placeholders and rewritten for Postgres by Rebind.
package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/stdlib" // postgres driver
	_ "modernc.org/sqlite"           // sqlite driver
)

// Dialect identifies the SQL engine.
type Dialect int

const (
	SQLite Dialect = iota
	Postgres
)

func (d Dialect) String() string {
	if d == Postgres {
		return "postgres"
	}
	return "sqlite"
}

// ErrNotFound is returned by repositories when a row does not exist.
var ErrNotFound = errors.New("not found")

// DB wraps *sql.DB with the dialect.
type DB struct {
	*sql.DB
	Dialect Dialect
}

// Open connects using a kmdn db URL: sqlite://path/to/kmdn.db or postgres://…
func Open(ctx context.Context, url string) (*DB, error) {
	switch {
	case strings.HasPrefix(url, "sqlite://"):
		path := strings.TrimPrefix(url, "sqlite://")
		if path == "" {
			return nil, errors.New("store: sqlite path is empty")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
		dsn := "file:" + path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)&_txlock=immediate"
		sdb, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, err
		}
		sdb.SetMaxOpenConns(8)
		db := &DB{DB: sdb, Dialect: SQLite}
		return db, db.PingContext(ctx)
	case strings.HasPrefix(url, "postgres://"), strings.HasPrefix(url, "postgresql://"):
		dc, err := stdlib.GetDefaultDriver().(driver.DriverContext).OpenConnector(url)
		if err != nil {
			return nil, err
		}
		sdb := sql.OpenDB(scopedConnector{dc})
		sdb.SetMaxOpenConns(20)
		sdb.SetConnMaxIdleTime(5 * time.Minute)
		db := &DB{DB: sdb, Dialect: Postgres}
		return db, db.PingContext(ctx)
	default:
		return nil, fmt.Errorf("store: unsupported db url %q (use sqlite:// or postgres://)", url)
	}
}

// Rebind rewrites ? placeholders to $1, $2… for Postgres. Question marks inside
// single-quoted string literals are left alone.
func (db *DB) Rebind(q string) string {
	if db.Dialect != Postgres || !strings.Contains(q, "?") {
		return q
	}
	var b strings.Builder
	b.Grow(len(q) + 8)
	n, inStr := 0, false
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case c == '\'':
			inStr = !inStr
			b.WriteByte(c)
		case c == '?' && !inStr:
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Querier is satisfied by *DB and *Tx so repositories work in or out of a
// transaction.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	Rebind(string) string
	D() Dialect
}

func (db *DB) D() Dialect { return db.Dialect }

// Tx is a transaction that remembers its dialect.
type Tx struct {
	*sql.Tx
	dialect Dialect
	db      *DB
}

func (tx *Tx) Rebind(q string) string { return tx.db.Rebind(q) }
func (tx *Tx) D() Dialect             { return tx.dialect }

// InTx runs fn in a transaction, committing on success and rolling back on
// error or panic.
func (db *DB) InTx(ctx context.Context, fn func(*Tx) error) (err error) {
	stx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	tx := &Tx{Tx: stx, dialect: db.Dialect, db: db}
	defer func() {
		if p := recover(); p != nil {
			_ = stx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = stx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	return stx.Commit()
}

// Exec runs a statement with ? placeholders.
func Exec(ctx context.Context, q Querier, query string, args ...any) (sql.Result, error) {
	return q.ExecContext(ctx, q.Rebind(query), args...)
}

// Query runs a query with ? placeholders.
func Query(ctx context.Context, q Querier, query string, args ...any) (*sql.Rows, error) {
	return q.QueryContext(ctx, q.Rebind(query), args...)
}

// QueryRow runs a single-row query with ? placeholders.
func QueryRow(ctx context.Context, q Querier, query string, args ...any) *sql.Row {
	return q.QueryRowContext(ctx, q.Rebind(query), args...)
}

// NotFound maps sql.ErrNoRows to ErrNotFound.
func NotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// IsUniqueViolation reports whether err is a unique-constraint failure.
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "UNIQUE constraint failed") || strings.Contains(s, "SQLSTATE 23505") || strings.Contains(s, "duplicate key value")
}

// Timestamps are stored as integer Unix milliseconds in both engines.

// Millis converts a time to storage format.
func Millis(t time.Time) int64 { return t.UnixMilli() }

// FromMillis converts storage format to time.Time (UTC).
func FromMillis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// NullMillis converts a nullable column value.
func NullMillis(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := FromMillis(v.Int64)
	return &t
}
