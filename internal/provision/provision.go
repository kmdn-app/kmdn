// Package provision changes who is in an org from an outside source, such as
// a directory sync, without invitations (docs/specs/16-organizations.md#sign-in).
// Every change is audited with its source and reported as an event. The
// source owns what it syncs; people added in kmdn are left alone.
package provision

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/events"
	"github.com/kmdn-app/kmdn/internal/groups"
	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/policy"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// ErrLastOwner means the change would leave the org without an active owner.
var ErrLastOwner = errors.New("provision: an organization needs at least one owner")

// Service applies provisioning changes.
type Service struct {
	DB     *store.DB
	Policy *policy.Policy
	Events events.Sink
}

// Member is a person as the source sees them.
type Member struct {
	Email string
	Name  string
	// Role is member, admin or owner; "" keeps the current one (member
	// for someone new).
	Role string
	// Active false keeps the membership but takes away access, like
	// deactivating someone in the org console.
	Active bool
}

// Upsert makes m a member of the org: it creates the account if needed,
// adds or updates the membership and returns the user. The org's member
// limit applies to people it adds (a *policy.ErrLimit).
func (s *Service) Upsert(ctx context.Context, orgID, source string, m Member) (users.User, error) {
	email := users.NormalizeEmail(m.Email)
	if email == "" || !strings.Contains(email, "@") {
		return users.User{}, fmt.Errorf("provision: %q isn't an email address", m.Email)
	}
	if m.Role != "" && !orgs.ValidRole(m.Role) {
		return users.User{}, fmt.Errorf("provision: bad role %q", m.Role)
	}
	var u users.User
	var event string
	var data map[string]any
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		var err error
		u, err = users.ByEmail(ctx, tx, email)
		if errors.Is(err, store.ErrNotFound) {
			if u, err = users.Create(ctx, tx, email, m.Name, false); err != nil {
				return err
			}
			if err := audit.Write(ctx, tx, s.entry(orgID, source, "user.provisioned", u.ID, nil)); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		cur, err := membership(ctx, tx, orgID, u.ID)
		if err != nil {
			return err
		}
		status := orgs.Active
		if !m.Active {
			status = orgs.Deactivated
		}
		if cur == nil {
			if max := s.Policy.For(ctx, orgID).Members; max > 0 && m.Active {
				n, err := orgs.Seats(ctx, tx, orgID)
				if err != nil {
					return err
				}
				if n >= max {
					return s.Policy.Limit(ctx, orgID, "members", max)
				}
			}
			role := m.Role
			if role == "" {
				role = orgs.Member
			}
			if err := orgs.AddMember(ctx, tx, orgID, u.ID, role, ""); err != nil {
				return err
			}
			if status != orgs.Active {
				if err := orgs.SetMemberStatus(ctx, tx, orgID, u.ID, status); err != nil {
					return err
				}
			}
			event, data = events.MemberAdded, map[string]any{"via": source, "role": role, "status": status}
			return audit.Write(ctx, tx, s.entry(orgID, source, "org.member_added", u.ID, data))
		}
		role := cur.Role
		if m.Role != "" {
			role = m.Role
		}
		if role == cur.Role && status == cur.Status {
			return nil
		}
		// Keep an active owner.
		if cur.Role == orgs.Owner && cur.Status == orgs.Active && (role != orgs.Owner || status != orgs.Active) {
			if err := keepOwner(ctx, tx, orgID); err != nil {
				return err
			}
		}
		if role != cur.Role {
			if err := orgs.SetRole(ctx, tx, orgID, u.ID, role); err != nil {
				return err
			}
		}
		if status != cur.Status {
			if err := orgs.SetMemberStatus(ctx, tx, orgID, u.ID, status); err != nil {
				return err
			}
		}
		event, data = events.MemberChanged, map[string]any{"via": source, "role": role, "status": status}
		return audit.Write(ctx, tx, s.entry(orgID, source, "org.member_updated", u.ID, data))
	})
	if err == nil && event != "" {
		s.Events.Emit(ctx, events.Event{Type: event, OrgID: orgID, UserID: u.ID, Data: data})
	}
	return u, err
}

// Remove takes userID out of the org (their account stays).
func (s *Service) Remove(ctx context.Context, orgID, source, userID string) error {
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		cur, err := membership(ctx, tx, orgID, userID)
		if err != nil || cur == nil {
			return err
		}
		if cur.Role == orgs.Owner && cur.Status == orgs.Active {
			if err := keepOwner(ctx, tx, orgID); err != nil {
				return err
			}
		}
		// Groups are per org; leaving the org leaves its groups.
		if _, err := store.Exec(ctx, tx, `DELETE FROM group_members WHERE user_id = ? AND group_id IN (SELECT id FROM groups WHERE org_id = ?)`, userID, orgID); err != nil {
			return err
		}
		if err := orgs.RemoveMember(ctx, tx, orgID, userID); err != nil {
			return err
		}
		return audit.Write(ctx, tx, s.entry(orgID, source, "org.member_removed", userID, nil))
	})
	if err == nil {
		s.Events.Emit(ctx, events.Event{Type: events.MemberRemoved, OrgID: orgID, UserID: userID, Data: map[string]any{"via": source}})
	}
	return err
}

// EnsureGroup returns the org's group called name, creating it if needed.
func (s *Service) EnsureGroup(ctx context.Context, orgID, source, name string) (groups.Group, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return groups.Group{}, errors.New("provision: a group needs a name")
	}
	var g groups.Group
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		list, err := groups.List(ctx, tx, orgID)
		if err != nil {
			return err
		}
		if i := slices.IndexFunc(list, func(g groups.Group) bool { return strings.EqualFold(g.Name, name) }); i >= 0 {
			g = list[i]
			return nil
		}
		if g, err = groups.Create(ctx, tx, orgID, name, ""); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorSystem, OrgID: orgID, Action: "group.created", TargetType: "group", TargetID: g.ID, Data: map[string]any{"via": source, "name": name}})
	})
	return g, err
}

// SetGroupMembers makes the org's group groupID contain exactly userIDs,
// who must be in the org.
func (s *Service) SetGroupMembers(ctx context.Context, orgID, source, groupID string, userIDs []string) error {
	return s.DB.InTx(ctx, func(tx *store.Tx) error {
		g, err := groups.Get(ctx, tx, groupID)
		if err != nil {
			return err
		}
		if g.OrgID != orgID {
			return store.ErrNotFound
		}
		have, err := groups.MemberIDs(ctx, tx, groupID)
		if err != nil {
			return err
		}
		var added, removed []string
		for _, id := range userIDs {
			if slices.Contains(have, id) || slices.Contains(added, id) {
				continue
			}
			if cur, err := membership(ctx, tx, orgID, id); err != nil {
				return err
			} else if cur == nil {
				return fmt.Errorf("provision: user %s isn't in the organization", id)
			}
			if err := groups.AddMember(ctx, tx, groupID, id); err != nil {
				return err
			}
			added = append(added, id)
		}
		for _, id := range have {
			if !slices.Contains(userIDs, id) {
				if err := groups.RemoveMember(ctx, tx, groupID, id); err != nil {
					return err
				}
				removed = append(removed, id)
			}
		}
		if len(added)+len(removed) == 0 {
			return nil
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorSystem, OrgID: orgID, Action: "group.members_synced", TargetType: "group", TargetID: groupID,
			Data: map[string]any{"via": source, "added": added, "removed": removed}})
	})
}

// RenameGroup renames the org's group groupID.
func (s *Service) RenameGroup(ctx context.Context, orgID, source, groupID, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("provision: a group needs a name")
	}
	return s.DB.InTx(ctx, func(tx *store.Tx) error {
		g, err := groups.Get(ctx, tx, groupID)
		if err != nil || g.OrgID != orgID {
			return store.ErrNotFound
		}
		if err := groups.Update(ctx, tx, groupID, name, g.Description); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorSystem, OrgID: orgID, Action: "group.renamed", TargetType: "group", TargetID: groupID, Data: map[string]any{"via": source, "name": name}})
	})
}

// DeleteGroup deletes the org's group groupID (and the roles it granted).
func (s *Service) DeleteGroup(ctx context.Context, orgID, source, groupID string) error {
	return s.DB.InTx(ctx, func(tx *store.Tx) error {
		g, err := groups.Get(ctx, tx, groupID)
		if err != nil || g.OrgID != orgID {
			return store.ErrNotFound
		}
		if err := groups.Delete(ctx, tx, groupID); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorSystem, OrgID: orgID, Action: "group.deleted", TargetType: "group", TargetID: groupID, Data: map[string]any{"via": source, "name": g.Name}})
	})
}

func (s *Service) entry(orgID, source, action, userID string, data map[string]any) audit.Entry {
	if data == nil {
		data = map[string]any{}
	}
	data["via"] = source
	return audit.Entry{ActorType: audit.ActorSystem, OrgID: orgID, Action: action, TargetType: "user", TargetID: userID, Data: data}
}

func membership(ctx context.Context, q store.Querier, orgID, userID string) (*orgs.Membership, error) {
	var m orgs.Membership
	err := store.QueryRow(ctx, q, `SELECT role, status FROM org_members WHERE org_id = ? AND user_id = ?`, orgID, userID).Scan(&m.Role, &m.Status)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return nil, nil
	}
	return &m, err
}

func keepOwner(ctx context.Context, q store.Querier, orgID string) error {
	n, err := orgs.CountOwners(ctx, q, orgID)
	if err != nil {
		return err
	}
	if n <= 1 {
		return ErrLastOwner
	}
	return nil
}
