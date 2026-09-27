// Package repos connects repositories, keeps their mirrors in sync and serves
// their published content. See docs/specs/06-git-and-forges.md.
package repos

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/kmdn-app/kmdn/internal/forge"
	"github.com/kmdn-app/kmdn/internal/store"
)

// Health states.
const (
	HealthPending      = "pending"
	HealthOK           = "ok"
	HealthDegraded     = "degraded"
	HealthDisconnected = "disconnected"
)

// Settings are per-repo options edited in repository settings.
type Settings struct {
	AllowSelfApproval bool          `json:"allow_self_approval"`
	AssistantEnabled  *bool         `json:"assistant_enabled,omitempty"`
	Assets            *AssetsConfig `json:"assets,omitempty"`
}

// Repo is a connected repository.
type Repo struct {
	ID            string           `json:"id"`
	OrgID         string           `json:"org_id"`
	ForgeHostID   string           `json:"forge_host_id"`
	ForgeKind     string           `json:"forge_kind"`
	InstallID     string           `json:"-"`
	InstallExtID  string           `json:"-"`
	ExternalID    string           `json:"external_id"`
	Owner         string           `json:"owner"`
	Name          string           `json:"name"`
	Slug          string           `json:"slug"` // owner/name
	DisplayName   string           `json:"display_name"`
	CloneURL      string           `json:"clone_url,omitempty"`
	WebURL        string           `json:"web_url"`
	DefaultBranch string           `json:"default_branch"`
	TargetBranch  string           `json:"target_branch"`
	ContentRoot   string           `json:"content_root"`
	Include       []string         `json:"include"`
	Exclude       []string         `json:"exclude"`
	Settings      Settings         `json:"settings"`
	Protection    forge.Protection `json:"protection"`
	HeadSHA       string           `json:"head_sha"`
	Health        string           `json:"health"`
	HealthDetail  string           `json:"health_detail"`
	KmdnYML       KmdnYML          `json:"kmdn_yml"`
	LastFetchAt   *time.Time       `json:"last_fetch_at,omitempty"`
	CreatedAt     time.Time        `json:"created_at"`

	TokenRef         string `json:"-"`
	WebhookSecretRef string `json:"-"`
}

// Scope returns the effective content scope.
func (r Repo) Scope() Scope { return EffectiveScope(r.ContentRoot, r.Include, r.Exclude, r.KmdnYML) }

// ForgeRepo converts to the forge adapter's identifier.
func (r Repo) ForgeRepo() forge.Repo {
	return forge.Repo{Owner: r.Owner, Name: r.Name, ExternalID: r.ExternalID, InstallID: r.InstallExtID, CloneURL: r.CloneURL}
}

const repoCols = `r.id, r.forge_host_id, h.kind, COALESCE(r.install_id, ''), COALESCE(i.external_id, ''), r.external_id, r.owner, r.name, r.display_name,
	r.clone_url, r.web_url, r.default_branch, r.target_branch, r.content_root, r.include_globs, r.exclude_globs, r.settings,
	r.protection, r.head_sha, r.health, r.health_detail, r.kmdn_yml, r.last_fetch_at, r.created_at,
	COALESCE(r.token_ref, ''), COALESCE(r.webhook_secret_ref, ''), r.org_id`

const repoFrom = ` FROM repos r JOIN forge_hosts h ON h.id = r.forge_host_id LEFT JOIN forge_installs i ON i.id = r.install_id`

func scanRepo(row interface{ Scan(...any) error }) (Repo, error) {
	var r Repo
	var inc, exc, settings, prot, kyml string
	var last sql.NullInt64
	var created int64
	err := row.Scan(&r.ID, &r.ForgeHostID, &r.ForgeKind, &r.InstallID, &r.InstallExtID, &r.ExternalID, &r.Owner, &r.Name, &r.DisplayName,
		&r.CloneURL, &r.WebURL, &r.DefaultBranch, &r.TargetBranch, &r.ContentRoot, &inc, &exc, &settings,
		&prot, &r.HeadSHA, &r.Health, &r.HealthDetail, &kyml, &last, &created, &r.TokenRef, &r.WebhookSecretRef, &r.OrgID)
	if err != nil {
		return r, err
	}
	_ = json.Unmarshal([]byte(inc), &r.Include)
	_ = json.Unmarshal([]byte(exc), &r.Exclude)
	_ = json.Unmarshal([]byte(settings), &r.Settings)
	_ = json.Unmarshal([]byte(prot), &r.Protection)
	_ = json.Unmarshal([]byte(kyml), &r.KmdnYML)
	if r.Include == nil {
		r.Include = []string{}
	}
	if r.Exclude == nil {
		r.Exclude = []string{}
	}
	r.LastFetchAt = store.NullMillis(last)
	r.CreatedAt = store.FromMillis(created)
	r.Slug = r.Owner + "/" + r.Name
	return r, nil
}

// Get loads a repo by id.
func Get(ctx context.Context, q store.Querier, id string) (Repo, error) {
	r, err := scanRepo(store.QueryRow(ctx, q, `SELECT `+repoCols+repoFrom+` WHERE r.id = ?`, id))
	return r, store.NotFound(err)
}

// BySlug loads an org's repo by owner/name (first match across hosts).
func BySlug(ctx context.Context, q store.Querier, orgID, owner, name string) (Repo, error) {
	r, err := scanRepo(store.QueryRow(ctx, q, `SELECT `+repoCols+repoFrom+` WHERE r.org_id = ? AND r.owner = ? AND r.name = ? ORDER BY r.created_at LIMIT 1`, orgID, owner, name))
	return r, store.NotFound(err)
}

// ByExternal finds a repo by forge host and external id.
func ByExternal(ctx context.Context, q store.Querier, hostID, externalID string) (Repo, error) {
	r, err := scanRepo(store.QueryRow(ctx, q, `SELECT `+repoCols+repoFrom+` WHERE r.forge_host_id = ? AND r.external_id = ?`, hostID, externalID))
	return r, store.NotFound(err)
}

// List returns repos by id (all when ids is nil and all is true).
func List(ctx context.Context, q store.Querier, ids []string, all bool) ([]Repo, error) {
	query := `SELECT ` + repoCols + repoFrom
	var args []any
	if !all {
		if len(ids) == 0 {
			return []Repo{}, nil
		}
		query += ` WHERE r.id IN (` + placeholders(len(ids)) + `)`
		for _, id := range ids {
			args = append(args, id)
		}
	}
	rows, err := store.Query(ctx, q, query+` ORDER BY r.display_name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Repo{}
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	b := make([]byte, 0, n*2)
	for i := 0; i < n; i++ {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '?')
	}
	return string(b)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
