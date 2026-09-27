package revisions

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
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/gitmirror"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// View is a revision with what the caller can do and who works on it.
type View struct {
	Revision
	Access  Access   `json:"access"`
	Members []Member `json:"members"`
	Files   int      `json:"file_count"`
}

// Routes registers revision endpoints.
func (s *Service) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Require)
		r.Get("/repos/{repo}/revisions", s.list)
		r.Post("/repos/{repo}/revisions", s.create)
		r.Get("/repos/{repo}/revisions/by-number/{number}", s.byNumber)
		r.Get("/repos/{repo}/templates", s.templates)
		r.Route("/revisions/{revision}", func(r chi.Router) {
			r.Get("/", s.get)
			r.Patch("/", s.update)
			r.Post("/close", s.close)
			r.Post("/reopen", s.reopen)
			r.Get("/tree", s.tree)
			r.Get("/files", s.files)
			r.Post("/files", s.fileOp)
			r.Get("/files/*", s.read)
			r.Get("/members", s.members)
			r.Put("/members/{user}", s.putMember)
			r.Delete("/members/{user}", s.deleteMember)
			r.Get("/events", s.events)
			r.Get("/assets", s.listAssets)
			r.Post("/assets", s.upload)
			r.Get("/raw/*", s.raw)
		})
	})
}

func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var inv *ErrInvalid
	var cf *ErrConflict
	switch {
	case errors.As(err, &inv):
		api.Error(w, r, api.Invalid(inv.Field, inv.Msg))
	case errors.As(err, &cf):
		api.Error(w, r, api.Err(http.StatusConflict, cf.Code, cf.Msg))
	case errors.Is(err, ErrForbidden):
		api.Error(w, r, api.Err(http.StatusForbidden, "forbidden", "You can't do this on this revision."))
	case errors.Is(err, store.ErrNotFound), errors.Is(err, gitmirror.ErrNotFound), errors.Is(err, repos.ErrOutOfScope):
		api.Error(w, r, api.ErrNotFound)
	default:
		api.Error(w, r, err)
	}
}

// loadRepo resolves {repo} and the caller's role (404 without access).
func (s *Service) loadRepo(w http.ResponseWriter, r *http.Request, repoID string) (repos.Repo, Caller, bool) {
	p, _ := auth.FromContext(r.Context())
	repo, err := repos.Get(r.Context(), s.DB, repoID)
	if err != nil {
		writeErr(w, r, err)
		return repo, Caller{}, false
	}
	role, err := access.Effective(r.Context(), s.DB, p.User, repo.ID)
	if err != nil {
		api.Error(w, r, err)
		return repo, Caller{}, false
	}
	if role == access.None {
		api.Error(w, r, api.ErrNotFound)
		return repo, Caller{}, false
	}
	return repo, Caller{User: p.User, Role: role}, true
}

func (s *Service) load(w http.ResponseWriter, r *http.Request) (Revision, repos.Repo, Caller, bool) {
	rev, err := Get(r.Context(), s.DB, chi.URLParam(r, "revision"))
	if err != nil {
		writeErr(w, r, err)
		return rev, repos.Repo{}, Caller{}, false
	}
	repo, c, ok := s.loadRepo(w, r, rev.RepoID)
	return rev, repo, c, ok
}

func (s *Service) view(r *http.Request, rev Revision, c Caller) (View, error) {
	a, err := s.AccessFor(r.Context(), rev, c)
	if err != nil {
		return View{}, err
	}
	ms, err := Members(r.Context(), s.DB, rev.ID)
	if err != nil {
		return View{}, err
	}
	var n int
	if err := store.QueryRow(r.Context(), s.DB, `SELECT COUNT(*) FROM revision_files WHERE revision_id = ?`, rev.ID).Scan(&n); err != nil {
		return View{}, err
	}
	return View{Revision: rev, Access: a, Members: ms, Files: n}, nil
}

func (s *Service) respond(w http.ResponseWriter, r *http.Request, status int, rev Revision, c Caller) {
	v, err := s.view(r, rev, c)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, status, v)
}

// States accepted by ?state=: a state name, "open" or "all".
func parseStates(q string) ([]State, bool) {
	switch q {
	case "", "open":
		return []State{Editing, InReview, Approved, Publishing}, true
	case "all":
		return nil, true
	}
	var out []State
	for _, p := range strings.Split(q, ",") {
		switch st := State(p); st {
		case Editing, InReview, Approved, Publishing, Published, Closed:
			out = append(out, st)
		default:
			return nil, false
		}
	}
	return out, true
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	repo, c, ok := s.loadRepo(w, r, chi.URLParam(r, "repo"))
	if !ok {
		return
	}
	states, ok := parseStates(r.URL.Query().Get("state"))
	if !ok {
		api.Error(w, r, api.Invalid("state", "Unknown state."))
		return
	}
	f := Filter{States: states}
	if r.URL.Query().Get("mine") == "true" {
		f.Member = c.User.ID
	}
	list, err := List(r.Context(), s.DB, repo.ID, f)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	out := make([]View, 0, len(list))
	for _, rev := range list {
		v, err := s.view(r, rev, c)
		if err != nil {
			api.Error(w, r, err)
			return
		}
		out = append(out, v)
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	repo, c, ok := s.loadRepo(w, r, chi.URLParam(r, "repo"))
	if !ok {
		return
	}
	var in CreateInput
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	rev, err := s.Create(r.Context(), repo, c, in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.respond(w, r, http.StatusCreated, rev, c)
}

func (s *Service) byNumber(w http.ResponseWriter, r *http.Request) {
	repo, c, ok := s.loadRepo(w, r, chi.URLParam(r, "repo"))
	if !ok {
		return
	}
	n, err := strconv.Atoi(chi.URLParam(r, "number"))
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	rev, err := ByNumber(r.Context(), s.DB, repo.ID, n)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.respond(w, r, http.StatusOK, rev, c)
}

func (s *Service) templates(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := s.loadRepo(w, r, chi.URLParam(r, "repo"))
	if !ok {
		return
	}
	list, err := s.Repos.Templates(r.Context(), repo)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	rev, _, c, ok := s.load(w, r)
	if !ok {
		return
	}
	s.respond(w, r, http.StatusOK, rev, c)
}

func (s *Service) update(w http.ResponseWriter, r *http.Request) {
	rev, _, c, ok := s.load(w, r)
	if !ok {
		return
	}
	var in UpdateInput
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	rev, err := s.Update(r.Context(), rev, c, in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.respond(w, r, http.StatusOK, rev, c)
}

func (s *Service) close(w http.ResponseWriter, r *http.Request) {
	rev, _, c, ok := s.load(w, r)
	if !ok {
		return
	}
	rev, err := s.Close(r.Context(), rev, c)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.respond(w, r, http.StatusOK, rev, c)
}

func (s *Service) reopen(w http.ResponseWriter, r *http.Request) {
	rev, _, c, ok := s.load(w, r)
	if !ok {
		return
	}
	rev, err := s.Reopen(r.Context(), rev, c)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.respond(w, r, http.StatusOK, rev, c)
}

func (s *Service) tree(w http.ResponseWriter, r *http.Request) {
	rev, repo, _, ok := s.load(w, r)
	if !ok {
		return
	}
	nodes, err := s.Tree(r.Context(), repo, rev)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"base_sha": rev.BaseSHA, "root": repo.Scope().Root, "items": nodes})
}

func (s *Service) files(w http.ResponseWriter, r *http.Request) {
	rev, _, _, ok := s.load(w, r)
	if !ok {
		return
	}
	list, err := Files(r.Context(), s.DB, rev.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Service) fileOp(w http.ResponseWriter, r *http.Request) {
	rev, repo, c, ok := s.load(w, r)
	if !ok {
		return
	}
	var op FileOp
	if err := api.DecodeLimit(r, &op, MaxFileSize+4096); err != nil {
		api.Error(w, r, err)
		return
	}
	f, err := s.ApplyFileOp(r.Context(), repo, rev, c, op)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, f)
}

// wildcard decodes and cleans {path} (see repos.filePath).
func wildcard(r *http.Request) string {
	p := chi.URLParam(r, "*")
	if u, err := url.PathUnescape(p); err == nil {
		p = u
	}
	return strings.TrimPrefix(path.Clean("/"+p), "/")
}

func (s *Service) read(w http.ResponseWriter, r *http.Request) {
	rev, repo, _, ok := s.load(w, r)
	if !ok {
		return
	}
	c, err := s.Read(r.Context(), repo, rev, wildcard(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, c)
}

func (s *Service) members(w http.ResponseWriter, r *http.Request) {
	rev, _, _, ok := s.load(w, r)
	if !ok {
		return
	}
	ms, err := Members(r.Context(), s.DB, rev.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": ms})
}

func (s *Service) putMember(w http.ResponseWriter, r *http.Request) {
	rev, repo, c, ok := s.load(w, r)
	if !ok {
		return
	}
	u, err := users.ByID(r.Context(), s.DB, chi.URLParam(r, "user"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	role, err := access.Effective(r.Context(), s.DB, u, repo.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	if err := s.AddMember(r.Context(), rev, c, u.ID, role); err != nil {
		writeErr(w, r, err)
		return
	}
	s.members(w, r)
}

func (s *Service) deleteMember(w http.ResponseWriter, r *http.Request) {
	rev, _, c, ok := s.load(w, r)
	if !ok {
		return
	}
	if err := s.RemoveMember(r.Context(), rev, c, chi.URLParam(r, "user")); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) events(w http.ResponseWriter, r *http.Request) {
	rev, _, _, ok := s.load(w, r)
	if !ok {
		return
	}
	list, err := Events(r.Context(), s.DB, rev.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Service) listAssets(w http.ResponseWriter, r *http.Request) {
	rev, _, _, ok := s.load(w, r)
	if !ok {
		return
	}
	list, err := Assets(r.Context(), s.DB, rev.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": list})
}

// upload takes multipart form fields "file" and "page" (the page the image is
// inserted into, which decides where it's stored).
func (s *Service) upload(w http.ResponseWriter, r *http.Request) {
	rev, repo, c, ok := s.load(w, r)
	if !ok {
		return
	}
	limit := maxAssetBytes(repo, s.UploadMaxMB)
	r.Body = http.MaxBytesReader(w, r.Body, limit+1<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		api.Error(w, r, api.Invalid("file", "Send the image as multipart form data (up to the size limit)."))
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	f, hdr, err := r.FormFile("file")
	if err != nil {
		api.Error(w, r, api.Invalid("file", "Attach an image in the \"file\" field."))
		return
	}
	defer f.Close()
	res, err := s.Upload(r.Context(), repo, rev, c, r.FormValue("page"), hdr.Filename, f)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	api.JSON(w, http.StatusCreated, res)
}

// raw serves images as the revision sees them: uploads first, then the base.
// Like the published raw endpoint, types are fixed and SVGs are sandboxed.
func (s *Service) raw(w http.ResponseWriter, r *http.Request) {
	rev, repo, _, ok := s.load(w, r)
	if !ok {
		return
	}
	p := wildcard(r)
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(p)), ".")
	ct, known := assetTypes[ext]
	if !known {
		api.Error(w, r, api.Err(http.StatusUnsupportedMediaType, "unsupported_type", "Only images can be served here."))
		return
	}
	b, _, err := s.ReadAsset(r.Context(), rev, p)
	if errors.Is(err, store.ErrNotFound) {
		var f repos.File
		if f, err = s.Repos.ReadFile(r.Context(), repo, p, rev.BaseSHA); err == nil {
			b = []byte(f.Content)
		}
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	h.Set("Cache-Control", "private, no-cache")
	_, _ = w.Write(b)
}
