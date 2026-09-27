package consistency

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/jobs"
	"github.com/kmdn-app/kmdn/internal/llm"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// Jobs.
const (
	JobIndex    = "consistency.index"
	JobRevision = "consistency.revision"
	JobScan     = "consistency.scan"
	JobSchedule = "consistency.schedule"
)

// ScopePublished is the repo scan's findings scope (a revision's is its id).
const ScopePublished = "published"

// Defaults (docs/specs/08-assistant.md#per-revision).
const (
	DefaultK = 8
	// DefaultConcurrency judges pairs a few at a time: scans of a large repo
	// run hundreds of calls, each a few seconds with a reasoning model.
	DefaultConcurrency = 4
	DefaultNear        = 0.78
	DefaultDuplicate   = 0.92
	// revisionMaxCalls caps LLM judgments per revision check.
	revisionMaxCalls = 40
	// RevisionDebounce: checks run this long after the last materialization.
	RevisionDebounce = 30 * time.Second
)

// Docs flushes live edits before reading a revision.
type Docs interface {
	FlushRevision(ctx context.Context, revID string)
}

// Service runs consistency checks.
type Service struct {
	DB        *store.DB
	LLM       *llm.Service
	Repos     *repos.Service
	Revisions *revisions.Service
	Jobs      *jobs.Queue
	Docs      Docs
	// Publish sends live events (realtime.Hub.Publish).
	Publish func(scope string, event map[string]any)
	// Brief posts a person's message to a revision's assistant thread
	// (Fix, Link instead, Start a revision to fix).
	Brief func(ctx context.Context, rev revisions.Revision, u users.User, text, path string) error
	Log   *slog.Logger

	// Tuning (zero: defaults).
	K         int
	Near      float32
	Duplicate float32
	// Concurrency is how many pairs are judged at once.
	Concurrency int
}

func (s *Service) concurrency() int {
	if s.Concurrency > 0 {
		return s.Concurrency
	}
	return DefaultConcurrency
}

func (s *Service) k() int {
	if s.K > 0 {
		return s.K
	}
	return DefaultK
}

func (s *Service) near() float32 {
	if s.Near > 0 {
		return s.Near
	}
	return DefaultNear
}

func (s *Service) dup() float32 {
	if s.Duplicate > 0 {
		return s.Duplicate
	}
	return DefaultDuplicate
}

// Available reports whether checks can run (a chat provider and an embeddings model).
func (s *Service) Available(ctx context.Context) bool { return s.LLM.EmbeddingsEnabled(ctx) }

// Register wires the jobs and the head-changed hook.
func (s *Service) Register() {
	decode := func(j jobs.Job) (map[string]string, error) {
		var p map[string]string
		if err := j.Decode(&p); err != nil {
			return nil, jobs.Permanent(err)
		}
		return p, nil
	}
	s.Jobs.Register(JobIndex, func(ctx context.Context, j jobs.Job) (any, error) {
		p, err := decode(j)
		if err != nil {
			return nil, err
		}
		return nil, s.Index(ctx, p["repo_id"])
	})
	s.Jobs.Register(JobRevision, func(ctx context.Context, j jobs.Job) (any, error) {
		p, err := decode(j)
		if err != nil {
			return nil, err
		}
		return nil, s.CheckRevision(ctx, p["revision_id"])
	})
	s.Jobs.Register(JobScan, func(ctx context.Context, j jobs.Job) (any, error) {
		p, err := decode(j)
		if err != nil {
			return nil, err
		}
		sc, err := s.Scan(ctx, p["repo_id"], p["by"])
		return sc, err
	})
	s.Jobs.Register(JobSchedule, func(ctx context.Context, _ jobs.Job) (any, error) {
		return nil, s.schedule(ctx)
	})
	s.Repos.OnHeadChanged = append(s.Repos.OnHeadChanged, func(ctx context.Context, r repos.Repo, _, _ string) error {
		if !s.Available(ctx) {
			return nil
		}
		_, err := s.Jobs.Enqueue(ctx, s.DB, JobIndex, map[string]string{"repo_id": r.ID}, jobs.EnqueueOptions{Key: JobIndex + ":" + r.ID, MaxAttempts: 3})
		return err
	})
}

// Index syncs a repo's published passages and embeds new ones.
func (s *Service) Index(ctx context.Context, repoID string) error {
	if !s.Available(ctx) {
		return nil
	}
	r, err := repos.Get(ctx, s.DB, repoID)
	if err != nil {
		return jobs.Permanent(err)
	}
	if _, err := s.IndexPublished(ctx, r); err != nil {
		return err
	}
	ps, err := s.published(ctx, r.ID)
	if err != nil {
		return err
	}
	_, err = s.vectors(ctx, r.ID, ps)
	return err
}

// RequestRevision queues a revision check after delay (deduplicated per revision).
func (s *Service) RequestRevision(ctx context.Context, revID string, delay time.Duration) {
	if !s.Available(ctx) {
		return
	}
	if _, err := s.Jobs.Enqueue(ctx, s.DB, JobRevision, map[string]string{"revision_id": revID}, jobs.EnqueueOptions{Key: JobRevision + ":" + revID, RunAt: time.Now().Add(delay), MaxAttempts: 2}); err != nil {
		s.Log.Error("queue consistency check", "err", err)
	}
}

// Pending reports whether a revision check or a repo scan is queued or running.
func (s *Service) Pending(ctx context.Context, kind, key string) bool {
	var n int
	_ = store.QueryRow(ctx, s.DB, `SELECT COUNT(*) FROM jobs WHERE kind = ? AND unique_key = ? AND status IN ('pending', 'running')`, kind, kind+":"+key).Scan(&n)
	return n > 0
}

type pair struct {
	a, b Passage
	sim  float32
}

// CheckRevision compares the passages a revision changes with the rest of
// the repo (and with each other) and records the findings.
func (s *Service) CheckRevision(ctx context.Context, revID string) error {
	if !s.Available(ctx) {
		return nil
	}
	rev, err := revisions.Get(ctx, s.DB, revID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if rev.State != revisions.Editing && rev.State != revisions.InReview && rev.State != revisions.Approved {
		return nil
	}
	repo, err := repos.Get(ctx, s.DB, rev.RepoID)
	if err != nil {
		return err
	}
	if s.Docs != nil {
		s.Docs.FlushRevision(ctx, rev.ID)
	}
	if _, err := s.IndexPublished(ctx, repo); err != nil {
		return err
	}
	pub, err := s.published(ctx, repo.ID)
	if err != nil {
		return err
	}
	files, err := revisions.Files(ctx, s.DB, rev.ID)
	if err != nil {
		return err
	}
	touched := map[string]bool{}
	var mine []Passage
	changed := map[int]bool{}
	for _, f := range files {
		touched[f.Path] = true
		if f.FromPath != "" {
			touched[f.FromPath] = true
		}
		if f.Op == revisions.OpDelete || !repos.IsMarkdown(f.Path) {
			continue
		}
		base := map[string]bool{}
		for _, p := range Chunk(f.Path, f.BaseMD) {
			base[p.Hash] = true
		}
		for _, p := range Chunk(f.Path, f.ContentMD) {
			if !base[p.Hash] {
				changed[len(mine)] = true
			}
			mine = append(mine, p)
		}
	}
	var cands []Passage
	for _, p := range pub {
		if !touched[p.Path] {
			cands = append(cands, p)
		}
	}
	cands = append(cands, mine...)
	var found []Finding
	var keep map[string]bool
	if len(changed) > 0 {
		vecs, err := s.vectors(ctx, repo.ID, cands)
		if err != nil {
			return err
		}
		flat := make(Flat, len(cands))
		for i, p := range cands {
			flat[i] = vecs[p.Hash]
		}
		offset := len(cands) - len(mine)
		seen := map[string]bool{}
		var pairs []pair
		for i := range mine {
			if !changed[i] {
				continue
			}
			a := mine[i]
			for _, h := range flat.Near(vecs[a.Hash], s.k(), s.near(), func(j int) bool { return cands[j].Path == a.Path || cands[j].Hash == a.Hash }) {
				b := cands[h.I]
				key := PairKey(a.Hash, b.Hash)
				if seen[key] {
					continue
				}
				seen[key] = true
				// Between two changed passages, A is the first in the revision.
				if h.I >= offset && changed[h.I-offset] && h.I-offset < i {
					pairs = append(pairs, pair{b, a, h.Sim})
				} else {
					pairs = append(pairs, pair{a, b, h.Sim})
				}
			}
		}
		d, err := s.decide(ctx, repo.ID, pairs, revisionMaxCalls)
		if err != nil {
			return err
		}
		found, keep = d.found, d.keep
	}
	if err := s.record(ctx, repo.ID, rev.ID, found, keep); err != nil {
		return err
	}
	if s.Publish != nil {
		s.Publish("revision:"+rev.ID, map[string]any{"type": "consistency", "revision": rev.ID})
	}
	return nil
}

// decision is the outcome of judging candidate pairs.
type decision struct {
	found  []Finding
	keep   map[string]bool // pair keys whose state stays as it is
	capped bool            // pairs were left undecided (cap, budget or errors)
	judged int             // judgments that succeeded, cached ones included
	failed int             // judgments that errored
	// lastErr is the latest judgment error.
	lastErr error
}

// decide turns candidate pairs into findings: near-identical pairs are
// duplicates outright, the rest are judged (cached verdicts are free) up to
// maxCalls model calls, several at a time. keep holds the pairs whose
// findings stay as they are: ignored ones and ones left undecided (capped
// reports the latter). Findings come out in similarity order.
func (s *Service) decide(ctx context.Context, repoID string, pairs []pair, maxCalls int) (d decision, err error) {
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].sim > pairs[j].sim })
	ignored, err := s.ignored(ctx, repoID)
	if err != nil {
		return d, err
	}
	d.keep = map[string]bool{}
	found := make([]*Finding, len(pairs))
	var todo []int
	for i, p := range pairs {
		key := PairKey(p.a.Hash, p.b.Hash)
		if ignored[key] {
			d.keep[key] = true
			continue
		}
		f := Finding{PairKey: key, A: side(p.a), B: side(p.b), Similarity: float64(p.sim)}
		if p.sim >= s.dup() {
			f.Kind = Duplicate
			f.Explanation = fmt.Sprintf("These passages are nearly the same (%.0f%% similar): one page could link to the other.", float64(p.sim)*100)
			found[i] = &f
			continue
		}
		todo = append(todo, i)
	}

	// Workers take pairs in order. A model call reserves one of maxCalls
	// before it starts and gives it back on a cache hit; a budget error stops
	// everyone.
	var (
		mu     sync.Mutex
		next   int
		calls  int
		stop   error
		wg     sync.WaitGroup
		budget *llm.ErrBudget
	)
	work := func() {
		defer wg.Done()
		for {
			mu.Lock()
			if next == len(todo) {
				mu.Unlock()
				return
			}
			i := todo[next]
			next++
			p := pairs[i]
			key := PairKey(p.a.Hash, p.b.Hash)
			if stop != nil || calls >= maxCalls {
				d.keep[key], d.capped = true, true
				mu.Unlock()
				continue
			}
			calls++
			mu.Unlock()

			j, called, err := s.judge(ctx, repoID, p.a, p.b)

			mu.Lock()
			if !called {
				calls--
			}
			switch {
			case errors.As(err, &budget):
				stop = err
				d.keep[key], d.capped = true, true
			case err != nil:
				s.Log.Warn("judge pair", "err", err, "a", p.a.Path, "b", p.b.Path)
				d.keep[key], d.capped = true, true
				d.failed, d.lastErr = d.failed+1, err
			default:
				d.judged++
				if j.Verdict == Contradiction || j.Verdict == Duplicate {
					found[i] = &Finding{PairKey: key, A: side(p.a), B: side(p.b), Similarity: float64(p.sim),
						Kind: j.Verdict, ClaimA: j.ClaimA, ClaimB: j.ClaimB, Explanation: j.Explanation}
				}
			}
			mu.Unlock()
		}
	}
	for range min(s.concurrency(), max(len(todo), 1)) {
		wg.Add(1)
		go work()
	}
	wg.Wait()
	for _, f := range found {
		if f != nil {
			d.found = append(d.found, *f)
		}
	}
	if stop != nil {
		return d, stop
	}
	return d, nil
}

func side(p Passage) Side {
	return Side{Path: p.Path, Slug: p.Slug, Heading: p.Heading, Line: p.Line, Text: p.Text}
}

// Scan is a repo scan's record.
type Scan struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"` // running | done | capped | error
	Passages    int        `json:"passages"`
	Candidates  int        `json:"candidates"`
	Judged      int        `json:"judged"`
	Found       int        `json:"found"`
	Error       string     `json:"error,omitempty"`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	RequestedBy string     `json:"-"`
}

// RequestScan queues a repo scan ("Run now", or the weekly schedule).
func (s *Service) RequestScan(ctx context.Context, repoID, by string) error {
	_, err := s.Jobs.Enqueue(ctx, s.DB, JobScan, map[string]string{"repo_id": repoID, "by": by}, jobs.EnqueueOptions{Key: JobScan + ":" + repoID, MaxAttempts: 1})
	return err
}

// Scan compares all published passages pairwise through the neighbour
// search, judges candidates up to the configured cap, and updates the
// repo's consistency report. Findings a complete pass no longer reproduces
// are closed; pairs left undecided by the cap keep their state, and the
// next scan picks them up (verdicts are cached).
func (s *Service) Scan(ctx context.Context, repoID, by string) (Scan, error) {
	sc := Scan{ID: ids.New("csc"), Status: "running", StartedAt: time.Now()}
	if !s.Available(ctx) {
		return sc, nil
	}
	r, err := repos.Get(ctx, s.DB, repoID)
	if err != nil {
		return sc, jobs.Permanent(err)
	}
	var byArg any
	if by != "" {
		byArg = by
	}
	if _, err := store.Exec(ctx, s.DB, `INSERT INTO consistency_scans (id, repo_id, status, requested_by, started_at) VALUES (?, ?, 'running', ?, ?)`, sc.ID, r.ID, byArg, store.Millis(sc.StartedAt)); err != nil {
		return sc, err
	}
	err = s.scan(ctx, r, &sc)
	if err != nil {
		sc.Status, sc.Error = "error", err.Error()
	}
	now := time.Now()
	sc.FinishedAt = &now
	if _, uerr := store.Exec(context.WithoutCancel(ctx), s.DB, `UPDATE consistency_scans SET status = ?, passages = ?, candidates = ?, judged = ?, found = ?, error = ?, finished_at = ? WHERE id = ?`,
		sc.Status, sc.Passages, sc.Candidates, sc.Judged, sc.Found, truncate(sc.Error, 1000), store.Millis(now), sc.ID); uerr != nil {
		return sc, uerr
	}
	if s.Publish != nil {
		s.Publish("repo:"+r.ID, map[string]any{"type": "consistency", "repo": r.ID})
	}
	var budget *llm.ErrBudget
	if errors.As(err, &budget) {
		return sc, nil // recorded; retrying won't help until the budget resets
	}
	return sc, err
}

func (s *Service) scan(ctx context.Context, r repos.Repo, sc *Scan) error {
	if _, err := s.IndexPublished(ctx, r); err != nil {
		return err
	}
	ps, err := s.published(ctx, r.ID)
	if err != nil {
		return err
	}
	sc.Passages = len(ps)
	vecs, err := s.vectors(ctx, r.ID, ps)
	if err != nil {
		return err
	}
	flat := make(Flat, len(ps))
	for i, p := range ps {
		flat[i] = vecs[p.Hash]
	}
	seen := map[string]bool{}
	var pairs []pair
	for i, a := range ps {
		for _, h := range flat.Near(flat[i], s.k(), s.near(), func(j int) bool { return ps[j].Path == a.Path || ps[j].Hash == a.Hash }) {
			b := ps[h.I]
			key := PairKey(a.Hash, b.Hash)
			if seen[key] {
				continue
			}
			seen[key] = true
			if b.Path < a.Path {
				pairs = append(pairs, pair{b, a, h.Sim})
			} else {
				pairs = append(pairs, pair{a, b, h.Sim})
			}
		}
	}
	sc.Candidates = len(pairs)
	st, err := s.LLM.Settings(ctx)
	if err != nil {
		return err
	}
	before := s.judgedCount(ctx, r.ID)
	d, err := s.decide(ctx, r.ID, pairs, st.Consistency.ScanMaxCalls)
	sc.Judged = s.judgedCount(ctx, r.ID) - before
	sc.Found = len(d.found)
	if err != nil {
		return err
	}
	sc.Status = "done"
	if d.capped {
		sc.Status = "capped"
	}
	if err := s.record(ctx, r.ID, ScopePublished, d.found, d.keep); err != nil {
		return err
	}
	if d.failed > 0 {
		// A provider that fails every call (a wrong model name, a missing
		// permission) fails the scan, so the report says why.
		if d.judged == 0 {
			return fmt.Errorf("every judgment failed (%d pairs): %w", d.failed, d.lastErr)
		}
		sc.Error = fmt.Sprintf("%d judgments failed; the last one: %v", d.failed, d.lastErr)
	}
	// Vectors no page uses any more, kept a month for revisions and rollbacks.
	_, err = store.Exec(ctx, s.DB, `DELETE FROM passage_embeddings WHERE created_at < ? AND hash NOT IN (SELECT hash FROM passages)`, store.Millis(time.Now().Add(-30*24*time.Hour)))
	return err
}

func (s *Service) judgedCount(ctx context.Context, repoID string) int {
	var n int
	_ = store.QueryRow(ctx, s.DB, `SELECT COUNT(*) FROM consistency_judgments WHERE repo_id = ?`, repoID).Scan(&n)
	return n
}

// schedule queues scans that are due (hourly).
func (s *Service) schedule(ctx context.Context) error {
	if !s.Available(ctx) {
		return nil
	}
	st, err := s.LLM.Settings(ctx)
	if err != nil || st.Consistency.ScanEveryDays <= 0 {
		return err
	}
	list, err := repos.List(ctx, s.DB, nil, true)
	if err != nil {
		return err
	}
	due := time.Now().Add(-time.Duration(st.Consistency.ScanEveryDays) * 24 * time.Hour)
	for _, r := range list {
		if r.HeadSHA == "" {
			continue
		}
		var last int64
		_ = store.QueryRow(ctx, s.DB, `SELECT COALESCE(MAX(started_at), 0) FROM consistency_scans WHERE repo_id = ?`, r.ID).Scan(&last)
		if last > store.Millis(due) {
			continue
		}
		if err := s.RequestScan(ctx, r.ID, ""); err != nil {
			return err
		}
	}
	return nil
}
