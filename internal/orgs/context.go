package orgs

import "context"

// Current is the org a request is about, resolved from its path, with the
// caller's role in it.
type Current struct {
	Org Org
	// Role is the caller's role; "" for an instance admin reaching into an
	// org they don't belong to.
	Role string
}

// Admin reports whether the caller administers the org (org admin or owner,
// or an instance admin).
func (c Current) Admin(instanceAdmin bool) bool { return instanceAdmin || AtLeast(c.Role, Admin) }

type ctxKey struct{}

// WithCurrent stores the resolved org in ctx.
func WithCurrent(ctx context.Context, c Current) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

// FromContext returns the org resolved for the request, if any.
func FromContext(ctx context.Context) (Current, bool) {
	c, ok := ctx.Value(ctxKey{}).(Current)
	return c, ok
}
