package notify

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
)

// AutoFollowFor is how long editors of a published page follow it.
var AutoFollowFor = 30 * 24 * time.Hour

// ReadDebounce: reads of the same version within this are not recorded again.
var ReadDebounce = time.Minute

// followKey is a follow's path: a page, or a folder ending in "/".
func followKey(p string, folder bool) string {
	p = strings.Trim(path.Clean("/"+strings.TrimSpace(p)), "/")
	if folder {
		if p == "" || p == "." {
			return "/"
		}
		return p + "/"
	}
	return p
}

// Followers returns who follows a page (directly or through a folder), and
// whose automatic follow hasn't expired.
func Followers(ctx context.Context, q store.Querier, repoID, page string) ([]string, error) {
	rows, err := store.Query(ctx, q, `SELECT user_id, path FROM follows WHERE repo_id = ? AND (auto_until IS NULL OR auto_until > ?)`, repoID, store.Millis(time.Now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var uid, p string
		if err := rows.Scan(&uid, &p); err != nil {
			return nil, err
		}
		if p == page || p == "/" || (strings.HasSuffix(p, "/") && strings.HasPrefix(page, p)) {
			out = append(out, uid)
		}
	}
	return out, rows.Err()
}

// published tells followers of the revision's pages, and has its editors
// follow them for AutoFollowFor.
func (s *Service) published(ctx context.Context, rev revisions.Revision, n Notification, d Data, skip []string) error {
	files, err := revisions.Files(ctx, s.DB, rev.ID)
	if err != nil {
		return err
	}
	members, err := revisions.Members(ctx, s.DB, rev.ID)
	if err != nil {
		return err
	}
	already := map[string]bool{}
	for _, u := range skip {
		already[u] = true
	}
	byUser := map[string][]string{}
	var order []string
	until := store.Millis(time.Now().Add(AutoFollowFor))
	for _, f := range files {
		if !repos.IsMarkdown(f.Path) {
			continue
		}
		followers, err := Followers(ctx, s.DB, rev.RepoID, f.Path)
		if err != nil {
			return err
		}
		for _, uid := range followers {
			if already[uid] {
				continue
			}
			if _, ok := byUser[uid]; !ok {
				order = append(order, uid)
			}
			byUser[uid] = append(byUser[uid], f.Path)
		}
		if f.Op == revisions.OpDelete {
			continue
		}
		for _, m := range members {
			if _, err := store.Exec(ctx, s.DB, `INSERT INTO follows (user_id, repo_id, path, auto_until, created_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
				m.UserID, rev.RepoID, f.Path, until, store.Millis(time.Now())); err != nil {
				return err
			}
		}
	}
	for _, uid := range order {
		dd := d
		dd.Path = byUser[uid][0]
		dd.Of = len(byUser[uid])
		m := n
		m.Kind, m.Data = KindPagePublished, mustJSON(dd)
		s.deliver(ctx, m, []string{uid})
	}
	return nil
}

// Read is when someone last read a page, at which version.
type Read struct {
	SHA string    `json:"sha"`
	At  time.Time `json:"at"`
}

// RecordRead notes that a user read a page at sha (the page's latest commit)
// and returns the previous read, if any.
func RecordRead(ctx context.Context, q store.Querier, userID, repoID, p, sha string) (*Read, error) {
	var prev Read
	var at int64
	err := store.QueryRow(ctx, q, `SELECT last_read_sha, last_read_at FROM page_reads WHERE user_id = ? AND repo_id = ? AND path = ?`, userID, repoID, p).Scan(&prev.SHA, &at)
	now := time.Now()
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = store.Exec(ctx, q, `INSERT INTO page_reads (user_id, repo_id, path, last_read_sha, last_read_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, userID, repoID, p, sha, store.Millis(now))
		return nil, err
	case err != nil:
		return nil, err
	}
	prev.At = store.FromMillis(at)
	if prev.SHA != sha || now.Sub(prev.At) > ReadDebounce {
		if _, err := store.Exec(ctx, q, `UPDATE page_reads SET last_read_sha = ?, last_read_at = ? WHERE user_id = ? AND repo_id = ? AND path = ?`, sha, store.Millis(now), userID, repoID, p); err != nil {
			return nil, err
		}
	}
	return &prev, nil
}

// FollowRoutes registers follow and page-read endpoints.
func (s *Service) FollowRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Require)
		r.Get("/me/follows", s.myFollows)
		r.Get("/repos/{repo}/follows", s.followState)
		r.Put("/repos/{repo}/follows", s.follow(true))
		r.Delete("/repos/{repo}/follows", s.follow(false))
		r.Post("/repos/{repo}/reads", s.read2)
		r.Get("/repos/{repo}/follows/updates", s.followUpdates)
	})
}

func (s *Service) repoFor(w http.ResponseWriter, r *http.Request) (repos.Repo, string, bool) {
	p, _ := auth.FromContext(r.Context())
	repo, err := repos.Get(r.Context(), s.DB, chi.URLParam(r, "repo"))
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return repo, "", false
	}
	role, err := access.Effective(r.Context(), s.DB, p.User, repo.ID)
	if err != nil || role == access.None {
		api.Error(w, r, api.ErrNotFound)
		return repo, "", false
	}
	return repo, p.User.ID, true
}

// Follow is a followed page or folder.
type Follow struct {
	RepoID string     `json:"repo_id"`
	Path   string     `json:"path"`
	Folder bool       `json:"folder"`
	Until  *time.Time `json:"auto_until,omitempty"`
}

func (s *Service) myFollows(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	rows, err := store.Query(r.Context(), s.DB, `SELECT repo_id, path, auto_until FROM follows WHERE user_id = ? AND (auto_until IS NULL OR auto_until > ?) ORDER BY repo_id, path`, p.User.ID, store.Millis(time.Now()))
	if err != nil {
		api.Error(w, r, err)
		return
	}
	defer rows.Close()
	out := []Follow{}
	for rows.Next() {
		var f Follow
		var until sql.NullInt64
		if err := rows.Scan(&f.RepoID, &f.Path, &until); err != nil {
			api.Error(w, r, err)
			return
		}
		f.Folder = strings.HasSuffix(f.Path, "/")
		f.Path = strings.TrimSuffix(f.Path, "/")
		f.Until = store.NullMillis(until)
		out = append(out, f)
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": out})
}

// followState says whether the caller follows a page (itself or a folder above it).
func (s *Service) followState(w http.ResponseWriter, r *http.Request) {
	repo, uid, ok := s.repoFor(w, r)
	if !ok {
		return
	}
	isFolder := r.URL.Query().Get("folder") == "true"
	page := followKey(r.URL.Query().Get("path"), isFolder)
	rows, err := store.Query(r.Context(), s.DB, `SELECT path, auto_until FROM follows WHERE user_id = ? AND repo_id = ? AND (auto_until IS NULL OR auto_until > ?)`, uid, repo.ID, store.Millis(time.Now()))
	if err != nil {
		api.Error(w, r, err)
		return
	}
	defer rows.Close()
	out := map[string]any{"following": false}
	for rows.Next() {
		var p string
		var until sql.NullInt64
		if err := rows.Scan(&p, &until); err != nil {
			api.Error(w, r, err)
			return
		}
		switch {
		case p == page && isFolder:
			out["following"], out["folder"] = true, strings.TrimSuffix(p, "/")
		case p == page:
			out["following"], out["page"] = true, true
			if until.Valid {
				out["auto_until"] = store.FromMillis(until.Int64)
			}
		case p == "/" || (strings.HasSuffix(p, "/") && strings.HasPrefix(page, p)):
			out["following"] = true
			out["folder"] = strings.TrimSuffix(p, "/")
		}
	}
	api.JSON(w, http.StatusOK, out)
}

func (s *Service) follow(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		repo, uid, ok := s.repoFor(w, r)
		if !ok {
			return
		}
		key := followKey(r.URL.Query().Get("path"), r.URL.Query().Get("folder") == "true")
		if key == "" || (!strings.HasSuffix(key, "/") && !repos.IsMarkdown(key)) {
			api.Error(w, r, api.Invalid("path", "Follow a page or a folder."))
			return
		}
		var err error
		if on {
			// A manual follow replaces an automatic one.
			_, err = store.Exec(r.Context(), s.DB, `INSERT INTO follows (user_id, repo_id, path, auto_until, created_at) VALUES (?, ?, ?, NULL, ?)
				ON CONFLICT (user_id, repo_id, path) DO UPDATE SET auto_until = NULL`, uid, repo.ID, key, store.Millis(time.Now()))
		} else {
			_, err = store.Exec(r.Context(), s.DB, `DELETE FROM follows WHERE user_id = ? AND repo_id = ? AND path = ?`, uid, repo.ID, key)
		}
		if err != nil {
			api.Error(w, r, err)
			return
		}
		s.followState(w, r)
	}
}

// read2 records a read of a page at its latest commit and returns the
// previous one (the page shows "Updated since your last visit" when they differ).
func (s *Service) read2(w http.ResponseWriter, r *http.Request) {
	repo, uid, ok := s.repoFor(w, r)
	if !ok {
		return
	}
	var in struct {
		Path string `json:"path"`
		SHA  string `json:"sha"`
	}
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	p := followKey(in.Path, false)
	if !repos.IsMarkdown(p) || len(in.SHA) < 7 || len(in.SHA) > 64 {
		api.Error(w, r, api.Invalid("path", "Record reads of a page at a commit."))
		return
	}
	prev, err := RecordRead(r.Context(), s.DB, uid, repo.ID, p, in.SHA)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"previous": prev})
}

// Update is a followed page that changed since the person last read it.
type Update struct {
	Path    string    `json:"path"`
	SHA     string    `json:"sha"`
	Title   string    `json:"title"`
	Author  string    `json:"author_name"`
	Date    time.Time `json:"date"`
	ReadAt  time.Time `json:"read_at"`
	ReadSHA string    `json:"read_sha"`
}

// followUpdates lists followed pages of the repository updated since the
// caller last read them (for the repo home).
func (s *Service) followUpdates(w http.ResponseWriter, r *http.Request) {
	repo, uid, ok := s.repoFor(w, r)
	if !ok {
		return
	}
	rows, err := store.Query(r.Context(), s.DB, `SELECT f.path, pr.last_read_sha, pr.last_read_at FROM follows f JOIN page_reads pr ON pr.user_id = f.user_id AND pr.repo_id = f.repo_id AND pr.path = f.path
		WHERE f.user_id = ? AND f.repo_id = ? AND (f.auto_until IS NULL OR f.auto_until > ?) LIMIT 100`, uid, repo.ID, store.Millis(time.Now()))
	if err != nil {
		api.Error(w, r, err)
		return
	}
	type row struct {
		path, sha string
		at        int64
	}
	var list []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.path, &x.sha, &x.at); err != nil {
			rows.Close()
			api.Error(w, r, err)
			return
		}
		list = append(list, x)
	}
	rows.Close()
	out := []Update{}
	if repo.HeadSHA != "" {
		m := s.Repos.Mirror(repo)
		for _, x := range list {
			log, err := m.Log(r.Context(), repo.HeadSHA, x.path, 1)
			if err != nil || len(log) == 0 || log[0].SHA == x.sha {
				continue
			}
			out = append(out, Update{Path: x.path, SHA: log[0].SHA, Title: log[0].Title, Author: log[0].AuthorName, Date: log[0].Date, ReadAt: store.FromMillis(x.at), ReadSHA: x.sha})
		}
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": out})
}
