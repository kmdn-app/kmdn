// Package settings stores settings as JSON values: instance-wide ones in
// instance_settings (SMTP, AI provider, setup state…) and each org's own in
// org_settings (docs/specs/16-organizations.md#settings).
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/kmdn-app/kmdn/internal/store"
)

// Get loads key into v. It returns store.ErrNotFound when unset.
func Get(ctx context.Context, q store.Querier, key string, v any) error {
	var raw string
	if err := store.QueryRow(ctx, q, `SELECT value FROM instance_settings WHERE key = ?`, key).Scan(&raw); err != nil {
		return store.NotFound(err)
	}
	return json.Unmarshal([]byte(raw), v)
}

// Set stores v under key.
func Set(ctx context.Context, q store.Querier, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = store.Exec(ctx, q, `INSERT INTO instance_settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, string(b), store.Millis(time.Now()))
	return err
}

// Delete removes key.
func Delete(ctx context.Context, q store.Querier, key string) error {
	_, err := store.Exec(ctx, q, `DELETE FROM instance_settings WHERE key = ?`, key)
	return err
}

// GetOrg loads an org's key into v. It returns store.ErrNotFound when unset.
func GetOrg(ctx context.Context, q store.Querier, orgID, key string, v any) error {
	var raw string
	if err := store.QueryRow(ctx, q, `SELECT value FROM org_settings WHERE org_id = ? AND key = ?`, orgID, key).Scan(&raw); err != nil {
		return store.NotFound(err)
	}
	return json.Unmarshal([]byte(raw), v)
}

// SetOrg stores v under an org's key.
func SetOrg(ctx context.Context, q store.Querier, orgID, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = store.Exec(ctx, q, `INSERT INTO org_settings (org_id, key, value, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (org_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, orgID, key, string(b), store.Millis(time.Now()))
	return err
}

// IsNotFound reports whether err means the setting is unset.
func IsNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }
