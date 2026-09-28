package repos

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/kmdn-app/kmdn/internal/forge"
	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/secrets"
	"github.com/kmdn-app/kmdn/internal/store"
)

// HostRecord is a stored forge host with its secret references.
type HostRecord struct {
	forge.Host
	// OrgID is the org that added the host; empty for hosts the instance
	// shares with every org (16-organizations.md#forges).
	OrgID            string    `json:"org_id,omitempty"`
	ClientSecretRef  string    `json:"-"`
	PrivateKeyRef    string    `json:"-"`
	WebhookSecretRef string    `json:"-"`
	CreatedAt        time.Time `json:"created_at"`
	Repos            int       `json:"repos"`
}

const hostCols = `id, kind, base_url, api_url, display_name, app_id, app_slug, client_id, COALESCE(client_secret_ref, ''), COALESCE(private_key_ref, ''), COALESCE(webhook_secret_ref, ''), created_at,
	(SELECT COUNT(*) FROM repos r WHERE r.forge_host_id = forge_hosts.id), COALESCE(org_id, '')`

func scanHost(row interface{ Scan(...any) error }) (HostRecord, error) {
	var h HostRecord
	var at int64
	err := row.Scan(&h.ID, &h.Kind, &h.BaseURL, &h.APIURL, &h.DisplayName, &h.AppID, &h.AppSlug, &h.ClientID, &h.ClientSecretRef, &h.PrivateKeyRef, &h.WebhookSecretRef, &at, &h.Repos, &h.OrgID)
	h.CreatedAt = store.FromMillis(at)
	return h, err
}

// GetHost loads a forge host.
func GetHost(ctx context.Context, q store.Querier, id string) (HostRecord, error) {
	h, err := scanHost(store.QueryRow(ctx, q, `SELECT `+hostCols+` FROM forge_hosts WHERE id = ?`, id))
	return h, store.NotFound(err)
}

// ListHosts returns all forge hosts.
func ListHosts(ctx context.Context, q store.Querier) ([]HostRecord, error) {
	return listHosts(ctx, q, `SELECT `+hostCols+` FROM forge_hosts ORDER BY created_at`)
}

// ListHostsFor returns the hosts an org can use: the shared ones and its own.
func ListHostsFor(ctx context.Context, q store.Querier, orgID string) ([]HostRecord, error) {
	return listHosts(ctx, q, `SELECT `+hostCols+` FROM forge_hosts WHERE org_id IS NULL OR org_id = ? ORDER BY created_at`, orgID)
}

// UsableBy reports whether an org can connect repositories through h.
func (h HostRecord) UsableBy(orgID string) bool { return h.OrgID == "" || h.OrgID == orgID }

func listHosts(ctx context.Context, q store.Querier, query string, args ...any) ([]HostRecord, error) {
	rows, err := store.Query(ctx, q, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HostRecord{}
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// HostInput creates a host.
type HostInput struct {
	ID            string // optional, generated when empty
	OrgID         string // empty: shared by the instance
	Kind          string
	BaseURL       string
	APIURL        string
	DisplayName   string
	AppID         string
	AppSlug       string
	ClientID      string
	ClientSecret  string
	PrivateKeyPEM string
	WebhookSecret string
}

// CreateHost stores a host and its secrets.
func CreateHost(ctx context.Context, tx *store.Tx, sec *secrets.Store, in HostInput) (HostRecord, error) {
	if in.ID == "" {
		in.ID = ids.New("fh")
	}
	put := func(kind, v string) (any, error) {
		if v == "" {
			return nil, nil
		}
		return sec.PutOrg(ctx, tx, in.OrgID, kind, []byte(v))
	}
	cs, err := put("forge_client_secret", in.ClientSecret)
	if err != nil {
		return HostRecord{}, err
	}
	pk, err := put("github_app_private_key", in.PrivateKeyPEM)
	if err != nil {
		return HostRecord{}, err
	}
	ws, err := put("forge_webhook_secret", in.WebhookSecret)
	if err != nil {
		return HostRecord{}, err
	}
	var org any
	if in.OrgID != "" {
		org = in.OrgID
	}
	if _, err := store.Exec(ctx, tx, `INSERT INTO forge_hosts (id, kind, base_url, api_url, display_name, app_id, app_slug, client_id, client_secret_ref, private_key_ref, webhook_secret_ref, created_at, org_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, in.ID, in.Kind, in.BaseURL, in.APIURL, in.DisplayName, in.AppID, in.AppSlug, in.ClientID, cs, pk, ws, store.Millis(time.Now()), org); err != nil {
		return HostRecord{}, err
	}
	return GetHost(ctx, tx, in.ID)
}

// EnsureGitHost returns the built-in host for plain git remotes, creating it once.
func EnsureGitHost(ctx context.Context, db *store.DB) (HostRecord, error) {
	h, err := scanHost(store.QueryRow(ctx, db, `SELECT `+hostCols+` FROM forge_hosts WHERE kind = 'git' AND org_id IS NULL ORDER BY created_at LIMIT 1`))
	if err == nil {
		return h, nil
	}
	if !errors.Is(store.NotFound(err), store.ErrNotFound) {
		return h, err
	}
	var out HostRecord
	err = db.InTx(ctx, func(tx *store.Tx) error {
		var err error
		out, err = CreateHost(ctx, tx, nil, HostInput{Kind: forge.KindGit, DisplayName: "Git remote"})
		return err
	})
	return out, err
}

// Adapters builds forge adapters, caching GitHub Apps (they cache tokens).
type Adapters struct {
	DB      *store.DB
	Secrets *secrets.Store
	HTTP    *http.Client
	// OrgHTTP, when set, is used for hosts an org added (their URLs come
	// from org admins): in strict mode it only reaches public addresses.
	OrgHTTP *http.Client

	mu   sync.Mutex
	apps map[string]*forge.GitHubApp
}

// For is the HTTP client for requests to a host.
func (a *Adapters) For(h HostRecord) *http.Client {
	if h.OrgID != "" && a.OrgHTTP != nil {
		return a.OrgHTTP
	}
	return a.HTTP
}

func (a *Adapters) secret(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	b, err := a.Secrets.Get(ctx, a.DB, ref)
	return string(b), err
}

// ForRepo returns the adapter to use for repo.
func (a *Adapters) ForRepo(ctx context.Context, r Repo) (forge.Adapter, error) {
	h, err := GetHost(ctx, a.DB, r.ForgeHostID)
	if err != nil {
		return nil, err
	}
	switch h.Kind {
	case forge.KindGitHub:
		return a.GitHubApp(ctx, h)
	case forge.KindGitLab:
		tok, err := a.secret(ctx, r.TokenRef)
		if err != nil {
			return nil, err
		}
		return &forge.GitLab{BaseURL: h.BaseURL, Token: tok, HTTP: a.For(h)}, nil
	case forge.KindGit:
		tok, err := a.secret(ctx, r.TokenRef)
		if err != nil {
			return nil, err
		}
		return &forge.PlainGit{Token: tok}, nil
	}
	return nil, fmt.Errorf("repos: unknown forge kind %q", h.Kind)
}

// GitHubApp returns the (cached) App client for host.
func (a *Adapters) GitHubApp(ctx context.Context, h HostRecord) (*forge.GitHubApp, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if app, ok := a.apps[h.ID]; ok {
		return app, nil
	}
	pemStr, err := a.secret(ctx, h.PrivateKeyRef)
	if err != nil {
		return nil, err
	}
	key, err := forge.ParsePrivateKey([]byte(pemStr))
	if err != nil {
		return nil, err
	}
	app := &forge.GitHubApp{APIURL: h.APIURL, AppID: h.AppID, PrivateKey: key, HTTP: a.For(h)}
	if a.apps == nil {
		a.apps = map[string]*forge.GitHubApp{}
	}
	a.apps[h.ID] = app
	return app, nil
}

// Forget drops a cached App (after credentials change).
func (a *Adapters) Forget(hostID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.apps, hostID)
}
