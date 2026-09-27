// Package kmdn runs kmdn inside another Go program: a hosted service adds
// plans, billing or its own sign-up pages around it without patching kmdn
// (docs/specs/16-organizations.md#embedding). Everything else in this
// module is internal; this package is the stable surface.
package kmdn

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/app"
	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/config"
	"github.com/kmdn-app/kmdn/internal/events"
	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/policy"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// Config is kmdn's configuration (docs/specs/13-operations.md#configuration).
type Config = config.Config

// LoadConfig reads a config file (empty: kmdn.yaml if present) and the
// KMDN_* environment, and validates the result.
func LoadConfig(path string) (Config, error) {
	cfg, err := config.Load(path, os.LookupEnv)
	if err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
}

// Types an embedding program uses.
type (
	// Policy decides what orgs may do; Limits caps one org.
	Policy = policy.Policy
	Limits = policy.Limits
	// Org is an organization; OrgSettings its own settings.
	Org         = orgs.Org
	OrgSettings = orgs.Settings
	// Event is something that happened in an org.
	Event = events.Event
	// User is an account.
	User = users.User
)

// Event types.
const (
	EventOrgCreated       = events.OrgCreated
	EventOrgStatusChanged = events.OrgStatusChanged
	EventMemberAdded      = events.MemberAdded
	EventMemberChanged    = events.MemberChanged
	EventMemberRemoved    = events.MemberRemoved
	EventRepoConnected    = events.RepoConnected
	EventRepoRemoved      = events.RepoRemoved
	EventAIUsage          = events.AIUsage
)

// Org roles.
const (
	RoleMember = orgs.Member
	RoleAdmin  = orgs.Admin
	RoleOwner  = orgs.Owner
)

// Option configures New.
type Option func(*app.Options)

// WithPolicy replaces the policy built from the config (strict mode, limits).
func WithPolicy(p *Policy) Option { return func(o *app.Options) { o.Policy = p } }

// WithManagedSettings fixes org settings per org, by JSON field name
// ("assistant", "monthly_tokens"); they show read-only in the console.
func WithManagedSettings(f func(ctx context.Context, org Org) map[string]any) Option {
	return func(o *app.Options) { o.Managed = f }
}

// WithEvents receives what happens in orgs, after it committed. The handler
// must not block.
func WithEvents(f func(ctx context.Context, e Event)) Option {
	return func(o *app.Options) { o.Events = f }
}

// WithRoutes adds routes to the root router (e.g. a billing webhook).
func WithRoutes(f func(chi.Router)) Option { return func(o *app.Options) { o.Routes = f } }

// WithAPIRoutes adds routes under /api/v1; CurrentUser works there.
func WithAPIRoutes(f func(chi.Router)) Option { return func(o *app.Options) { o.APIRoutes = f } }

// WithOrgRoutes adds routes under /api/v1/orgs/{org}, for the org's members
// (others get 404); CurrentOrg works there.
func WithOrgRoutes(f func(chi.Router)) Option { return func(o *app.Options) { o.OrgRoutes = f } }

// WithMigrations runs the program's own NNNN_name.sql migrations from the
// root of fsys after kmdn's, recorded in table (e.g. "saas_migrations").
func WithMigrations(fsys fs.FS, table string) Option {
	return func(o *app.Options) { o.Migrations, o.MigrationsTable = fsys, table }
}

// App is a running kmdn.
type App struct {
	a *app.App
}

// New opens the database, applies migrations and builds kmdn.
func New(ctx context.Context, cfg Config, log *slog.Logger, opts ...Option) (*App, error) {
	var o app.Options
	for _, opt := range opts {
		opt(&o)
	}
	if log == nil {
		log = slog.Default()
	}
	a, err := app.NewWith(ctx, cfg, log, o)
	if err != nil {
		return nil, err
	}
	return &App{a: a}, nil
}

// Handler serves kmdn (the app, the API, WebSockets, MCP, webhooks).
func (k *App) Handler() http.Handler { return k.a.Server.Handler() }

// Run serves on config server.listen and runs background work until ctx
// is cancelled.
func (k *App) Run(ctx context.Context) error { return k.a.Run(ctx) }

// Close releases the database.
func (k *App) Close() error { return k.a.Close() }

// DB is kmdn's database, for the program's own tables.
func (k *App) DB() *sql.DB { return k.a.DB.DB }

// Postgres reports whether the database is Postgres (else SQLite).
func (k *App) Postgres() bool { return k.a.DB.Dialect == store.Postgres }

// CurrentUser is the signed-in person of a request under WithAPIRoutes or
// WithOrgRoutes.
func CurrentUser(r *http.Request) (User, bool) {
	p, ok := auth.FromContext(r.Context())
	return p.User, ok
}

// CurrentOrg is the org of a request under WithOrgRoutes, with the
// caller's role in it ("" for an instance admin outside it).
func CurrentOrg(r *http.Request) (Org, string, bool) {
	c, ok := orgs.FromContext(r.Context())
	return c.Org, c.Role, ok
}

// NewOrg is what creating an org needs. The owner's account is created when
// no account has that email.
type NewOrg struct {
	Slug       string // empty: made from Name
	Name       string
	OwnerEmail string
	OwnerName  string
}

// ErrSlugTaken means another org has the slug.
var ErrSlugTaken = orgs.ErrSlugTaken

// CreateOrg creates an org with its owner (multi mode).
func (k *App) CreateOrg(ctx context.Context, in NewOrg) (Org, error) {
	email := users.NormalizeEmail(in.OwnerEmail)
	if email == "" {
		return Org{}, errors.New("kmdn: owner email is not valid")
	}
	slug := in.Slug
	if slug == "" {
		slug = orgs.Slugify(in.Name)
	}
	var o Org
	var owner User
	err := k.a.DB.InTx(ctx, func(tx *store.Tx) error {
		u, err := users.ByEmail(ctx, tx, email)
		if errors.Is(err, store.ErrNotFound) {
			name := strings.TrimSpace(in.OwnerName)
			if name == "" {
				name = email[:strings.IndexByte(email, '@')]
			}
			u, err = users.Create(ctx, tx, email, name, false)
		}
		if err != nil {
			return err
		}
		owner = u
		if o, err = orgs.Create(ctx, tx, slug, in.Name, u.ID); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorSystem, OrgID: o.ID, Action: "org.created", TargetType: "org", TargetID: o.ID, Data: map[string]any{"slug": o.Slug, "owner": u.ID}})
	})
	if err != nil {
		return Org{}, err
	}
	k.emit(ctx, Event{Type: EventOrgCreated, OrgID: o.ID, UserID: owner.ID, Data: map[string]any{"slug": o.Slug}})
	k.emit(ctx, Event{Type: EventMemberAdded, OrgID: o.ID, UserID: owner.ID, Data: map[string]any{"role": RoleOwner}})
	return o, nil
}

// OrgBySlug loads an org.
func (k *App) OrgBySlug(ctx context.Context, slug string) (Org, error) {
	return orgs.BySlug(ctx, k.a.DB, slug)
}

// OrgByID loads an org.
func (k *App) OrgByID(ctx context.Context, id string) (Org, error) {
	return orgs.ByID(ctx, k.a.DB, id)
}

// SuspendOrg makes an org read-only (people read and comment, nobody edits
// or publishes); members see reason.
func (k *App) SuspendOrg(ctx context.Context, orgID, reason string) error {
	return k.setStatus(ctx, orgID, orgs.Suspended, reason)
}

// ResumeOrg lifts a suspension.
func (k *App) ResumeOrg(ctx context.Context, orgID string) error {
	return k.setStatus(ctx, orgID, orgs.Active, "")
}

func (k *App) setStatus(ctx context.Context, orgID, status, reason string) error {
	err := k.a.DB.InTx(ctx, func(tx *store.Tx) error {
		if err := orgs.SetStatus(ctx, tx, orgID, status, reason); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorSystem, OrgID: orgID, Action: "org.status_changed", TargetType: "org", TargetID: orgID, Data: map[string]any{"status": status, "reason": reason}})
	})
	if err == nil {
		k.emit(ctx, Event{Type: EventOrgStatusChanged, OrgID: orgID, Data: map[string]any{"status": status, "reason": reason}})
	}
	return err
}

// Seats counts an org's active members and pending invitations.
func (k *App) Seats(ctx context.Context, orgID string) (int, error) {
	return orgs.Seats(ctx, k.a.DB, orgID)
}

// OrgSettings returns an org's effective settings.
func (k *App) OrgSettings(ctx context.Context, orgID string) (OrgSettings, error) {
	st, _, err := k.a.OrgSettings.Get(ctx, orgID)
	return st, err
}

func (k *App) emit(ctx context.Context, e Event) { k.a.Events.Emit(ctx, e) }

// DefaultConfig is the configuration used when nothing is set.
func DefaultConfig() Config { return config.Defaults() }

// SignIn starts a session for the active account with this email in the
// response, for a program's own sign-up or sign-in flow once it has
// verified the address itself.
func (k *App) SignIn(w http.ResponseWriter, r *http.Request, email string) error {
	u, err := users.ByEmail(r.Context(), k.a.DB, users.NormalizeEmail(email))
	if err != nil {
		return err
	}
	if u.Status != users.Active {
		return errors.New("kmdn: the account is deactivated")
	}
	return k.a.AuthH.SignIn(w, r, u, "embedded")
}
