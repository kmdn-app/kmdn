package threads

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// Service serves thread endpoints.
type Service struct {
	DB *store.DB
	// Publish sends live events (realtime.Hub.Publish). Optional.
	Publish func(scope string, event map[string]any)
	// OnComment runs after a comment is posted (mentions and notifications). Optional.
	OnComment func(ctx context.Context, t Thread, c Comment, by users.User)
	// PlaceAnchor stores a new thread's position in the page's document. Optional.
	PlaceAnchor func(ctx context.Context, rev revisions.Revision, c revisions.Caller, path, threadID string, position []byte) error
}

// Routes registers the endpoints.
func (s *Service) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Require)
		r.Get("/revisions/{revision}/threads", s.listForRevision)
		r.Post("/revisions/{revision}/threads", s.createForRevision)
		r.Get("/threads/{thread}", s.get)
		r.Patch("/threads/{thread}", s.patch)
		r.Post("/threads/{thread}/comments", s.reply)
		r.Patch("/comments/{comment}", s.edit)
		r.Delete("/comments/{comment}", s.delete)
		r.Put("/comments/{comment}/reactions/{kind}", s.react(true))
		r.Delete("/comments/{comment}/reactions/{kind}", s.react(false))
	})
}

func (s *Service) role(w http.ResponseWriter, r *http.Request, repoID string) (users.User, access.Role, bool) {
	p, _ := auth.FromContext(r.Context())
	role, err := access.Effective(r.Context(), s.DB, p.User, repoID)
	if err != nil {
		api.Error(w, r, err)
		return p.User, role, false
	}
	if role == access.None {
		api.Error(w, r, api.ErrNotFound)
		return p.User, role, false
	}
	return p.User, role, true
}

func (s *Service) revision(w http.ResponseWriter, r *http.Request) (revisions.Revision, users.User, access.Role, bool) {
	rev, err := revisions.Get(r.Context(), s.DB, chi.URLParam(r, "revision"))
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return rev, users.User{}, "", false
	}
	u, role, ok := s.role(w, r, rev.RepoID)
	return rev, u, role, ok
}

func (s *Service) thread(w http.ResponseWriter, r *http.Request, id string) (Thread, users.User, access.Role, bool) {
	repoID, err := RepoOf(r.Context(), s.DB, id)
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return Thread{}, users.User{}, "", false
	}
	u, role, ok := s.role(w, r, repoID)
	if !ok {
		return Thread{}, u, role, false
	}
	t, err := Get(r.Context(), s.DB, id)
	if err != nil {
		api.Error(w, r, err)
		return t, u, role, false
	}
	return t, u, role, true
}

func (s *Service) emit(t Thread, action string) {
	if s.Publish == nil {
		return
	}
	ev := map[string]any{"type": "thread", "action": action, "thread": t.ID, "path": t.Path}
	if t.RevisionID != "" {
		s.Publish("revision:"+t.RevisionID, ev)
	}
}

func cleanPath(p string) string {
	if u, err := url.PathUnescape(p); err == nil {
		p = u
	}
	return strings.TrimPrefix(path.Clean("/"+strings.TrimSpace(p)), "/")
}

func (s *Service) listForRevision(w http.ResponseWriter, r *http.Request) {
	rev, _, _, ok := s.revision(w, r)
	if !ok {
		return
	}
	f := Filter{RevisionID: rev.ID, Sort: r.URL.Query().Get("sort")}
	if p := r.URL.Query().Get("path"); p != "" {
		f.Path = cleanPath(p)
	}
	list, err := List(r.Context(), s.DB, f)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Service) createForRevision(w http.ResponseWriter, r *http.Request) {
	rev, u, role, ok := s.revision(w, r)
	if !ok {
		return
	}
	if !rev.State.Open() {
		api.Error(w, r, api.Err(http.StatusConflict, "invalid_state", "This revision is closed for comments."))
		return
	}
	var in CreateInput
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	in.Path = cleanPath(in.Path)
	if !repos.IsMarkdown(in.Path) {
		api.Error(w, r, api.Invalid("path", "Comment on a page of the revision."))
		return
	}
	round := 0
	if rev.State == revisions.InReview || rev.State == revisions.Approved {
		round = rev.ReviewRound
	}
	t, err := Create(r.Context(), s.DB, rev.RepoID, rev.ID, KindRevision, round, u.ID, in)
	if errors.Is(err, errBody) {
		api.Error(w, r, api.Invalid("body", "Write a comment (up to 20,000 characters)."))
		return
	}
	if err != nil {
		api.Error(w, r, err)
		return
	}
	if len(in.Position) > 0 && string(in.Position) != "null" && s.PlaceAnchor != nil {
		if len(in.Position) > 8<<10 {
			api.Error(w, r, api.Invalid("position", "Position is too large."))
			return
		}
		if err := s.PlaceAnchor(r.Context(), rev, revisions.Caller{User: u, Role: role}, t.Path, t.ID, in.Position); err != nil {
			api.Error(w, r, err)
			return
		}
	}
	_ = revisions.Record(r.Context(), s.DB, rev.ID, revisions.ActorUser, u.ID, "thread_opened", map[string]any{"thread": t.ID, "path": t.Path})
	if s.OnComment != nil && len(t.Comments) > 0 {
		s.OnComment(r.Context(), t, t.Comments[0], u)
	}
	s.emit(t, "created")
	api.JSON(w, http.StatusCreated, t)
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	t, _, _, ok := s.thread(w, r, chi.URLParam(r, "thread"))
	if !ok {
		return
	}
	api.JSON(w, http.StatusOK, t)
}

func (s *Service) patch(w http.ResponseWriter, r *http.Request) {
	t, u, role, ok := s.thread(w, r, chi.URLParam(r, "thread"))
	if !ok {
		return
	}
	var in struct {
		State string `json:"state"`
	}
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	if in.State != StateOpen && in.State != StateResolved {
		api.Error(w, r, api.Invalid("state", "state is open or resolved."))
		return
	}
	// Anyone who can edit the repository, or who started the thread.
	if !role.AtLeast(access.Contributor) && t.CreatedBy != u.ID {
		api.Error(w, r, api.Err(http.StatusForbidden, "forbidden", "You can't resolve this thread."))
		return
	}
	if err := SetState(r.Context(), s.DB, t.ID, u.ID, in.State); err != nil {
		api.Error(w, r, err)
		return
	}
	t, _ = Get(r.Context(), s.DB, t.ID)
	s.emit(t, in.State)
	api.JSON(w, http.StatusOK, t)
}

func (s *Service) reply(w http.ResponseWriter, r *http.Request) {
	t, u, _, ok := s.thread(w, r, chi.URLParam(r, "thread"))
	if !ok {
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	c, err := Reply(r.Context(), s.DB, t.ID, u.ID, in.Body)
	if errors.Is(err, errBody) {
		api.Error(w, r, api.Invalid("body", "Write a comment (up to 20,000 characters)."))
		return
	}
	if err != nil {
		api.Error(w, r, err)
		return
	}
	c.AuthorName = u.Name
	if s.OnComment != nil {
		s.OnComment(r.Context(), t, c, u)
	}
	s.emit(t, "reply")
	api.JSON(w, http.StatusCreated, c)
}

func (s *Service) comment(w http.ResponseWriter, r *http.Request) (CommentInfo, Thread, users.User, bool) {
	ci, err := CommentOf(r.Context(), s.DB, chi.URLParam(r, "comment"))
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return ci, Thread{}, users.User{}, false
	}
	t, u, _, ok := s.thread(w, r, ci.ThreadID)
	return ci, t, u, ok
}

func (s *Service) edit(w http.ResponseWriter, r *http.Request) {
	ci, t, u, ok := s.comment(w, r)
	if !ok {
		return
	}
	if ci.AuthorID != u.ID || ci.Deleted {
		api.Error(w, r, api.Err(http.StatusForbidden, "forbidden", "You can only edit your own comments."))
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	if err := Edit(r.Context(), s.DB, chi.URLParam(r, "comment"), in.Body); err != nil {
		if errors.Is(err, errBody) {
			api.Error(w, r, api.Invalid("body", "Write a comment (up to 20,000 characters)."))
			return
		}
		api.Error(w, r, err)
		return
	}
	s.emit(t, "edited")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) delete(w http.ResponseWriter, r *http.Request) {
	ci, t, u, ok := s.comment(w, r)
	if !ok {
		return
	}
	if ci.AuthorID != u.ID && !u.IsInstanceAdmin {
		api.Error(w, r, api.Err(http.StatusForbidden, "forbidden", "You can only delete your own comments."))
		return
	}
	if err := Delete(r.Context(), s.DB, chi.URLParam(r, "comment")); err != nil {
		api.Error(w, r, err)
		return
	}
	s.emit(t, "deleted")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) react(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// chi matches the raw path, so "+1" arrives as "%2B1".
		kind, _ := url.PathUnescape(chi.URLParam(r, "kind"))
		if !Reactions[kind] {
			api.Error(w, r, api.Invalid("kind", "Reactions are +1, check or eyes."))
			return
		}
		_, t, u, ok := s.comment(w, r)
		if !ok {
			return
		}
		if err := React(r.Context(), s.DB, chi.URLParam(r, "comment"), u.ID, kind, on); err != nil {
			api.Error(w, r, err)
			return
		}
		s.emit(t, "reaction")
		w.WriteHeader(http.StatusNoContent)
	}
}
