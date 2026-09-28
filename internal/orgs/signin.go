package orgs

import "context"

type gateKey struct{}

// Gate says why the request's session may not act in orgID (nil: it may).
type Gate func(ctx context.Context, orgID string) error

// WithSignInGate attaches the sign-in policy for the request's session
// (docs/specs/16-organizations.md#sign-in).
func WithSignInGate(ctx context.Context, g Gate) context.Context {
	return context.WithValue(ctx, gateKey{}, g)
}

// SignInBlocked reports why the request's session may not act in orgID
// (nil when it may, or when there's no session, as in background jobs).
func SignInBlocked(ctx context.Context, orgID string) error {
	g, _ := ctx.Value(gateKey{}).(Gate)
	if g == nil {
		return nil
	}
	return g(ctx, orgID)
}

func signInBlocked(ctx context.Context, orgID string) bool { return SignInBlocked(ctx, orgID) != nil }
