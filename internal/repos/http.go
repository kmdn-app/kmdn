package repos

import (
	"errors"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/gitmirror"
	"github.com/kmdn-app/kmdn/internal/groups"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// RepoView is a repo plus the caller's role.
type RepoView struct {
	Repo
	Role  access.Role `json:"role"`
	Scope Scope       `json:"scope"`
}

// Routes registers repository endpoints.
func (s *Service) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Require)
		r.Get("/repos", s.list)
		r.With(auth.RequireAdmin).Post("/repos", s.connect)
		r.Get("/repos/by-slug/{owner}/{name}", s.bySlug)
		r.Route("/repos/{repo}", func(r chi.Router) {
			r.Get("/", s.get)
			r.Patch("/", s.update)
			r.Delete("/", s.disconnect)
			r.Post("/refresh", s.refresh)
			r.Get("/tree", s.tree)
			r.Get("/files/*", s.file)
			r.Get("/raw/*", s.raw)
			r.Get("/activity", s.activity)
			r.Get("/history/*", s.history)
			r.Get("/blame/*", s.blame)
			r.Get("/webhook", s.webhook)
			r.Get("/members", s.members)
			r.Put("/members/{type}/{id}", s.putMember)
			r.Delete("/members/{type}/{id}", s.deleteMember)
		})
	})
}

func (s *Service) load(w http.ResponseWriter, r *http.Request, min access.Role) (Repo, access.Role, bool) {
	p, _ := auth.FromContext(r.Context())
	repo, err := Get(r.Context(), s.DB, chi.URLParam(r, "repo"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			api.Error(w, r, api.ErrNotFound)
		} else {
			api.Error(w, r, err)
		}
		return repo, "", false
	}
	role, err := access.Effective(r.Context(), s.DB, p.User, repo.ID)
	if err != nil {
		api.Error(w, r, err)
		return repo, role, false
	}
	if role == access.None {
		api.Error(w, r, api.ErrNotFound) // don't reveal repos the caller can't see
		return repo, role, false
	}
	if !role.AtLeast(min) {
		api.Error(w, r, api.Err(http.StatusForbidden, "forbidden", "You need the "+string(min)+" role on this repository."))
		return repo, role, false
	}
	return repo, role, true
}

func view(r Repo, role access.Role) RepoView { return RepoView{Repo: r, Role: role, Scope: r.Scope()} }

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	idsList, err := access.RepoIDs(r.Context(), s.DB, p.User)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	list, err := List(r.Context(), s.DB, idsList, p.User.IsInstanceAdmin)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	out := make([]RepoView, 0, len(list))
	for _, repo := range list {
		role, err := access.Effective(r.Context(), s.DB, p.User, repo.ID)
		if err != nil {
			api.Error(w, r, err)
			return
		}
		out = append(out, view(repo, role))
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Service) connect(w http.ResponseWriter, r *http.Request) {
	var in ConnectInput
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	p, _ := auth.FromContext(r.Context())
	repo, jobID, err := s.Connect(r.Context(), p, in)
	if err != nil {
		var inv *ErrInvalid
		if errors.As(err, &inv) {
			api.Error(w, r, api.Invalid(inv.Field, inv.Msg))
			return
		}
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusAccepted, map[string]any{"repo": view(repo, access.Admin), "job_id": jobID})
}

func (s *Service) bySlug(w http.ResponseWriter, r *http.Request) {
	repo, err := BySlug(r.Context(), s.DB, chi.URLParam(r, "owner"), chi.URLParam(r, "name"))
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	rctx := chi.RouteContext(r.Context())
	rctx.URLParams.Add("repo", repo.ID)
	s.get(w, r)
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	repo, role, ok := s.load(w, r, access.Viewer)
	if !ok {
		return
	}
	api.JSON(w, http.StatusOK, view(repo, role))
}

func (s *Service) update(w http.ResponseWriter, r *http.Request) {
	repo, role, ok := s.load(w, r, access.Admin)
	if !ok {
		return
	}
	var in UpdateInput
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	p, _ := auth.FromContext(r.Context())
	updated, err := s.Update(r.Context(), p, repo, in)
	if err != nil {
		var inv *ErrInvalid
		if errors.As(err, &inv) {
			api.Error(w, r, api.Invalid(inv.Field, inv.Msg))
			return
		}
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, view(updated, role))
}

func (s *Service) disconnect(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := s.load(w, r, access.Admin)
	if !ok {
		return
	}
	p, _ := auth.FromContext(r.Context())
	if err := s.Disconnect(r.Context(), p, repo); err != nil {
		api.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) refresh(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := s.load(w, r, access.Maintainer)
	if !ok {
		return
	}
	id, err := s.EnqueueFullSync(r.Context(), repo.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	s.Jobs.Notify()
	api.JSON(w, http.StatusAccepted, map[string]string{"job_id": id})
}

func (s *Service) tree(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := s.load(w, r, access.Viewer)
	if !ok {
		return
	}
	nodes, err := s.Tree(r.Context(), repo)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"head_sha": repo.HeadSHA, "root": repo.Scope().Root, "items": nodes})
}

// filePath returns the {path} wildcard. chi matches on the escaped path, so a
// client that encodes "/" as %2F (openapi-fetch does) needs it decoded here.
// Cleaning after decoding keeps "docs/..%2F.." from leaving the content root.
func filePath(r *http.Request) string {
	p := chi.URLParam(r, "*")
	if u, err := url.PathUnescape(p); err == nil {
		p = u
	}
	return strings.TrimPrefix(path.Clean("/"+p), "/")
}

func readErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrOutOfScope):
		api.Error(w, r, api.Err(http.StatusNotFound, "out_of_scope", "This file is outside the repository's content root or filters."))
	case errors.Is(err, gitmirror.ErrNotFound):
		api.Error(w, r, api.ErrNotFound)
	default:
		api.Error(w, r, err)
	}
}

func (s *Service) file(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := s.load(w, r, access.Viewer)
	if !ok {
		return
	}
	f, err := s.ReadFile(r.Context(), repo, filePath(r), r.URL.Query().Get("sha"))
	if err != nil {
		readErr(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, f)
}

var rawTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
	".avif": "image/avif", ".svg": "image/svg+xml", ".pdf": "application/pdf", ".md": "text/plain; charset=utf-8",
	".markdown": "text/plain; charset=utf-8", ".mdx": "text/plain; charset=utf-8",
}

// raw serves file bytes (images in pages). Content is untrusted: types are
// fixed by extension, sniffing is off, and SVG/PDF run in a CSP sandbox.
func (s *Service) raw(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := s.load(w, r, access.Viewer)
	if !ok {
		return
	}
	p := filePath(r)
	ct, known := rawTypes[strings.ToLower(path.Ext(p))]
	if !known {
		api.Error(w, r, api.Err(http.StatusUnsupportedMediaType, "unsupported_type", "Only images, PDFs and markdown can be served."))
		return
	}
	f, err := s.ReadFile(r.Context(), repo, p, r.URL.Query().Get("sha"))
	if err != nil {
		readErr(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	h.Set("Content-Disposition", "inline")
	if r.URL.Query().Get("sha") != "" {
		h.Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "private, no-cache")
		h.Set("ETag", `"`+repo.HeadSHA+`"`)
		if r.Header.Get("If-None-Match") == `"`+repo.HeadSHA+`"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	_, _ = w.Write([]byte(f.Content))
}

func (s *Service) activity(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := s.load(w, r, access.Viewer)
	if !ok {
		return
	}
	if repo.HeadSHA == "" {
		api.JSON(w, http.StatusOK, map[string]any{"items": []any{}})
		return
	}
	list, err := s.Mirror(repo).Recent(r.Context(), repo.HeadSHA, 30)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	sc := repo.Scope()
	type item struct {
		gitmirror.Commit
		Paths []string `json:"paths"`
	}
	out := []item{}
	for _, c := range list {
		var paths []string
		for _, p := range c.Paths {
			if sc.Contains(p) && IsMarkdown(p) {
				paths = append(paths, p)
			}
		}
		if len(paths) > 0 {
			out = append(out, item{Commit: c.Commit, Paths: paths})
		}
		if len(out) >= 15 {
			break
		}
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Service) history(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := s.load(w, r, access.Viewer)
	if !ok {
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if n <= 0 || n > 200 {
		n = 50
	}
	list, err := s.History(r.Context(), repo, filePath(r), n)
	if err != nil {
		readErr(w, r, err)
		return
	}
	if list == nil {
		list = []gitmirror.Commit{}
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Service) blame(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := s.load(w, r, access.Viewer)
	if !ok {
		return
	}
	lines, err := s.Blame(r.Context(), repo, filePath(r))
	if err != nil {
		readErr(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": lines})
}

// webhook shows repository admins where forges should send push events
// (plain git and GitLab; GitHub Apps are configured automatically).
func (s *Service) webhook(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := s.load(w, r, access.Admin)
	if !ok {
		return
	}
	url, secret, err := s.WebhookInfo(r.Context(), repo)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	header := ""
	switch repo.ForgeKind {
	case "git":
		header = "X-Kmdn-Token"
	case "gitlab":
		header = "X-Gitlab-Token"
	}
	api.JSON(w, http.StatusOK, map[string]string{"url": url, "secret": secret, "header": header})
}

func (s *Service) members(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := s.load(w, r, access.Viewer)
	if !ok {
		return
	}
	m, err := access.Members(r.Context(), s.DB, repo.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	if m == nil {
		m = []access.Member{}
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": m})
}

func (s *Service) putMember(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := s.load(w, r, access.Admin)
	if !ok {
		return
	}
	var body struct {
		Role string `json:"role"`
	}
	if err := api.Decode(r, &body); err != nil {
		api.Error(w, r, err)
		return
	}
	role, err := access.Parse(body.Role)
	if err != nil {
		api.Error(w, r, api.Invalid("role", err.Error()))
		return
	}
	ptype, pid := chi.URLParam(r, "type"), chi.URLParam(r, "id")
	ctx := r.Context()
	switch ptype {
	case access.UserPrincipal:
		if _, err := users.ByID(ctx, s.DB, pid); err != nil {
			api.Error(w, r, api.Invalid("id", "No such user."))
			return
		}
	case access.GroupPrincipal:
		if _, err := groups.Get(ctx, s.DB, pid); err != nil {
			api.Error(w, r, api.Invalid("id", "No such group."))
			return
		}
	default:
		api.Error(w, r, api.Invalid("type", "Type must be user or group."))
		return
	}
	p, _ := auth.FromContext(ctx)
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		if err := access.Grant(ctx, tx, repo.ID, ptype, pid, role); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, Action: "member.role_changed", TargetType: ptype, TargetID: pid, RepoID: repo.ID, Data: map[string]any{"role": role}})
	})
	if err != nil {
		api.Error(w, r, err)
		return
	}
	s.members(w, r)
}

func (s *Service) deleteMember(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := s.load(w, r, access.Admin)
	if !ok {
		return
	}
	ctx := r.Context()
	p, _ := auth.FromContext(ctx)
	ptype, pid := chi.URLParam(r, "type"), chi.URLParam(r, "id")
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		if err := access.Revoke(ctx, tx, repo.ID, ptype, pid); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, Action: "member.removed", TargetType: ptype, TargetID: pid, RepoID: repo.ID})
	})
	if err != nil {
		api.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
