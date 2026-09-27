// Package orgs is the repository for organizations and their members
// (docs/specs/16-organizations.md).
package orgs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// DefaultID is the org every install starts with; in single mode it is the
// only one.
const DefaultID = "org_default"

// Modes (config orgs.mode).
const (
	Single = "single"
	Multi  = "multi"
)

// Member roles, in increasing order of rights.
const (
	Member = "member"
	Admin  = "admin"
	Owner  = "owner"
)

// Org and member statuses.
const (
	Active      = "active"
	Suspended   = "suspended"
	Deleting    = "deleting"
	Deactivated = "deactivated"
)

var rank = map[string]int{"": 0, Member: 1, Admin: 2, Owner: 3}

// AtLeast reports whether role grants at least min.
func AtLeast(role, min string) bool { return rank[role] >= rank[min] }

// ValidRole reports whether r is a member role.
func ValidRole(r string) bool { return r == Member || r == Admin || r == Owner }

// Org is an organization.
type Org struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// Membership is a person's place in an org.
type Membership struct {
	OrgID     string    `json:"org_id"`
	UserID    string    `json:"user_id"`
	Role      string    `json:"role"`
	Status    string    `json:"status"`
	InvitedBy string    `json:"invited_by,omitempty"`
	JoinedAt  time.Time `json:"joined_at"`
}

// Reserved are the first URL segments the app uses, which can't be slugs
// (16-organizations.md#urls-and-the-request-context).
var Reserved = []string{"admin", "settings", "signin", "signup", "setup", "invite", "auth", "api", "ws", "mcp", "hooks",
	"assets", "healthz", "readyz", "metrics", "version", "orgs", "new"}

var slugRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ErrSlug describes why a slug was refused.
type ErrSlug struct{ Reason string }

func (e *ErrSlug) Error() string { return e.Reason }

// ValidSlug checks an org slug: 2–39 lowercase letters, digits and single
// hyphens, not a reserved word.
func ValidSlug(s string) error {
	switch {
	case len(s) < 2 || len(s) > 39:
		return &ErrSlug{"Use 2 to 39 characters."}
	case !slugRe.MatchString(s):
		return &ErrSlug{"Use lowercase letters, digits and single hyphens, not at the start or end."}
	case slices.Contains(Reserved, s):
		return &ErrSlug{fmt.Sprintf("%q is reserved; choose another.", s)}
	}
	return nil
}

// Slugify turns a name into a candidate slug ("Northwind Docs" →
// "northwind-docs"); the result may still need a suffix to be unique.
func Slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.TrimSuffix(b.String(), "-")
	if len(s) > 39 {
		s = strings.TrimSuffix(s[:39], "-")
	}
	if len(s) < 2 || slices.Contains(Reserved, s) {
		s = "org-" + s
	}
	return strings.TrimSuffix(s, "-")
}

// ErrSlugTaken is returned when another org has the slug.
var ErrSlugTaken = errors.New("orgs: slug taken")

const cols = `id, slug, name, status, created_at`

func scan(r interface{ Scan(...any) error }) (Org, error) {
	var o Org
	var at int64
	err := r.Scan(&o.ID, &o.Slug, &o.Name, &o.Status, &at)
	o.CreatedAt = store.FromMillis(at)
	return o, err
}

// Create inserts an org with owner as its first owner.
func Create(ctx context.Context, q store.Querier, slug, name, owner string) (Org, error) {
	if err := ValidSlug(slug); err != nil {
		return Org{}, err
	}
	if _, err := BySlug(ctx, q, slug); err == nil {
		return Org{}, ErrSlugTaken
	} else if !errors.Is(err, store.ErrNotFound) {
		return Org{}, err
	}
	o := Org{ID: ids.New(ids.Org), Slug: slug, Name: strings.TrimSpace(name), Status: Active, CreatedAt: time.Now().UTC().Truncate(time.Millisecond)}
	if _, err := store.Exec(ctx, q, `INSERT INTO orgs (id, slug, name, status, created_at) VALUES (?, ?, ?, ?, ?)`,
		o.ID, o.Slug, o.Name, o.Status, store.Millis(o.CreatedAt)); err != nil {
		return Org{}, err
	}
	if owner != "" {
		if err := AddMember(ctx, q, o.ID, owner, Owner, ""); err != nil {
			return Org{}, err
		}
	}
	return o, nil
}

// ByID loads an org.
func ByID(ctx context.Context, q store.Querier, id string) (Org, error) {
	o, err := scan(store.QueryRow(ctx, q, `SELECT `+cols+` FROM orgs WHERE id = ?`, id))
	return o, store.NotFound(err)
}

// BySlug loads an org by slug.
func BySlug(ctx context.Context, q store.Querier, slug string) (Org, error) {
	o, err := scan(store.QueryRow(ctx, q, `SELECT `+cols+` FROM orgs WHERE slug = ?`, slug))
	return o, store.NotFound(err)
}

// List returns every org, by name (instance console).
func List(ctx context.Context, q store.Querier) ([]Org, error) {
	return list(ctx, q, `SELECT `+cols+` FROM orgs WHERE status <> ? ORDER BY name, slug`, Deleting)
}

// modeKey is where the instance's mode lives (instance_settings, JSON), so
// every access check agrees on it without the config at hand. A missing row
// means single mode.
const modeKey = "orgs_mode"

// Mode returns the instance's mode.
func Mode(ctx context.Context, q store.Querier) (string, error) {
	var v string
	err := store.QueryRow(ctx, q, `SELECT value FROM instance_settings WHERE key = ?`, modeKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) || v == `"single"` {
		return Single, nil
	}
	if err != nil {
		return "", err
	}
	return Multi, nil
}

// ErrModeChange is returned when single mode is asked for while several
// orgs exist.
var ErrModeChange = errors.New("orgs.mode can't go back to single while more than one organization exists")

// SetMode records the configured mode at startup. Going back to single mode
// is refused while more than one org exists.
func SetMode(ctx context.Context, q store.Querier, mode string) error {
	if mode != Single && mode != Multi {
		return fmt.Errorf("orgs: bad mode %q", mode)
	}
	if mode == Single {
		var n int
		if err := store.QueryRow(ctx, q, `SELECT COUNT(*) FROM orgs WHERE status <> ?`, Deleting).Scan(&n); err != nil {
			return err
		}
		if n > 1 {
			return ErrModeChange
		}
	}
	_, err := store.Exec(ctx, q, `INSERT INTO instance_settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, modeKey, `"`+mode+`"`, store.Millis(time.Now()))
	return err
}

// ForUser returns the orgs u belongs to, by name. In single mode that is
// the default org for every active account.
func ForUser(ctx context.Context, q store.Querier, u users.User) ([]Org, error) {
	mode, err := Mode(ctx, q)
	if err != nil {
		return nil, err
	}
	if mode == Single {
		if u.Status != users.Active {
			return nil, nil
		}
		o, err := ByID(ctx, q, DefaultID)
		if err != nil {
			return nil, err
		}
		return []Org{o}, nil
	}
	return list(ctx, q, `SELECT o.id, o.slug, o.name, o.status, o.created_at FROM orgs o
		JOIN org_members m ON m.org_id = o.id
		WHERE m.user_id = ? AND m.status = ? AND o.status <> ? ORDER BY o.name, o.slug`, u.ID, Active, Deleting)
}

func list(ctx context.Context, q store.Querier, query string, args ...any) ([]Org, error) {
	rows, err := store.Query(ctx, q, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Org
	for rows.Next() {
		o, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Role returns u's role in the org, or "" when u isn't an active member.
// In single mode every active account is a member of the default org and
// instance admins are its owners, whether or not a membership row exists;
// a deactivated row still removes access.
func Role(ctx context.Context, q store.Querier, orgID string, u users.User) (string, error) {
	if u.Status != users.Active {
		return "", nil
	}
	// One round trip: the membership row (if any) and the instance mode.
	var role, status, mode sql.NullString
	err := store.QueryRow(ctx, q, `SELECT m.role, m.status, s.value FROM (SELECT 1 AS one) x
		LEFT JOIN org_members m ON m.org_id = ? AND m.user_id = ?
		LEFT JOIN instance_settings s ON s.key = ?`, orgID, u.ID, modeKey).Scan(&role, &status, &mode)
	if err != nil {
		return "", err
	}
	if status.String == Deactivated {
		return "", nil
	}
	if !mode.Valid || mode.String == `"single"` {
		if orgID != DefaultID {
			return "", nil
		}
		if u.IsInstanceAdmin {
			return Owner, nil
		}
		if role.String == "" {
			return Member, nil
		}
	}
	return role.String, nil
}

// Belongs reports whether u is in the org whatever the account's own
// status (a deactivated account can still be granted roles, which count
// again once it's reactivated).
func Belongs(ctx context.Context, q store.Querier, orgID string, u users.User) (bool, error) {
	u.Status = users.Active
	role, err := Role(ctx, q, orgID, u)
	return role != "", err
}

// AddMember adds userID to the org with role, or reactivates and updates an
// existing membership.
func AddMember(ctx context.Context, q store.Querier, orgID, userID, role, invitedBy string) error {
	if !ValidRole(role) {
		return fmt.Errorf("orgs: bad role %q", role)
	}
	var by any
	if invitedBy != "" {
		by = invitedBy
	}
	_, err := store.Exec(ctx, q, `INSERT INTO org_members (org_id, user_id, role, status, invited_by, joined_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (org_id, user_id) DO UPDATE SET role = excluded.role, status = excluded.status`,
		orgID, userID, role, Active, by, store.Millis(time.Now()))
	return err
}

// SetRole changes an existing member's role.
func SetRole(ctx context.Context, q store.Querier, orgID, userID, role string) error {
	if !ValidRole(role) {
		return fmt.Errorf("orgs: bad role %q", role)
	}
	return one(store.Exec(ctx, q, `UPDATE org_members SET role = ? WHERE org_id = ? AND user_id = ?`, role, orgID, userID))
}

// SetMemberStatus activates or deactivates a membership.
func SetMemberStatus(ctx context.Context, q store.Querier, orgID, userID, status string) error {
	if status != Active && status != Deactivated {
		return fmt.Errorf("orgs: bad member status %q", status)
	}
	return one(store.Exec(ctx, q, `UPDATE org_members SET status = ? WHERE org_id = ? AND user_id = ?`, status, orgID, userID))
}

// RemoveMember deletes a membership.
func RemoveMember(ctx context.Context, q store.Querier, orgID, userID string) error {
	return one(store.Exec(ctx, q, `DELETE FROM org_members WHERE org_id = ? AND user_id = ?`, orgID, userID))
}

// Members lists an org's membership rows, oldest first.
func Members(ctx context.Context, q store.Querier, orgID string) ([]Membership, error) {
	rows, err := store.Query(ctx, q, `SELECT org_id, user_id, role, status, COALESCE(invited_by, ''), joined_at FROM org_members WHERE org_id = ? ORDER BY joined_at, user_id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Membership
	for rows.Next() {
		var m Membership
		var at int64
		if err := rows.Scan(&m.OrgID, &m.UserID, &m.Role, &m.Status, &m.InvitedBy, &at); err != nil {
			return nil, err
		}
		m.JoinedAt = store.FromMillis(at)
		out = append(out, m)
	}
	return out, rows.Err()
}

// CountOwners returns the number of active owners, so the last one can't
// leave or be demoted. In single mode, instance admins are owners of the
// default org too.
func CountOwners(ctx context.Context, q store.Querier, orgID string) (int, error) {
	mode, err := Mode(ctx, q)
	if err != nil {
		return 0, err
	}
	admins := mode == Single && orgID == DefaultID
	var n int
	err = store.QueryRow(ctx, q, `SELECT COUNT(*) FROM users u LEFT JOIN org_members m ON m.org_id = ? AND m.user_id = u.id
		WHERE u.status = ? AND COALESCE(m.status, '') <> ? AND (m.role = ? OR (? AND u.is_instance_admin = ?))`,
		orgID, users.Active, Deactivated, Owner, admins, true).Scan(&n)
	return n, err
}

// MemberClause returns a WHERE condition on the user id column col that
// keeps the org's active members: every active account without a
// deactivated membership for the default org in single mode, the active
// membership rows otherwise.
func MemberClause(ctx context.Context, q store.Querier, orgID, col string) (string, []any, error) {
	mode, err := Mode(ctx, q)
	if err != nil {
		return "", nil, err
	}
	if mode == Single {
		if orgID != DefaultID {
			return "1 = 0", nil, nil
		}
		return col + ` NOT IN (SELECT user_id FROM org_members WHERE org_id = ? AND status = ?)`, []any{orgID, Deactivated}, nil
	}
	return col + ` IN (SELECT user_id FROM org_members WHERE org_id = ? AND status = ?)`, []any{orgID, Active}, nil
}

// Rename changes an org's display name.
func Rename(ctx context.Context, q store.Querier, id, name string) error {
	return one(store.Exec(ctx, q, `UPDATE orgs SET name = ? WHERE id = ?`, strings.TrimSpace(name), id))
}

// SetSlug changes an org's slug; old URLs stop working.
func SetSlug(ctx context.Context, q store.Querier, id, slug string) error {
	if err := ValidSlug(slug); err != nil {
		return err
	}
	if o, err := BySlug(ctx, q, slug); err == nil && o.ID != id {
		return ErrSlugTaken
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return one(store.Exec(ctx, q, `UPDATE orgs SET slug = ? WHERE id = ?`, slug, id))
}

// SetStatus suspends, resumes or marks an org for deletion.
func SetStatus(ctx context.Context, q store.Querier, id, status string) error {
	if status != Active && status != Suspended && status != Deleting {
		return fmt.Errorf("orgs: bad status %q", status)
	}
	return one(store.Exec(ctx, q, `UPDATE orgs SET status = ? WHERE id = ?`, status, id))
}

func one(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}
