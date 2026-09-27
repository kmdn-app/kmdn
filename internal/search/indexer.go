package search

import (
	"context"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/jobs"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/store"
)

// JobIndex re-indexes a repo's published content.
const JobIndex = "search.index"

// Indexer keeps the published index in sync with repo heads.
type Indexer struct {
	Index
	Repos *repos.Service
	Jobs  *jobs.Queue
}

// Register wires the job and the head-changed hook.
func (ix *Indexer) Register() {
	ix.Jobs.Register(JobIndex, func(ctx context.Context, j jobs.Job) (any, error) {
		var p struct {
			RepoID string `json:"repo_id"`
		}
		if err := j.Decode(&p); err != nil {
			return nil, jobs.Permanent(err)
		}
		n, err := ix.Reindex(ctx, p.RepoID)
		return map[string]int{"changed": n}, err
	})
	enqueue := func(ctx context.Context, r repos.Repo) error {
		_, err := ix.Jobs.Enqueue(ctx, ix.DB, JobIndex, map[string]string{"repo_id": r.ID}, jobs.EnqueueOptions{Key: r.ID})
		return err
	}
	ix.Repos.OnScopeChanged = append(ix.Repos.OnScopeChanged, enqueue)
	ix.Repos.OnHeadChanged = append(ix.Repos.OnHeadChanged, func(ctx context.Context, r repos.Repo, _, _ string) error {
		return enqueue(ctx, r)
	})
}

// Reindex indexes changed markdown files and drops removed ones. It returns
// how many documents changed.
func (ix *Indexer) Reindex(ctx context.Context, repoID string) (int, error) {
	r, err := repos.Get(ctx, ix.DB, repoID)
	if err != nil {
		return 0, jobs.Permanent(err)
	}
	if r.HeadSHA == "" {
		return 0, nil
	}
	m := ix.Repos.Mirror(r)
	sc := r.Scope()
	entries, err := m.Tree(ctx, r.HeadSHA, sc.Root)
	if err != nil {
		return 0, err
	}
	have, err := ix.Indexed(ctx, r.ID, Published)
	if err != nil {
		return 0, err
	}
	want := map[string]string{} // path -> blob sha
	bySHA := map[string][]string{}
	var fetch []string
	for _, e := range entries {
		if e.Type != "blob" || !repos.IsMarkdown(e.Path) || !sc.Contains(e.Path) || e.Size > 2<<20 {
			continue
		}
		want[e.Path] = e.SHA
		if have[e.Path] != e.SHA {
			if len(bySHA[e.SHA]) == 0 {
				fetch = append(fetch, e.SHA)
			}
			bySHA[e.SHA] = append(bySHA[e.SHA], e.Path)
		}
	}
	changed := 0
	cred, err := ix.Repos.Adapters.ForRepo(ctx, r)
	if err != nil {
		return 0, err
	}
	c, err := cred.Credential(ctx, r.ForgeRepo())
	if err != nil {
		return 0, err
	}
	for start := 0; start < len(fetch); start += 200 {
		batch := fetch[start:min(start+200, len(fetch))]
		err := ix.DB.InTx(ctx, func(tx *store.Tx) error {
			return m.ReadBlobs(ctx, c, batch, func(sha string, content []byte) error {
				for _, p := range bySHA[sha] {
					name := strings.TrimSuffix(path.Base(p), path.Ext(p))
					if err := ix.Put(ctx, tx, r.ID, Published, p, sha, Extract(string(content), name)); err != nil {
						return err
					}
					changed++
				}
				return nil
			})
		})
		if err != nil {
			return changed, err
		}
	}
	for p := range have {
		if _, ok := want[p]; !ok {
			if err := ix.Delete(ctx, ix.DB, r.ID, Published, p); err != nil {
				return changed, err
			}
			changed++
		}
	}
	return changed, nil
}

// Routes registers GET /repos/{repo}/search.
func (ix *Indexer) Routes(rt chi.Router) {
	rt.With(auth.Require).Get("/repos/{repo}/search", func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.FromContext(r.Context())
		repoID := chi.URLParam(r, "repo")
		if role, err := access.Effective(r.Context(), ix.DB, p.User, repoID); err != nil || role == access.None {
			api.Error(w, r, api.ErrNotFound)
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		hits, err := ix.Search(r.Context(), repoID, Published, r.URL.Query().Get("q"), limit)
		if err != nil {
			api.Error(w, r, err)
			return
		}
		api.JSON(w, http.StatusOK, map[string]any{"items": hits})
	})
}
