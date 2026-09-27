package updates

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/textdiff"
)

// Routes registers the endpoints.
func (s *Service) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Require)
		r.Get("/revisions/{revision}/updates", s.get)
		r.Post("/revisions/{revision}/updates/{update}/apply", s.apply)
		r.Post("/revisions/{revision}/conflicts/resolve", s.resolvePage)
	})
}

// View is a pending update as the preview shows it.
type View struct {
	ID         string     `json:"id"`
	TargetSHA  string     `json:"target_sha"`
	PreparedAt time.Time  `json:"prepared_at"`
	LastCommit *Commit    `json:"last_commit,omitempty"`
	Files      []FileView `json:"files"`
	Conflicts  int        `json:"conflicts"`
}

// Commit is the latest Published commit an update brings in.
type Commit struct {
	SHA        string    `json:"sha"`
	AuthorName string    `json:"author_name"`
	Title      string    `json:"title"`
	Date       time.Time `json:"date"`
}

// FileView is a page of the update with what Published changed in it.
type FileView struct {
	Path      string          `json:"path"`
	Kind      string          `json:"kind"`
	Conflicts int             `json:"conflicts"`
	Additions int             `json:"additions"`
	Deletions int             `json:"deletions"`
	Hunks     []textdiff.Hunk `json:"hunks"`
}

func (s *Service) load(w http.ResponseWriter, r *http.Request) (revisions.Revision, repos.Repo, revisions.Caller, bool) {
	p, _ := auth.FromContext(r.Context())
	rev, err := revisions.Get(r.Context(), s.DB, chi.URLParam(r, "revision"))
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return rev, repos.Repo{}, revisions.Caller{}, false
	}
	repo, err := repos.Get(r.Context(), s.DB, rev.RepoID)
	if err != nil {
		api.Error(w, r, err)
		return rev, repo, revisions.Caller{}, false
	}
	role, err := access.Effective(r.Context(), s.DB, p.User, repo.ID)
	if err != nil {
		api.Error(w, r, err)
		return rev, repo, revisions.Caller{}, false
	}
	if role == access.None {
		api.Error(w, r, api.ErrNotFound)
		return rev, repo, revisions.Caller{}, false
	}
	return rev, repo, revisions.Caller{User: p.User, Role: role}, true
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	rev, repo, c, ok := s.load(w, r)
	if !ok {
		return
	}
	u, err := Pending(r.Context(), s.DB, rev.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	acc, err := s.Revisions.AccessFor(r.Context(), rev, c)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	out := map[string]any{"update": nil, "can_apply": acc.CanEdit}
	if u != nil {
		v := View{ID: u.ID, TargetSHA: u.TargetSHA, PreparedAt: u.PreparedAt, Files: []FileView{}}
		for _, f := range u.Files {
			add, del := textdiff.Stats(f.BaseMD, f.TheirsMD)
			v.Files = append(v.Files, FileView{Path: f.Path, Kind: f.Kind, Conflicts: f.Conflicts, Additions: add, Deletions: del, Hunks: textdiff.Hunks(f.BaseMD, f.TheirsMD, 3)})
			v.Conflicts += f.Conflicts
		}
		if len(u.Files) > 0 {
			if log, err := s.Repos.Mirror(repo).Log(r.Context(), u.TargetSHA, upstreamOf(rev, u.Files[0].Path, s, r), 1); err == nil && len(log) > 0 {
				v.LastCommit = &Commit{SHA: log[0].SHA, AuthorName: log[0].AuthorName, Title: log[0].Title, Date: log[0].Date}
			}
		}
		out["update"] = v
	}
	api.JSON(w, http.StatusOK, out)
}

// upstreamOf is the Published path of a revision page (its old path when renamed).
func upstreamOf(rev revisions.Revision, p string, s *Service, r *http.Request) string {
	if f, err := revisions.FileAt(r.Context(), s.DB, rev.ID, p); err == nil {
		return upstreamPath(f)
	}
	return p
}

func (s *Service) apply(w http.ResponseWriter, r *http.Request) {
	rev, _, c, ok := s.load(w, r)
	if !ok {
		return
	}
	res, err := s.Apply(r.Context(), rev, c, chi.URLParam(r, "update"))
	if err != nil {
		var cf *revisions.ErrConflict
		switch {
		case errors.Is(err, revisions.ErrForbidden):
			api.Error(w, r, api.Err(http.StatusForbidden, "forbidden", "You can't apply updates to this revision right now."))
		case errors.As(err, &cf):
			api.Error(w, r, api.Err(http.StatusConflict, cf.Code, cf.Msg))
		default:
			api.Error(w, r, err)
		}
		return
	}
	api.JSON(w, http.StatusOK, res)
}

func (s *Service) resolvePage(w http.ResponseWriter, r *http.Request) {
	rev, _, c, ok := s.load(w, r)
	if !ok {
		return
	}
	var in struct {
		Path   string `json:"path"`
		Choice string `json:"choice"`
	}
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	if in.Choice != "keep" && in.Choice != "delete" {
		api.Error(w, r, api.Invalid("choice", "choice is keep or delete."))
		return
	}
	if err := s.ResolvePage(r.Context(), rev, c, in.Path, in.Choice); err != nil {
		var cf *revisions.ErrConflict
		switch {
		case errors.Is(err, revisions.ErrForbidden):
			api.Error(w, r, api.Err(http.StatusForbidden, "forbidden", "You can't edit this revision right now."))
		case errors.As(err, &cf):
			api.Error(w, r, api.Err(http.StatusConflict, cf.Code, cf.Msg))
		default:
			api.Error(w, r, err)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
