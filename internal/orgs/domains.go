package orgs

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/kmdn-app/kmdn/internal/store"
)

// Domain is an email domain an org claims: once verified, people signing in
// with an address there join the org (16-organizations.md#sign-in-and-joining).
type Domain struct {
	Domain     string     `json:"domain"`
	OrgID      string     `json:"org_id"`
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

var domainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// ErrDomain is returned for something that isn't a domain name.
var ErrDomain = errors.New("orgs: not a domain name")

// ErrDomainTaken means another org claims the domain.
var ErrDomainTaken = errors.New("orgs: domain claimed by another organization")

// NormalizeDomain lower-cases a domain ("@Acme.com" → "acme.com") and checks it.
func NormalizeDomain(s string) (string, error) {
	d := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "@")))
	if !domainRe.MatchString(d) || len(d) > 253 {
		return "", ErrDomain
	}
	return d, nil
}

// AddDomain claims a domain for an org, unverified. Another org's
// unverified claim doesn't block it (nobody can hold a domain they can't
// verify); a verified one does.
func AddDomain(ctx context.Context, q store.Querier, orgID, domain string) (Domain, error) {
	d, err := NormalizeDomain(domain)
	if err != nil {
		return Domain{}, err
	}
	if _, err := store.Exec(ctx, q, `INSERT INTO org_domains (domain, org_id, created_at) VALUES (?, ?, ?)
		ON CONFLICT (domain) DO UPDATE SET org_id = excluded.org_id, created_at = excluded.created_at WHERE org_domains.verified_at IS NULL`,
		d, orgID, store.Millis(time.Now())); err != nil {
		return Domain{}, err
	}
	got, err := domainRow(ctx, q, d)
	if err != nil {
		return Domain{}, err
	}
	if got.OrgID != orgID {
		return Domain{}, ErrDomainTaken
	}
	return got, nil
}

// RemoveDomain drops an org's claim.
func RemoveDomain(ctx context.Context, q store.Querier, orgID, domain string) error {
	return one(store.Exec(ctx, q, `DELETE FROM org_domains WHERE org_id = ? AND domain = ?`, orgID, strings.ToLower(domain)))
}

// VerifyDomain marks a claim verified (the deployment decides how: an
// instance admin, or a DNS check in a hosted service).
func VerifyDomain(ctx context.Context, q store.Querier, orgID, domain string) error {
	return one(store.Exec(ctx, q, `UPDATE org_domains SET verified_at = ? WHERE org_id = ? AND domain = ?`, store.Millis(time.Now()), orgID, strings.ToLower(domain)))
}

// Domains lists an org's domains.
func Domains(ctx context.Context, q store.Querier, orgID string) ([]Domain, error) {
	rows, err := store.Query(ctx, q, `SELECT domain, org_id, verified_at, created_at FROM org_domains WHERE org_id = ? ORDER BY domain`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Domain{}
	for rows.Next() {
		d, err := scanDomain(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func domainRow(ctx context.Context, q store.Querier, domain string) (Domain, error) {
	d, err := scanDomain(store.QueryRow(ctx, q, `SELECT domain, org_id, verified_at, created_at FROM org_domains WHERE domain = ?`, domain))
	return d, store.NotFound(err)
}

func scanDomain(r interface{ Scan(...any) error }) (Domain, error) {
	var d Domain
	var verified sql.NullInt64
	var created int64
	err := r.Scan(&d.Domain, &d.OrgID, &verified, &created)
	d.VerifiedAt, d.CreatedAt = store.NullMillis(verified), store.FromMillis(created)
	return d, err
}

// AutoJoinOrg returns the active org whose verified domain matches email's,
// or "".
func AutoJoinOrg(ctx context.Context, q store.Querier, email string) (string, error) {
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return "", nil
	}
	var org string
	err := store.QueryRow(ctx, q, `SELECT d.org_id FROM org_domains d JOIN orgs o ON o.id = d.org_id
		WHERE d.domain = ? AND d.verified_at IS NOT NULL AND o.status = ?`, strings.ToLower(email[at+1:]), Active).Scan(&org)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return org, err
}

// Join adds u to the org as a member unless they already have a membership
// row (active or deactivated: an org that deactivated someone keeps them out).
func Join(ctx context.Context, q store.Querier, orgID, userID string) error {
	_, err := store.Exec(ctx, q, `INSERT INTO org_members (org_id, user_id, role, status, joined_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT (org_id, user_id) DO NOTHING`,
		orgID, userID, Member, Active, store.Millis(time.Now()))
	return err
}
