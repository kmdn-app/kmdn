// Package settings stores instance-wide settings as JSON values in
// instance_settings (SMTP, auto-join domains, setup state…).
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

// IsNotFound reports whether err means the setting is unset.
func IsNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }
