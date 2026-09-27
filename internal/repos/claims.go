package repos

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/kmdn-app/kmdn/internal/forge"
	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/store"
)

// ErrClaimed means another org already connected this GitHub installation.
var ErrClaimed = errors.New("repos: installation claimed by another organization")

// InstallOrg returns the org that claimed an installation, or "".
func InstallOrg(ctx context.Context, q store.Querier, installID string) (string, error) {
	var org string
	err := store.QueryRow(ctx, q, `SELECT org_id FROM install_claims WHERE install_id = ?`, installID).Scan(&org)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return org, err
}

// ClaimInstall gives an installation to an org (16-organizations.md#forges).
// Claiming again for the same org is a no-op; another org's claim is
// ErrClaimed.
func ClaimInstall(ctx context.Context, q store.Querier, installID, orgID, by string) error {
	var byv any
	if by != "" {
		byv = by
	}
	if _, err := store.Exec(ctx, q, `INSERT INTO install_claims (install_id, org_id, claimed_by, claimed_at) VALUES (?, ?, ?, ?) ON CONFLICT (install_id) DO NOTHING`,
		installID, orgID, byv, store.Millis(time.Now())); err != nil {
		return err
	}
	owner, err := InstallOrg(ctx, q, installID)
	if err != nil {
		return err
	}
	if owner != orgID {
		return ErrClaimed
	}
	return nil
}

// ReleaseInstall removes an installation's claim (the App was uninstalled).
func ReleaseInstall(ctx context.Context, q store.Querier, hostID, externalID string) error {
	_, err := store.Exec(ctx, q, `DELETE FROM install_claims WHERE install_id IN (SELECT id FROM forge_installs WHERE forge_host_id = ? AND external_id = ?)`, hostID, externalID)
	return err
}

// InstallFor decides whether org may use the installation (by internal id):
// its own claim, or, in single mode, the default org claiming it now.
func InstallFor(ctx context.Context, q store.Querier, installID, orgID, by string) error {
	owner, err := InstallOrg(ctx, q, installID)
	if err != nil {
		return err
	}
	switch owner {
	case orgID:
		return nil
	case "":
		mode, err := orgs.Mode(ctx, q)
		if err != nil {
			return err
		}
		if mode == orgs.Single && orgID == orgs.DefaultID {
			return ClaimInstall(ctx, q, installID, orgID, by)
		}
		return ErrUnclaimed
	}
	return ErrClaimed
}

// ErrUnclaimed means no org connected the installation yet.
var ErrUnclaimed = errors.New("repos: installation not connected to the organization")

// OrgInstalls lists the installations of a host that org claimed.
func OrgInstalls(ctx context.Context, q store.Querier, hostID, orgID string) ([]forge.Install, error) {
	rows, err := store.Query(ctx, q, `SELECT i.external_id, i.account_login FROM forge_installs i JOIN install_claims c ON c.install_id = i.id
		WHERE i.forge_host_id = ? AND c.org_id = ? ORDER BY i.account_login`, hostID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []forge.Install{}
	for rows.Next() {
		var in forge.Install
		if err := rows.Scan(&in.ExternalID, &in.AccountLogin); err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// installID returns the internal id of a host's installation.
func installID(ctx context.Context, q store.Querier, hostID, externalID string) (string, error) {
	var id string
	err := store.QueryRow(ctx, q, `SELECT id FROM forge_installs WHERE forge_host_id = ? AND external_id = ?`, hostID, externalID).Scan(&id)
	return id, store.NotFound(err)
}
