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
	"github.com/kmdn-app/kmdn/internal/settings"
	"github.com/kmdn-app/kmdn/internal/store"
)

// ForgeRoutes registers /admin/forges endpoints (instance admins only).
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
	// session cookie is present (same site, top-level GET).
	r.With(auth.RequireAdmin).Get("/admin/forges/github/callback", s.githubCallback)
}

func (s *Service) listHosts(w http.ResponseWriter, r *http.Request) {
	hs, err := ListHosts(r.Context(), s.DB)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": hs})
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
		h, err = CreateHost(ctx, tx, s.Secrets, HostInput{Kind: in.Kind, BaseURL: in.BaseURL, DisplayName: in.DisplayName, ClientID: in.ClientID, ClientSecret: in.ClientSecret})
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, Action: "forge.created", TargetType: "forge_host", TargetID: h.ID, Data: map[string]any{"kind": in.Kind}})
	})
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusCreated, h)
}

func (s *Service) deleteHost(w http.ResponseWriter, r *http.Request) {
	h, err := GetHost(r.Context(), s.DB, chi.URLParam(r, "id"))
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	if h.Repos > 0 {
		api.Error(w, r, api.Err(http.StatusConflict, "forge_in_use", "Disconnect this forge's repositories first."))
		return
	}
	ctx := r.Context()
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
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
	if err := settings.Set(r.Context(), s.DB, "github_manifest:"+auth.Hash(state), manifestState{HostID: hostID, BaseURL: base, CreatedAt: time.Now()}); err != nil {
		api.Error(w, r, err)
		return
	}
	manifest := forge.AppManifest(s.BaseURL, name)
	manifest["hook_attributes"] = map[string]any{"url": strings.TrimRight(s.BaseURL, "/") + "/hooks/github/" + hostID, "active": true}
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
		h, err := CreateHost(ctx, tx, s.Secrets, HostInput{ID: st.HostID, Kind: forge.KindGitHub, BaseURL: st.BaseURL, APIURL: apiURL, DisplayName: host,
			AppID: strconv.FormatInt(conv.ID, 10), AppSlug: conv.Slug, ClientID: conv.ClientID, ClientSecret: conv.ClientSecret, PrivateKeyPEM: conv.PEM, WebhookSecret: conv.WebhookSecret})
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, Action: "forge.created", TargetType: "forge_host", TargetID: h.ID, Data: map[string]any{"kind": "github", "app": conv.Slug}})
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
	h, err := GetHost(r.Context(), s.DB, chi.URLParam(r, "id"))
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
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

func (s *Service) installations(w http.ResponseWriter, r *http.Request) {
	h, app, ok := s.githubHost(w, r)
	if !ok {
		return
	}
	list, err := app.Installations(r.Context())
	if err != nil {
		api.Error(w, r, forgeErr(err))
		return
	}
	for _, in := range list {
		if _, err := UpsertInstall(r.Context(), s.DB, h.ID, in); err != nil {
			api.Error(w, r, err)
			return
		}
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": list, "install_url": strings.TrimRight(h.BaseURL, "/") + "/apps/" + h.AppSlug + "/installations/new"})
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
