package repos

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/forge"
	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/orghttp"
	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/settings"
	"github.com/kmdn-app/kmdn/internal/store"
)

// ForgeRoutes registers the instance console's /admin/forges endpoints
// (instance admins): hosts shared by every org, and every installation.
func (s *Service) ForgeRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Get("/admin/forges", s.listHosts)
		r.Post("/admin/forges", s.createHost)
		r.Delete("/admin/forges/{id}", s.deleteHost)
		r.Post("/admin/forges/github/manifest", s.githubManifest)
		r.Get("/admin/forges/{id}/installations", s.installations)
		r.Get("/admin/forges/{id}/repositories", s.availableRepos)
	})
	// GitHub redirects the browser here after creating the App; the admin's
	// session cookie is present (same site, top-level GET). The state says
	// whether it was an instance or an org console.
	r.With(auth.Require).Get("/admin/forges/github/callback", s.githubCallback)
}

// OrgForgeRoutes registers the org console's forges (under /orgs/{org}):
// the shared hosts and the org's own, and the org's installations.
func (s *Service) OrgForgeRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(orghttp.RequireAdmin)
		r.Get("/admin/forges", s.listHosts)
		r.Post("/admin/forges", s.createHost)
		r.Delete("/admin/forges/{id}", s.deleteHost)
		r.Post("/admin/forges/github/manifest", s.githubManifest)
		r.Get("/admin/forges/{id}/installations", s.installations)
		r.Get("/admin/forges/{id}/repositories", s.availableRepos)
	})
}

// orgOf is the org of an org-console request, or "" in the instance console.
func orgOf(r *http.Request) string {
	c, _ := orgs.FromContext(r.Context())
	return c.Org.ID
}

func (s *Service) listHosts(w http.ResponseWriter, r *http.Request) {
	var hs []HostRecord
	var err error
	if org := orgOf(r); org != "" {
		hs, err = ListHostsFor(r.Context(), s.DB, org)
	} else {
		hs, err = ListHosts(r.Context(), s.DB)
	}
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": hs, "gitlab_callback_url": GitLabCallbackURL(s.BaseURL)})
}

// host loads the {id} host of the path: in the org console, one the org can
// use (shared or its own); editable reports whether the caller may change it.
func (s *Service) host(w http.ResponseWriter, r *http.Request) (h HostRecord, editable, ok bool) {
	h, err := GetHost(r.Context(), s.DB, chi.URLParam(r, "id"))
	org := orgOf(r)
	if err != nil || (org != "" && !h.UsableBy(org)) {
		api.Error(w, r, api.ErrNotFound)
		return h, false, false
	}
	return h, org == "" || h.OrgID == org, true
}

// GitLabCallbackURL is the redirect URI to register in a GitLab OAuth
// application (sign-in and account linking), the same for every GitLab host.
func GitLabCallbackURL(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + "/api/v1/auth/oauth/callback"
}

func (s *Service) createHost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind         string `json:"kind"`
		BaseURL      string `json:"base_url"`
		DisplayName  string `json:"display_name"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	switch in.Kind {
	case forge.KindGitLab:
		u, err := url.Parse(strings.TrimRight(in.BaseURL, "/"))
		if err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "" {
			api.Error(w, r, api.Invalid("base_url", "Enter your GitLab URL, e.g. https://gitlab.com."))
			return
		}
		in.BaseURL = u.String()
		if in.DisplayName == "" {
			in.DisplayName = "GitLab · " + u.Host
		}
	case forge.KindGit:
		if orgOf(r) != "" {
			api.Error(w, r, api.Invalid("kind", "Plain git remotes use the instance's built-in forge."))
			return
		}
		if in.DisplayName == "" {
			in.DisplayName = "Git remote"
		}
	case forge.KindGitHub:
		api.Error(w, r, api.Invalid("kind", "Create GitHub Apps with the manifest flow (POST /admin/forges/github/manifest)."))
		return
	default:
		api.Error(w, r, api.Invalid("kind", "Kind must be gitlab or git."))
		return
	}
	ctx := r.Context()
	p, _ := auth.FromContext(ctx)
	var h HostRecord
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		var err error
		h, err = CreateHost(ctx, tx, s.Secrets, HostInput{OrgID: orgOf(r), Kind: in.Kind, BaseURL: in.BaseURL, DisplayName: in.DisplayName, ClientID: in.ClientID, ClientSecret: in.ClientSecret})
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, OrgID: h.OrgID, Action: "forge.created", TargetType: "forge_host", TargetID: h.ID, Data: map[string]any{"kind": in.Kind}})
	})
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusCreated, h)
}

func (s *Service) deleteHost(w http.ResponseWriter, r *http.Request) {
	h, editable, ok := s.host(w, r)
	if !ok {
		return
	}
	if !editable {
		api.Error(w, r, api.Err(http.StatusForbidden, "forge_shared", "This forge is shared by the instance; only instance admins can remove it."))
		return
	}
	if h.Repos > 0 {
		api.Error(w, r, api.Err(http.StatusConflict, "forge_in_use", "Disconnect this forge's repositories first."))
		return
	}
	ctx := r.Context()
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		for _, ref := range []string{h.ClientSecretRef, h.PrivateKeyRef, h.WebhookSecretRef} {
			if ref != "" {
				if err := s.Secrets.Delete(ctx, tx, ref); err != nil {
					return err
				}
			}
		}
		_, err := store.Exec(ctx, tx, `DELETE FROM forge_hosts WHERE id = ?`, h.ID)
		return err
	})
	if err != nil {
		api.Error(w, r, err)
		return
	}
	s.Adapters.Forget(h.ID)
	w.WriteHeader(http.StatusNoContent)
}

type manifestState struct {
	HostID    string    `json:"host_id"`
	OrgID     string    `json:"org_id,omitempty"`
	UserID    string    `json:"user_id"`
	BaseURL   string    `json:"base_url"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Service) githubManifest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		BaseURL      string `json:"base_url"` // GitHub web URL; empty = github.com
		Organization string `json:"organization"`
		Name         string `json:"name"`
	}
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	base := strings.TrimRight(in.BaseURL, "/")
	if base == "" {
		base = "https://github.com"
	}
	if u, err := url.Parse(base); err != nil || u.Scheme != "https" || u.Host == "" {
		api.Error(w, r, api.Invalid("base_url", "Enter the GitHub URL, e.g. https://github.com or https://github.example.com."))
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "kmdn " + auth.InstanceName(r.Context(), s.DB)
	}
	state := auth.Token(18)
	hostID := ids.New("fh")
	p, _ := auth.FromContext(r.Context())
	if err := settings.Set(r.Context(), s.DB, "github_manifest:"+auth.Hash(state), manifestState{HostID: hostID, OrgID: orgOf(r), UserID: p.User.ID, BaseURL: base, CreatedAt: time.Now()}); err != nil {
		api.Error(w, r, err)
		return
	}
	manifest := forge.AppManifest(s.BaseURL, name, hostID)
	action := base + "/settings/apps/new?state=" + url.QueryEscape(state)
	if in.Organization != "" {
		action = base + "/organizations/" + url.PathEscape(in.Organization) + "/settings/apps/new?state=" + url.QueryEscape(state)
	}
	b, _ := json.Marshal(manifest)
	api.JSON(w, http.StatusOK, map[string]any{"action_url": action, "manifest": string(b)})
}

func (s *Service) githubCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	code, state := r.URL.Query().Get("code"), r.URL.Query().Get("state")
	var st manifestState
	key := "github_manifest:" + auth.Hash(state)
	if state == "" || settings.Get(ctx, s.DB, key, &st) != nil || time.Since(st.CreatedAt) > time.Hour {
		http.Redirect(w, r, "/admin?forge_error=expired", http.StatusFound)
		return
	}
	_ = settings.Delete(ctx, s.DB, key)
	// The person who started the flow finishes it, with the same rights.
	caller, _ := auth.FromContext(ctx)
	allowed := caller.User.ID == st.UserID && caller.User.IsInstanceAdmin
	if caller.User.ID == st.UserID && st.OrgID != "" && !allowed {
		role, err := orgs.Role(ctx, s.DB, st.OrgID, caller.User)
		allowed = err == nil && orgs.AtLeast(role, orgs.Admin)
	}
	if !allowed {
		http.Redirect(w, r, "/admin?forge_error=session", http.StatusFound)
		return
	}
	apiURL := forge.GitHubAPIURL(st.BaseURL)
	conv, err := forge.ConvertManifest(ctx, s.Adapters.HTTP, apiURL, code)
	if err != nil {
		s.Log.Error("github manifest conversion", "error", err)
		http.Redirect(w, r, "/admin?forge_error=conversion", http.StatusFound)
		return
	}
	p, _ := auth.FromContext(ctx)
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		host := "GitHub"
		if st.BaseURL != "https://github.com" {
			u, _ := url.Parse(st.BaseURL)
			host = "GitHub · " + u.Host
		}
		h, err := CreateHost(ctx, tx, s.Secrets, HostInput{ID: st.HostID, OrgID: st.OrgID, Kind: forge.KindGitHub, BaseURL: st.BaseURL, APIURL: apiURL, DisplayName: host,
			AppID: strconv.FormatInt(conv.ID, 10), AppSlug: conv.Slug, ClientID: conv.ClientID, ClientSecret: conv.ClientSecret, PrivateKeyPEM: conv.PEM, WebhookSecret: conv.WebhookSecret})
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, OrgID: st.OrgID, Action: "forge.created", TargetType: "forge_host", TargetID: h.ID, Data: map[string]any{"kind": "github", "app": conv.Slug}})
	})
	if err != nil {
		s.Log.Error("store github app", "error", err)
		http.Redirect(w, r, "/admin?forge_error=store", http.StatusFound)
		return
	}
	// Next step: install the App on an account.
	http.Redirect(w, r, strings.TrimRight(st.BaseURL, "/")+"/apps/"+url.PathEscape(conv.Slug)+"/installations/new", http.StatusFound)
}

func (s *Service) githubHost(w http.ResponseWriter, r *http.Request) (HostRecord, *forge.GitHubApp, bool) {
	h, _, ok := s.host(w, r)
	if !ok {
		return h, nil, false
	}
	if h.Kind != forge.KindGitHub {
		api.Error(w, r, api.Err(http.StatusBadRequest, "not_github", "Only GitHub forges have installations."))
		return h, nil, false
	}
	app, err := s.Adapters.GitHubApp(r.Context(), h)
	if err != nil {
		api.Error(w, r, err)
		return h, nil, false
	}
	return h, app, true
}

// InstallView is an installation with the org that connected it.
type InstallView struct {
	forge.Install
	OrgID string `json:"org_id,omitempty"`
}

// installations lists the App's installations: every one in the instance
// console (with its org), the org's own in the org console. In single mode
// they all belong to the default org.
func (s *Service) installations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	h, app, ok := s.githubHost(w, r)
	if !ok {
		return
	}
	list, err := app.Installations(ctx)
	if err != nil {
		api.Error(w, r, forgeErr(err))
		return
	}
	mode, err := orgs.Mode(ctx, s.DB)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	org := orgOf(r)
	out := []InstallView{}
	for _, in := range list {
		iid, err := UpsertInstall(ctx, s.DB, h.ID, in)
		if err != nil {
			api.Error(w, r, err)
			return
		}
		if mode == orgs.Single {
			if err := ClaimInstall(ctx, s.DB, iid, orgs.DefaultID, ""); err != nil && !errors.Is(err, ErrClaimed) {
				api.Error(w, r, err)
				return
			}
		}
		owner, err := InstallOrg(ctx, s.DB, iid)
		if err != nil {
			api.Error(w, r, err)
			return
		}
		if org == "" || owner == org {
			out = append(out, InstallView{in, owner})
		}
	}
	resp := map[string]any{"items": out, "install_url": strings.TrimRight(h.BaseURL, "/") + "/apps/" + h.AppSlug + "/installations/new"}
	if c, ok := orgs.FromContext(ctx); ok && mode == orgs.Multi {
		// Installing or connecting goes through GitHub so kmdn can check the
		// person can see the installation before the org claims it.
		resp["connect_url"] = "/api/v1/orgs/" + c.Org.Slug + "/admin/forges/" + h.ID + "/connect"
	}
	api.JSON(w, http.StatusOK, resp)
}

func (s *Service) availableRepos(w http.ResponseWriter, r *http.Request) {
	_, app, ok := s.githubHost(w, r)
	if !ok {
		return
	}
	inst := r.URL.Query().Get("installation")
	if inst == "" {
		api.Error(w, r, api.Invalid("installation", "Pick an installation."))
		return
	}
	if org := orgOf(r); org != "" {
		h, _ := GetHost(r.Context(), s.DB, chi.URLParam(r, "id"))
		iid, err := installID(r.Context(), s.DB, h.ID, inst)
		if err == nil {
			err = InstallFor(r.Context(), s.DB, iid, org, "")
		}
		if err != nil {
			api.Error(w, r, api.ErrNotFound)
			return
		}
	}
	list, err := app.InstallationRepos(r.Context(), inst)
	if err != nil {
		api.Error(w, r, forgeErr(err))
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": list})
}

func forgeErr(err error) error {
	var ae *forge.APIError
	if errors.As(err, &ae) {
		return api.Err(http.StatusBadGateway, "forge_error", "The forge answered: "+ae.Message)
	}
	return err
}
