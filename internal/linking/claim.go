package linking

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/forge"
	"github.com/kmdn-app/kmdn/internal/orghttp"
	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/settings"
	"github.com/kmdn-app/kmdn/internal/store"
)

// A GitHub installation is connected to one org (16-organizations.md#forges).
// An org admin either installs the App from kmdn (GitHub hands the
// installation back to the setup URL) or picks an installation that exists;
// either way kmdn then asks GitHub, as that person, which installations they
// can access before the org claims one. Knowing an installation id isn't
// enough to take it.

const forgesPage = "/admin?section=repositories"

// OrgRoutes registers the org console's GitHub connect flow (under /orgs/{org}).
func (s *Service) OrgRoutes(r chi.Router) {
	r.With(orghttp.RequireAdmin).Get("/admin/forges/{id}/connect", s.connect)
}

type installState struct {
	OrgID   string    `json:"org_id"`
	HostID  string    `json:"host_id"`
	UserID  string    `json:"user_id"`
	Created time.Time `json:"created"`
}

// connect starts connecting a GitHub installation to the org: the one given
// by ?installation=, or a new one installed on GitHub.
func (s *Service) connect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, _ := auth.FromContext(ctx)
	org := orghttp.Current(r)
	h, err := repos.GetHost(ctx, s.DB, chi.URLParam(r, "id"))
	if err != nil || h.Kind != forge.KindGitHub || !h.UsableBy(org.ID) || h.ClientID == "" {
		http.Redirect(w, r, forgesPage+"&forge_error=unavailable", http.StatusFound)
		return
	}
	if inst := r.URL.Query().Get("installation"); inst != "" {
		s.authorize(w, r, h, pending{HostID: h.ID, Mode: "claim", UserID: p.User.ID, OrgID: org.ID, InstallID: inst, Redirect: forgesPage, Created: time.Now()})
		return
	}
	state := auth.Token(24)
	if err := settings.Set(ctx, s.DB, "github_install:"+auth.Hash(state), installState{OrgID: org.ID, HostID: h.ID, UserID: p.User.ID, Created: time.Now()}); err != nil {
		http.Redirect(w, r, forgesPage+"&forge_error=store", http.StatusFound)
		return
	}
	http.Redirect(w, r, forge.InstallURL(h.BaseURL, h.AppSlug, state), http.StatusFound)
}

// githubSetup is where GitHub sends people after installing or configuring
// the App. Without kmdn's state (an install started on GitHub, or an App
// made before orgs) it just shows the forges page.
func (s *Service) githubSetup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, _ := auth.FromContext(ctx)
	q := r.URL.Query()
	state, inst := q.Get("state"), q.Get("installation_id")
	if state == "" || inst == "" {
		http.Redirect(w, r, forgesPage, http.StatusFound)
		return
	}
	var st installState
	key := "github_install:" + auth.Hash(state)
	if settings.Get(ctx, s.DB, key, &st) != nil || time.Since(st.Created) > time.Hour || st.UserID != p.User.ID {
		http.Redirect(w, r, forgesPage+"&forge_error=expired", http.StatusFound)
		return
	}
	_ = settings.Delete(ctx, s.DB, key)
	h, err := repos.GetHost(ctx, s.DB, st.HostID)
	if err != nil || !h.UsableBy(st.OrgID) {
		http.Redirect(w, r, forgesPage+"&forge_error=unavailable", http.StatusFound)
		return
	}
	s.authorize(w, r, h, pending{HostID: h.ID, Mode: "claim", UserID: p.User.ID, OrgID: st.OrgID, InstallID: inst, Redirect: forgesPage, Created: time.Now()})
}

// claim finishes connecting: the person must be the one who started, still
// administer the org, and be able to access the installation on GitHub.
func (s *Service) claim(w http.ResponseWriter, r *http.Request, h repos.HostRecord, st pending, token string) {
	ctx := r.Context()
	p, ok := auth.FromContext(ctx)
	if !ok || p.User.ID != st.UserID {
		fail(w, r, st, "session")
		return
	}
	if role, err := orgs.Role(ctx, s.DB, st.OrgID, p.User); err != nil || (!orgs.AtLeast(role, orgs.Admin) && !p.User.IsInstanceAdmin) {
		fail(w, r, st, "session")
		return
	}
	mine, err := forge.UserInstallations(ctx, s.client(), h.APIURL, token)
	if err != nil {
		fail(w, r, st, "forge")
		return
	}
	i := slices.IndexFunc(mine, func(in forge.Install) bool { return in.ExternalID == st.InstallID })
	if i < 0 {
		fail(w, r, st, "not_yours")
		return
	}
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		iid, err := repos.UpsertInstall(ctx, tx, h.ID, mine[i])
		if err != nil {
			return err
		}
		if err := repos.ClaimInstall(ctx, tx, iid, st.OrgID, p.User.ID); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, OrgID: st.OrgID, Action: "forge.installation_connected",
			TargetType: "forge_host", TargetID: h.ID, Data: map[string]any{"installation": st.InstallID, "account": mine[i].AccountLogin}})
	})
	if errors.Is(err, repos.ErrClaimed) {
		fail(w, r, st, "claimed")
		return
	}
	if err != nil {
		fail(w, r, st, "store")
		return
	}
	http.Redirect(w, r, safeRedirect(st.Redirect)+"&connected="+url.QueryEscape(mine[i].AccountLogin), http.StatusFound)
}
