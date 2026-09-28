// Package lifecycle deletes organizations and erases accounts
// (docs/specs/16-organizations.md#lifecycle). Deleting an org is two steps:
// a request makes it unreachable at once and can be undone for Grace; then
// Purge removes its rows, repository mirrors and uploads, and destroys its
// key, so any copy of its secrets left in a backup can't be read.
// Erasing an account removes the person's data and keeps what they wrote,
// attributed to "Deleted user".
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/blobs"
	"github.com/kmdn-app/kmdn/internal/events"
	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/secrets"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// DefaultGrace is how long a deleted org can be restored.
const DefaultGrace = 30 * 24 * time.Hour

// Service deletes orgs and erases accounts.
type Service struct {
	DB      *store.DB
	Repos   *repos.Service
	Blobs   blobs.Store
	Secrets *secrets.Store
	Events  events.Sink
	Log     *slog.Logger
	Grace   time.Duration
}

func (s *Service) grace() time.Duration {
	if s.Grace > 0 {
		return s.Grace
	}
	return DefaultGrace
}

// Errors.
var (
	ErrDefaultOrg = errors.New("lifecycle: the default org can't be deleted in single mode")
	ErrNotDeleted = errors.New("lifecycle: the org isn't being deleted")
)

// SoleOwnerError means the person is the only owner of Org (a slug), which
// has other members: someone else must become an owner first.
type SoleOwnerError struct{ Org string }

func (e *SoleOwnerError) Error() string {
	return "lifecycle: the only owner of " + e.Org + ", which has other members"
}

// RequestDeletion makes orgID unreachable now and purgeable after Grace.
func (s *Service) RequestDeletion(ctx context.Context, orgID string, a audit.Entry) error {
	if orgID == orgs.DefaultID {
		if mode, err := orgs.Mode(ctx, s.DB); err != nil || mode == orgs.Single {
			return ErrDefaultOrg
		}
	}
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		if err := orgs.SetStatus(ctx, tx, orgID, orgs.Deleting, "This organization is being deleted."); err != nil {
			return err
		}
		if _, err := store.Exec(ctx, tx, `UPDATE orgs SET deleted_at = ? WHERE id = ?`, store.Millis(time.Now()), orgID); err != nil {
			return err
		}
		a.OrgID, a.Action, a.TargetType, a.TargetID = orgID, "org.deletion_requested", "org", orgID
		return audit.Write(ctx, tx, a)
	})
	if err == nil {
		s.Events.Emit(ctx, events.Event{Type: events.OrgStatusChanged, OrgID: orgID, Data: map[string]any{"status": orgs.Deleting}})
	}
	return err
}

// Restore undoes a deletion request that hasn't been purged yet.
func (s *Service) Restore(ctx context.Context, orgID string, a audit.Entry) error {
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		res, err := store.Exec(ctx, tx, `UPDATE orgs SET status = ?, status_reason = '', deleted_at = NULL WHERE id = ? AND status = ?`, orgs.Active, orgID, orgs.Deleting)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotDeleted
		}
		a.OrgID, a.Action, a.TargetType, a.TargetID = orgID, "org.restored", "org", orgID
		return audit.Write(ctx, tx, a)
	})
	if err == nil {
		s.Events.Emit(ctx, events.Event{Type: events.OrgStatusChanged, OrgID: orgID, Data: map[string]any{"status": orgs.Active}})
	}
	return err
}

// tenant tables with their own org_id column, in an order that respects
// foreign keys; everything under a repo, group, key or thread goes with it
// (ON DELETE CASCADE), and org_members, org_settings, org_domains,
// org_keys and install_claims go with the org.
var tenant = []string{"repos", "assistant_threads", "assistant_runs", "notifications", "invites", "groups", "agent_keys", "jobs", "webhook_deliveries", "audit_log", "uploads", "forge_hosts"}

// Purge deletes orgID for good. It must be being deleted.
func (s *Service) Purge(ctx context.Context, orgID string) error {
	var status string
	if err := store.QueryRow(ctx, s.DB, `SELECT status FROM orgs WHERE id = ?`, orgID).Scan(&status); err != nil {
		return store.NotFound(err)
	}
	if status != orgs.Deleting {
		return ErrNotDeleted
	}
	list, err := repos.ListIn(ctx, s.DB, orgID)
	if err != nil {
		return err
	}
	var mirrors []string
	for _, r := range list {
		mirrors = append(mirrors, s.Repos.Mirror(r).Path)
	}
	var uploads []string
	rows, err := store.Query(ctx, s.DB, `SELECT sha256 FROM uploads WHERE org_id = ?`, orgID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			_ = rows.Close()
			return err
		}
		uploads = append(uploads, blobs.Key(orgID, sha))
	}
	_ = rows.Close()
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		// Secrets first: without the org's key, nothing it encrypted can be
		// read, wherever a copy survives.
		if err := s.Secrets.DestroyOrgKey(ctx, tx, orgID); err != nil {
			return err
		}
		for _, t := range tenant {
			if _, err := store.Exec(ctx, tx, `DELETE FROM `+t+` WHERE org_id = ?`, orgID); err != nil {
				return fmt.Errorf("%s: %w", t, err)
			}
		}
		if _, err := store.Exec(ctx, tx, `DELETE FROM orgs WHERE id = ?`, orgID); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorSystem, Action: "org.purged", TargetType: "org", TargetID: orgID})
	})
	if err != nil {
		return err
	}
	for _, p := range mirrors {
		if err := os.RemoveAll(p); err != nil {
			s.Log.Error("purge mirror", "org", orgID, "err", err)
		}
	}
	for _, k := range uploads {
		if err := s.Blobs.Delete(ctx, k); err != nil && !errors.Is(err, blobs.ErrNotFound) {
			s.Log.Error("purge upload", "org", orgID, "err", err)
		}
	}
	s.Events.Emit(ctx, events.Event{Type: events.OrgDeleted, OrgID: orgID})
	s.Log.Info("org purged", "org", orgID, "repos", len(mirrors), "uploads", len(uploads))
	return nil
}

// PurgeDue purges orgs deleted more than Grace ago.
func (s *Service) PurgeDue(ctx context.Context) (int, error) {
	rows, err := store.Query(ctx, s.DB, `SELECT id FROM orgs WHERE status = ? AND deleted_at <= ?`, orgs.Deleting, store.Millis(time.Now().Add(-s.grace())))
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	var errs []error
	n := 0
	for _, id := range ids {
		if err := s.Purge(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("org %s: %w", id, err))
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

// ErasedName is what an erased person's name becomes.
const ErasedName = "Deleted user"

// EraseUser removes a person's personal data (GDPR erasure): sign-ins,
// memberships, preferences and linked identities go; the account row stays,
// anonymized, so what they wrote keeps an author ("Deleted user") and
// history stays consistent. Git history on the forge isn't touched. Orgs
// where they were alone are requested for deletion; an org where they're
// the only owner but not alone must get another owner first.
func (s *Service) EraseUser(ctx context.Context, userID string, a audit.Entry) error {
	u, err := users.ByID(ctx, s.DB, userID)
	if err != nil {
		return err
	}
	memberships, err := orgs.ForUser(ctx, s.DB, u)
	if err != nil {
		return err
	}
	var alone []string
	for _, o := range memberships {
		role, err := orgs.MemberRole(ctx, s.DB, o.ID, u)
		if err != nil {
			return err
		}
		if role != orgs.Owner {
			continue
		}
		owners, err := orgs.CountOwners(ctx, s.DB, o.ID)
		if err != nil {
			return err
		}
		seats, err := orgs.Seats(ctx, s.DB, o.ID)
		if err != nil {
			return err
		}
		switch {
		case seats <= 1:
			alone = append(alone, o.ID)
		case owners <= 1:
			return &SoleOwnerError{Org: o.Slug}
		}
	}
	for _, id := range alone {
		if err := s.RequestDeletion(ctx, id, audit.Entry{ActorType: a.ActorType, ActorID: a.ActorID, Data: map[string]any{"why": "the only member's account was erased"}}); err != nil && !errors.Is(err, ErrDefaultOrg) {
			return err
		}
	}
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		for _, q := range []string{
			`DELETE FROM sessions WHERE user_id = ?`,
			`DELETE FROM passkeys WHERE user_id = ?`,
			`DELETE FROM linked_accounts WHERE user_id = ?`,
			`DELETE FROM identity_links WHERE user_id = ?`,
			`DELETE FROM push_subscriptions WHERE user_id = ?`,
			`DELETE FROM notification_prefs WHERE user_id = ?`,
			`DELETE FROM notifications WHERE user_id = ?`,
			`DELETE FROM follows WHERE user_id = ?`,
			`DELETE FROM page_reads WHERE user_id = ?`,
			`DELETE FROM org_members WHERE user_id = ?`,
			`DELETE FROM group_members WHERE user_id = ?`,
			`DELETE FROM repo_members WHERE principal_type = 'user' AND principal_id = ?`,
		} {
			if _, err := store.Exec(ctx, tx, q, userID); err != nil {
				return fmt.Errorf("%s: %w", strings.Fields(q)[2], err)
			}
		}
		if _, err := store.Exec(ctx, tx, `DELETE FROM magic_links WHERE email = ?`, u.Email); err != nil {
			return err
		}
		// A unique, undeliverable address keeps the email index happy and
		// the old one free for a new account.
		if _, err := store.Exec(ctx, tx, `UPDATE users SET email = ?, name = ?, avatar_upload_id = NULL, commit_email_custom = NULL, status = ?, is_instance_admin = ? WHERE id = ?`,
			"erased+"+userID+"@invalid", ErasedName, users.Deactivated, false, userID); err != nil {
			return err
		}
		a.Action, a.TargetType, a.TargetID = "user.erased", "user", userID
		return audit.Write(ctx, tx, a)
	})
	if err == nil {
		s.Log.Info("account erased", "user", userID)
	}
	return err
}
