package orgs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/kmdn-app/kmdn/internal/settings"
	"github.com/kmdn-app/kmdn/internal/store"
)

// Settings are an org's own settings (16-organizations.md#settings).
type Settings struct {
	// Assistant turns every AI feature on or off for the org's repositories
	// (the instance must have a provider too).
	Assistant bool `json:"assistant"`
}

// DefaultSettings apply to an org that has saved none.
func DefaultSettings() Settings { return Settings{Assistant: true} }

const settingsKey = "settings"

// Managed returns the settings the deployment fixes for an org, as JSON
// field names and values (config, env or an embedding program). Those
// fields override what the org saved and are read-only in the console.
type Managed func(ctx context.Context, org Org) map[string]any

// SettingsStore reads and writes org settings through the managed overlay.
type SettingsStore struct {
	DB      store.Querier
	Managed Managed
}

// ErrLocked is returned when a change touches a managed field.
var ErrLocked = errors.New("orgs: setting managed by the deployment")

// Get returns an org's effective settings and the fields that are locked.
func (s *SettingsStore) Get(ctx context.Context, orgID string) (Settings, []string, error) {
	st := DefaultSettings()
	if err := settings.GetOrg(ctx, s.DB, orgID, settingsKey, &st); err != nil && !settings.IsNotFound(err) {
		return st, nil, err
	}
	if s.Managed == nil {
		return st, []string{}, nil
	}
	o, err := ByID(ctx, s.DB, orgID)
	if err != nil {
		return st, nil, err
	}
	fixed := s.Managed(ctx, o)
	if len(fixed) == 0 {
		return st, []string{}, nil
	}
	b, err := json.Marshal(fixed)
	if err != nil {
		return st, nil, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, nil, err
	}
	locked := make([]string, 0, len(fixed))
	for k := range fixed {
		locked = append(locked, k)
	}
	slices.Sort(locked)
	return st, locked, nil
}

// Update applies the JSON fields in patch to an org's saved settings. It
// fails with ErrLocked when a field is managed.
func (s *SettingsStore) Update(ctx context.Context, q store.Querier, orgID string, patch map[string]json.RawMessage) (Settings, error) {
	_, locked, err := s.Get(ctx, orgID)
	if err != nil {
		return Settings{}, err
	}
	for k := range patch {
		if slices.Contains(locked, k) {
			return Settings{}, ErrLocked
		}
	}
	saved := DefaultSettings()
	if err := settings.GetOrg(ctx, q, orgID, settingsKey, &saved); err != nil && !settings.IsNotFound(err) {
		return Settings{}, err
	}
	merged := map[string]json.RawMessage{}
	b, _ := json.Marshal(saved)
	if err := json.Unmarshal(b, &merged); err != nil {
		return Settings{}, err
	}
	for k, v := range patch {
		merged[k] = v
	}
	b, _ = json.Marshal(merged)
	next := DefaultSettings()
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&next); err != nil {
		return Settings{}, err
	}
	if err := settings.SetOrg(ctx, q, orgID, settingsKey, next); err != nil {
		return Settings{}, err
	}
	st, _, err := s.Get(ctx, orgID)
	return st, err
}
