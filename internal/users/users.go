// Package users is the repository for kmdn accounts.
package users

import (
	"context"
	"database/sql"
	"net/mail"
	"strings"
	"time"

	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/store"
)

// Statuses.
const (
	Active      = "active"
	Deactivated = "deactivated"
)

// User is a kmdn account.
type User struct {
	ID     string `json:"id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
	Locale string `json:"locale"`
	Theme  string `json:"theme"`
	// Palette is the color theme; UIScale the interface size in percent.
	Palette         string     `json:"palette"`
	UIScale         int        `json:"ui_scale"`
	CommitEmailMode string     `json:"commit_email_mode"`
	IsInstanceAdmin bool       `json:"is_instance_admin"`
	Status          string     `json:"status"`
	CreatedAt       time.Time  `json:"created_at"`
	LastActiveAt    *time.Time `json:"last_active_at,omitempty"`
}

// NormalizeEmail lower-cases and validates an address. It returns "" when invalid.
func NormalizeEmail(s string) string {
	s = strings.TrimSpace(s)
	a, err := mail.ParseAddress(s)
	if err != nil || a.Name != "" || !strings.Contains(a.Address, "@") || strings.ContainsAny(a.Address, " <>") {
		return ""
	}
	return strings.ToLower(a.Address)
}

const cols = `id, email, name, locale, theme, commit_email_mode, is_instance_admin, status, created_at, last_active_at, palette, ui_scale`

func scan(r interface{ Scan(...any) error }) (User, error) {
	var u User
	var created int64
	var last sql.NullInt64
	err := r.Scan(&u.ID, &u.Email, &u.Name, &u.Locale, &u.Theme, &u.CommitEmailMode, &u.IsInstanceAdmin, &u.Status, &created, &last, &u.Palette, &u.UIScale)
	u.CreatedAt = store.FromMillis(created)
	u.LastActiveAt = store.NullMillis(last)
	return u, err
}

// Create inserts a user. Email must already be normalized.
func Create(ctx context.Context, q store.Querier, email, name string, admin bool) (User, error) {
	u := User{ID: ids.New(ids.User), Email: email, Name: name, Locale: "en", Theme: "system", Palette: DefaultPalette, UIScale: DefaultUIScale, CommitEmailMode: "forge_noreply",
		IsInstanceAdmin: admin, Status: Active, CreatedAt: time.Now().UTC().Truncate(time.Millisecond)}
	_, err := store.Exec(ctx, q, `INSERT INTO users (id, email, name, is_instance_admin, status, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		u.ID, u.Email, u.Name, u.IsInstanceAdmin, u.Status, store.Millis(u.CreatedAt))
	return u, err
}

// ByID loads a user.
func ByID(ctx context.Context, q store.Querier, id string) (User, error) {
	u, err := scan(store.QueryRow(ctx, q, `SELECT `+cols+` FROM users WHERE id = ?`, id))
	return u, store.NotFound(err)
}

// ByEmail loads a user by normalized email.
func ByEmail(ctx context.Context, q store.Querier, email string) (User, error) {
	u, err := scan(store.QueryRow(ctx, q, `SELECT `+cols+` FROM users WHERE email = ?`, email))
	return u, store.NotFound(err)
}

// Count returns the number of users.
func Count(ctx context.Context, q store.Querier) (int, error) {
	var n int
	err := store.QueryRow(ctx, q, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CountAdmins returns the number of active instance admins.
func CountAdmins(ctx context.Context, q store.Querier) (int, error) {
	var n int
	err := store.QueryRow(ctx, q, `SELECT COUNT(*) FROM users WHERE is_instance_admin = ? AND status = ?`, true, Active).Scan(&n)
	return n, err
}

// UpdateProfile changes the editable profile fields.
// Appearance choices (docs/specs/02-ux.md#visual-language).
const (
	DefaultPalette = "default"
	DefaultUIScale = 120
)

// Palettes and UIScales are the accepted values.
var (
	Palettes = []string{"default", "catppuccin", "gruvbox", "base16", "nord"}
	UIScales = []int{100, 110, 120, 135}
)

// UpdateAppearance stores a user's palette and interface size.
func UpdateAppearance(ctx context.Context, q store.Querier, id, palette string, scale int) error {
	_, err := store.Exec(ctx, q, `UPDATE users SET palette = ?, ui_scale = ? WHERE id = ?`, palette, scale, id)
	return err
}

func UpdateProfile(ctx context.Context, q store.Querier, id, name, theme string) error {
	_, err := store.Exec(ctx, q, `UPDATE users SET name = ?, theme = ? WHERE id = ?`, name, theme, id)
	return err
}

// Touch records activity (at most once a minute is enough; callers debounce).
func Touch(ctx context.Context, q store.Querier, id string, at time.Time) error {
	_, err := store.Exec(ctx, q, `UPDATE users SET last_active_at = ? WHERE id = ?`, store.Millis(at), id)
	return err
}
