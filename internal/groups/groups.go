// Package groups manages an organization's groups of users. Groups can be
// granted roles on the org's repositories (see package access).
package groups

import (
	"context"
	"time"

	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/store"
)

// Group is a named set of users.
type Group struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Members     int       `json:"members"`
	CreatedAt   time.Time `json:"created_at"`
}

// Create adds a group to an org.
func Create(ctx context.Context, q store.Querier, orgID, name, desc string) (Group, error) {
	g := Group{ID: ids.New(ids.Group), OrgID: orgID, Name: name, Description: desc, CreatedAt: time.Now().UTC().Truncate(time.Millisecond)}
	_, err := store.Exec(ctx, q, `INSERT INTO groups (id, org_id, name, description, created_at) VALUES (?, ?, ?, ?, ?)`, g.ID, orgID, name, desc, store.Millis(g.CreatedAt))
	return g, err
}

// List returns an org's groups with member counts.
func List(ctx context.Context, q store.Querier, orgID string) ([]Group, error) {
	rows, err := store.Query(ctx, q, `SELECT g.id, g.org_id, g.name, g.description, g.created_at, (SELECT COUNT(*) FROM group_members m WHERE m.group_id = g.id) FROM groups g WHERE g.org_id = ? ORDER BY g.name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Group{}
	for rows.Next() {
		var g Group
		var at int64
		if err := rows.Scan(&g.ID, &g.OrgID, &g.Name, &g.Description, &at, &g.Members); err != nil {
			return nil, err
		}
		g.CreatedAt = store.FromMillis(at)
		out = append(out, g)
	}
	return out, rows.Err()
}

// Get returns one group.
func Get(ctx context.Context, q store.Querier, id string) (Group, error) {
	var g Group
	var at int64
	err := store.QueryRow(ctx, q, `SELECT g.id, g.org_id, g.name, g.description, g.created_at, (SELECT COUNT(*) FROM group_members m WHERE m.group_id = g.id) FROM groups g WHERE g.id = ?`, id).
		Scan(&g.ID, &g.OrgID, &g.Name, &g.Description, &at, &g.Members)
	g.CreatedAt = store.FromMillis(at)
	return g, store.NotFound(err)
}

// Update renames or re-describes a group.
func Update(ctx context.Context, q store.Querier, id, name, desc string) error {
	_, err := store.Exec(ctx, q, `UPDATE groups SET name = ?, description = ? WHERE id = ?`, name, desc, id)
	return err
}

// Delete removes a group and its grants.
func Delete(ctx context.Context, q store.Querier, id string) error {
	if _, err := store.Exec(ctx, q, `DELETE FROM repo_members WHERE principal_type = 'group' AND principal_id = ?`, id); err != nil {
		return err
	}
	_, err := store.Exec(ctx, q, `DELETE FROM groups WHERE id = ?`, id)
	return err
}

// AddMember adds a user (idempotent).
func AddMember(ctx context.Context, q store.Querier, groupID, userID string) error {
	_, err := store.Exec(ctx, q, `INSERT INTO group_members (group_id, user_id, added_at) VALUES (?, ?, ?) ON CONFLICT (group_id, user_id) DO NOTHING`,
		groupID, userID, store.Millis(time.Now()))
	return err
}

// RemoveMember removes a user.
func RemoveMember(ctx context.Context, q store.Querier, groupID, userID string) error {
	_, err := store.Exec(ctx, q, `DELETE FROM group_members WHERE group_id = ? AND user_id = ?`, groupID, userID)
	return err
}

// MemberIDs lists user ids in a group.
func MemberIDs(ctx context.Context, q store.Querier, groupID string) ([]string, error) {
	rows, err := store.Query(ctx, q, `SELECT user_id FROM group_members WHERE group_id = ? ORDER BY added_at`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
