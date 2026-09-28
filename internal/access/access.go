// Package access resolves per-repo roles. Roles are kmdn-managed only
// (docs/specs/09-auth-permissions.md#roles-per-repo): a user's effective role
// is the highest of their direct grant and the grants of their groups.
// Roles only count inside the repo's organization
// (docs/specs/16-organizations.md#roles): people outside it have none, org
// admins and owners are Admin on its repos, and instance admins are Admin
// everywhere.
package access

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// Role is a per-repo role.
type Role string

const (
	None        Role = ""
	Viewer      Role = "viewer"
	Contributor Role = "contributor"
	Maintainer  Role = "maintainer"
	Admin       Role = "admin"
)

var rank = map[Role]int{None: 0, Viewer: 1, Contributor: 2, Maintainer: 3, Admin: 4}

// Parse validates a role name.
func Parse(s string) (Role, error) {
	r := Role(s)
	if r == None || rank[r] == 0 {
		return None, fmt.Errorf("unknown role %q (use viewer, contributor, maintainer or admin)", s)
	}
	return r, nil
}

// AtLeast reports whether r grants min.
func (r Role) AtLeast(min Role) bool { return rank[r] >= rank[min] }

func maxRole(a, b Role) Role {
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// Principal types.
const (
	UserPrincipal  = "user"
	GroupPrincipal = "group"
)

// Effective returns u's role on repoID.
func Effective(ctx context.Context, q store.Querier, u users.User, repoID string) (Role, error) {
	if u.IsInstanceAdmin {
		return Admin, nil
	}
	if u.Status != users.Active {
		return None, nil
	}
	var orgID, status string
	if err := store.QueryRow(ctx, q, `SELECT r.org_id, o.status FROM repos r JOIN orgs o ON o.id = r.org_id WHERE r.id = ?`, repoID).Scan(&orgID, &status); errors.Is(err, sql.ErrNoRows) {
		return None, nil
	} else if err != nil {
		return None, err
	}
	orgRole, err := orgs.Role(ctx, q, orgID, u)
	if err != nil || orgRole == "" {
		return None, err
	}
	// A suspended org is read-only: people can read and comment, nothing more.
	if status == orgs.Suspended {
		return Viewer, nil
	}
	if orgs.AtLeast(orgRole, orgs.Admin) {
		return Admin, nil
	}
	rows, err := store.Query(ctx, q, `SELECT role FROM repo_members WHERE repo_id = ? AND (
		(principal_type = 'user' AND principal_id = ?) OR
		(principal_type = 'group' AND principal_id IN (SELECT group_id FROM group_members WHERE user_id = ?)))`, repoID, u.ID, u.ID)
	if err != nil {
		return None, err
	}
	defer rows.Close()
	best := None
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return None, err
		}
		best = maxRole(best, Role(r))
	}
	return best, rows.Err()
}

// RepoIDs returns the repos of orgID that u can see (any role): all of
// them for instance admins and org admins, the granted ones for members,
// none outside the org.
func RepoIDs(ctx context.Context, q store.Querier, u users.User, orgID string) ([]string, error) {
	if u.Status != users.Active {
		return nil, nil
	}
	all := u.IsInstanceAdmin
	if !all {
		role, err := orgs.Role(ctx, q, orgID, u)
		if err != nil || role == "" {
			return nil, err
		}
		all = orgs.AtLeast(role, orgs.Admin)
	}
	query := `SELECT DISTINCT m.repo_id FROM repo_members m JOIN repos r ON r.id = m.repo_id WHERE r.org_id = ? AND (
		(m.principal_type = 'user' AND m.principal_id = ?) OR
		(m.principal_type = 'group' AND m.principal_id IN (SELECT group_id FROM group_members WHERE user_id = ?)))`
	args := []any{orgID, u.ID, u.ID}
	if all {
		query, args = `SELECT id FROM repos WHERE org_id = ?`, []any{orgID}
	}
	rows, err := store.Query(ctx, q, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Grant sets a principal's role on a repo.
func Grant(ctx context.Context, q store.Querier, repoID, principalType, principalID string, role Role) error {
	if principalType != UserPrincipal && principalType != GroupPrincipal {
		return fmt.Errorf("unknown principal type %q", principalType)
	}
	_, err := store.Exec(ctx, q, `INSERT INTO repo_members (repo_id, principal_type, principal_id, role, added_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (repo_id, principal_type, principal_id) DO UPDATE SET role = excluded.role`,
		repoID, principalType, principalID, string(role), store.Millis(time.Now()))
	return err
}

// Revoke removes a principal's grant.
func Revoke(ctx context.Context, q store.Querier, repoID, principalType, principalID string) error {
	_, err := store.Exec(ctx, q, `DELETE FROM repo_members WHERE repo_id = ? AND principal_type = ? AND principal_id = ?`, repoID, principalType, principalID)
	return err
}

// Member is a grant on a repo, with display info.
type Member struct {
	Type    string `json:"type"` // user | group
	ID      string `json:"id"`
	Name    string `json:"name"`
	Email   string `json:"email,omitempty"`
	Role    Role   `json:"role"`
	Members int    `json:"members,omitempty"` // for groups
}

// Members lists grants on a repo: users first, then groups.
func Members(ctx context.Context, q store.Querier, repoID string) ([]Member, error) {
	rows, err := store.Query(ctx, q, `SELECT m.principal_type, m.principal_id, m.role,
			COALESCE(u.name, g.name, ''), COALESCE(u.email, ''),
			(SELECT COUNT(*) FROM group_members gm WHERE gm.group_id = g.id)
		FROM repo_members m
		LEFT JOIN users u ON m.principal_type = 'user' AND u.id = m.principal_id
		LEFT JOIN groups g ON m.principal_type = 'group' AND g.id = m.principal_id
		WHERE m.repo_id = ? ORDER BY m.principal_type DESC, 4`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var m Member
		var role string
		var count sql.NullInt64
		if err := rows.Scan(&m.Type, &m.ID, &role, &m.Name, &m.Email, &count); err != nil {
			return nil, err
		}
		m.Role = Role(role)
		if m.Type == GroupPrincipal {
			m.Members = int(count.Int64)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ErrForbidden is returned by Require when the role is insufficient.
var ErrForbidden = errors.New("access: forbidden")

// Require returns the effective role or ErrForbidden if below min.
func Require(ctx context.Context, q store.Querier, u users.User, repoID string, min Role) (Role, error) {
	r, err := Effective(ctx, q, u, repoID)
	if err != nil {
		return r, err
	}
	if !r.AtLeast(min) {
		return r, ErrForbidden
	}
	return r, nil
}
