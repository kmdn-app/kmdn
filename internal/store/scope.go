package store

import (
	"context"
	"database/sql/driver"
	"errors"

	"github.com/kmdn-app/kmdn/internal/telemetry"
)

// Row-level security on Postgres (docs/specs/16-organizations.md#isolation):
// tenant tables have a policy that shows only rows of the org in the session
// setting app.org_id, or every row when it's empty. A context scoped with
// WithOrg makes every statement run with it; unscoped contexts (jobs,
// webhooks, instance routes) see everything. The service layer's filters are
// the primary guarantee; this catches one that's missing.
//
// The setting lives on the connection. Each connection remembers what it
// last set, so a statement costs an extra round trip only when its scope
// differs from the previous one's on that connection.

type scopeKey struct{}

// WithOrg scopes the Postgres statements run with ctx to orgID. Logs and
// traces from ctx carry the org too (telemetry.WithOrg).
func WithOrg(ctx context.Context, orgID string) context.Context {
	return context.WithValue(telemetry.WithOrg(ctx, orgID), scopeKey{}, orgID)
}

// OrgScope returns the org ctx is scoped to ("" when unscoped).
func OrgScope(ctx context.Context) string {
	s, _ := ctx.Value(scopeKey{}).(string)
	return s
}

// Unscoped drops the org scope, for work that spans orgs on behalf of the
// instance.
func Unscoped(ctx context.Context) context.Context {
	return context.WithValue(ctx, scopeKey{}, "")
}

type scopedConnector struct{ driver.Connector }

func (c scopedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	dc, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &scopedConn{Conn: dc}, nil
}

func (c scopedConnector) Driver() driver.Driver { return c.Connector.Driver() }

var errNoContext = errors.New("store: driver connection lacks context methods")

type scopedConn struct {
	driver.Conn
	cur   string
	known bool // cur is the session's setting
	// setInTx: the setting changed inside the open transaction, so a
	// rollback undoes it.
	inTx, setInTx bool
}

func (c *scopedConn) apply(ctx context.Context) error {
	want := OrgScope(ctx)
	if c.known && c.cur == want {
		return nil
	}
	ex, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return errNoContext
	}
	c.known = false
	if _, err := ex.ExecContext(ctx, "SELECT set_config('app.org_id', $1, false)", []driver.NamedValue{{Ordinal: 1, Value: want}}); err != nil {
		return err
	}
	c.cur, c.known = want, true
	if c.inTx {
		c.setInTx = true
	}
	return nil
}

func (c *scopedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	ex, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	if err := c.apply(ctx); err != nil {
		return nil, err
	}
	return ex.ExecContext(ctx, query, args)
}

func (c *scopedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	if err := c.apply(ctx); err != nil {
		return nil, err
	}
	return q.QueryContext(ctx, query, args)
}

// PrepareContext scopes the statement to the context it's prepared with.
func (c *scopedConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	p, ok := c.Conn.(driver.ConnPrepareContext)
	if !ok {
		return c.Prepare(query)
	}
	if err := c.apply(ctx); err != nil {
		return nil, err
	}
	return p.PrepareContext(ctx, query)
}

func (c *scopedConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	b, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return nil, errNoContext
	}
	// Outside the transaction, so it survives a rollback.
	if err := c.apply(ctx); err != nil {
		return nil, err
	}
	tx, err := b.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	c.inTx, c.setInTx = true, false
	return &scopedTx{Tx: tx, c: c}, nil
}

func (c *scopedConn) Ping(ctx context.Context) error {
	if p, ok := c.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *scopedConn) ResetSession(ctx context.Context) error {
	if r, ok := c.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *scopedConn) IsValid() bool {
	if v, ok := c.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

func (c *scopedConn) CheckNamedValue(v *driver.NamedValue) error {
	if n, ok := c.Conn.(driver.NamedValueChecker); ok {
		return n.CheckNamedValue(v)
	}
	return driver.ErrSkip
}

type scopedTx struct {
	driver.Tx
	c *scopedConn
}

func (t *scopedTx) Commit() error {
	err := t.Tx.Commit()
	if err != nil && t.c.setInTx {
		t.c.known = false // a failed commit rolls back
	}
	t.c.inTx = false
	return err
}

func (t *scopedTx) Rollback() error {
	if t.c.setInTx {
		t.c.known = false
	}
	t.c.inTx = false
	return t.Tx.Rollback()
}
