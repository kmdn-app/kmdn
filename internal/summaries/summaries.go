// Package summaries writes the review assistant's summary card and
// suggested commit, and one-line summaries of published changes for
// readers (docs/specs/07-review.md#review-assistant).
package summaries

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kmdn-app/kmdn/internal/docengine"
	"github.com/kmdn-app/kmdn/internal/jobs"
	"github.com/kmdn-app/kmdn/internal/links"
	"github.com/kmdn-app/kmdn/internal/llm"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/textdiff"
)

// Jobs.
const (
	JobReview  = "summaries.review"
	JobChanges = "summaries.changes"
)

// Docs flushes live edits before reading a revision.
type Docs interface {
	FlushRevision(ctx context.Context, revID string)
}

// Service writes summaries.
type Service struct {
	DB        *store.DB
	LLM       *llm.Service
	Repos     *repos.Service
	Revisions *revisions.Service
	Links     *links.Service
	Engine    *docengine.Engine
	Docs      Docs
	Jobs      *jobs.Queue
	// Publish sends live events (realtime.Hub.Publish).
	Publish func(scope string, event map[string]any)
	// ThreadText returns the people's messages in the revision's assistant
	// thread (why the change is made). Optional.
	ThreadText func(ctx context.Context, revID string) string
	Log        *slog.Logger
}

// Review is a revision's summary card.
type Review struct {
	Summary     string    `json:"summary"`
	CommitTitle string    `json:"commit_title"`
	CommitBody  string    `json:"commit_body"`
	Findings    []Finding `json:"findings"` // the model's (style guide)
	Model       string    `json:"model,omitempty"`
	Error       string    `json:"error,omitempty"`
	At          time.Time `json:"created_at"`
	Stale       bool      `json:"stale"`
}

// Register wires the jobs.
func (s *Service) Register() {
	s.Jobs.Register(JobReview, func(ctx context.Context, j jobs.Job) (any, error) {
		var in struct {
			RevisionID string `json:"revision_id"`
			By         string `json:"by"`
		}
		if err := j.Decode(&in); err != nil {
			return nil, jobs.Permanent(err)
		}
		return nil, s.WriteReview(ctx, in.RevisionID, in.By)
	})
	s.Jobs.Register(JobChanges, func(ctx context.Context, j jobs.Job) (any, error) {
		var in struct {
			RevisionID string `json:"revision_id"`
		}
		if err := j.Decode(&in); err != nil {
			return nil, jobs.Permanent(err)
		}
		return nil, s.WriteChanges(ctx, in.RevisionID)
	})
}

// RequestReview queues a summary (at submit, or on request).
func (s *Service) RequestReview(ctx context.Context, revID, by string) {
	if rev, err := revisions.Get(ctx, s.DB, revID); err != nil || !s.LLM.EnabledForRepo(ctx, rev.RepoID) {
		return
	}
	if _, err := s.Jobs.Enqueue(ctx, s.DB, JobReview, map[string]string{"revision_id": revID, "by": by}, jobs.EnqueueOptions{Key: JobReview + ":" + revID, MaxAttempts: 2}); err != nil {
		s.Log.Error("queue review summary", "err", err)
	}
}

// Pending reports whether a summary is being written.
func (s *Service) Pending(ctx context.Context, revID string) bool {
	var n int
	_ = store.QueryRow(ctx, s.DB, `SELECT COUNT(*) FROM jobs WHERE kind = ? AND unique_key = ? AND status IN ('pending', 'running')`, JobReview, JobReview+":"+revID).Scan(&n)
	return n > 0
}

// GetReview loads a revision's summary (nil when there's none).
func (s *Service) GetReview(ctx context.Context, rev revisions.Revision) (*Review, error) {
	var r Review
	var hash, findings string
	var at int64
	err := store.QueryRow(ctx, s.DB, `SELECT content_hash, summary, commit_title, commit_body, findings, model, error, created_at FROM review_summaries WHERE revision_id = ?`, rev.ID).
		Scan(&hash, &r.Summary, &r.CommitTitle, &r.CommitBody, &findings, &r.Model, &r.Error, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(findings), &r.Findings)
	if r.Findings == nil {
		r.Findings = []Finding{}
	}
	r.At = store.FromMillis(at)
	if cur, err := revisions.ContentHash(ctx, s.DB, rev.ID); err == nil {
		r.Stale = cur != hash
	}
	return &r, nil
}

// SuggestedCommit returns the summary's commit title and body when it's up to date.
func (s *Service) SuggestedCommit(ctx context.Context, rev revisions.Revision) (string, string, bool) {
	r, err := s.GetReview(ctx, rev)
	if err != nil || r == nil || r.Stale || r.CommitTitle == "" {
		return "", "", false
	}
	return r.CommitTitle, r.CommitBody, true
}

// maxDiff bounds the diff sent to the model.
const maxDiff = 40_000

// diffText renders the revision's changes as unified hunks per page.
func (s *Service) diffText(ctx context.Context, rev revisions.Revision) (string, []string, error) {
	files, err := revisions.Files(ctx, s.DB, rev.ID)
	if err != nil {
		return "", nil, err
	}
	var b strings.Builder
	var paths []string
	for _, f := range files {
		if !repos.IsMarkdown(f.Path) {
			continue
		}
		paths = append(paths, f.Path)
		switch f.Op {
		case revisions.OpDelete:
			fmt.Fprintf(&b, "### %s (deleted)\n\n", f.Path)
			continue
		case revisions.OpRename:
			fmt.Fprintf(&b, "### %s (renamed from %s)\n", f.Path, f.FromPath)
		case revisions.OpAdd:
			fmt.Fprintf(&b, "### %s (new page)\n", f.Path)
		default:
			fmt.Fprintf(&b, "### %s\n", f.Path)
		}
		for _, h := range textdiff.Hunks(f.BaseMD, f.ContentMD, 2) {
			fmt.Fprintf(&b, "@@ -%d +%d @@\n", h.OldStart, h.NewStart)
			for _, l := range h.Lines {
				fmt.Fprintf(&b, "%s%s\n", l.Op, l.Text)
			}
		}
		b.WriteString("\n")
		if b.Len() > maxDiff {
			break
		}
	}
	d := b.String()
	if len(d) > maxDiff {
		d = d[:maxDiff] + "\n… (diff truncated)"
	}
	return d, paths, nil
}

// styleGuide returns the repository's style guide (STYLE.md or .kmdn/style.md), if any.
func (s *Service) styleGuide(ctx context.Context, repo repos.Repo) string {
	for _, p := range []string{".kmdn/style.md", "STYLE.md", strings.Trim(repo.ContentRoot, "/") + "/STYLE.md"} {
		f, err := s.Repos.ReadConfigFile(ctx, repo, p)
		if err == nil && len(f) > 0 {
			if len(f) > 8000 {
				f = f[:8000]
			}
			return string(f)
		}
	}
	return ""
}

var reviewTool = llm.Tool{Name: "report_review", Description: "Report the review summary.", Schema: json.RawMessage(`{"type":"object","properties":{
"summary":{"type":"string","description":"One paragraph: what changed and why, for reviewers"},
"commit_title":{"type":"string","description":"Imperative, at most 72 characters"},
"commit_body":{"type":"string","description":"What changed and why, in a few sentences; no markdown headings"},
"style_issues":{"type":"array","items":{"type":"object","properties":{"path":{"type":"string"},"quote":{"type":"string","description":"The exact passage"},"message":{"type":"string"}},"required":["path","message"]}}},
"required":["summary","commit_title","commit_body"]}`)}

// WriteReview asks the model for the summary card and stores it.
func (s *Service) WriteReview(ctx context.Context, revID, by string) error {
	rev, err := revisions.Get(ctx, s.DB, revID)
	if err != nil {
		return jobs.Permanent(err)
	}
	repo, err := repos.Get(ctx, s.DB, rev.RepoID)
	if err != nil {
		return err
	}
	s.Docs.FlushRevision(ctx, rev.ID)
	hash, err := revisions.ContentHash(ctx, s.DB, rev.ID)
	if err != nil {
		return err
	}
	diff, _, err := s.diffText(ctx, rev)
	if err != nil {
		return err
	}
	var prompt strings.Builder
	fmt.Fprintf(&prompt, "Revision #%d “%s” in %s.\n", rev.Number, rev.Title, repo.Slug)
	if rev.Description != "" {
		fmt.Fprintf(&prompt, "Its description:\n%s\n", rev.Description)
	}
	if s.ThreadText != nil {
		if t := s.ThreadText(ctx, rev.ID); t != "" {
			fmt.Fprintf(&prompt, "\nWhat its editors asked the assistant (the intent):\n%s\n", t)
		}
	}
	fmt.Fprintf(&prompt, "\nThe changes, as a markdown diff per page:\n%s", diff)
	style := s.styleGuide(ctx, repo)
	system := []llm.System{{Text: "You help reviewers of documentation changes. Summarize what a revision changes and why in one plain paragraph, suggest a commit title and body, and list passages that break the style guide if one is given. Be factual; don't praise.", Cache: true}}
	if style != "" {
		system = append(system, llm.System{Text: "The repository's style guide:\n" + style})
	}
	res, err := s.LLM.Complete(ctx, llm.TaskReviewSummary, llm.Run{UserID: by, RepoID: repo.ID, RevisionID: rev.ID}, llm.ChatRequest{
		System: system, Messages: []llm.Message{llm.Text(llm.RoleUser, prompt.String())}, Tools: []llm.Tool{reviewTool}, ToolChoice: reviewTool.Name, MaxTokens: 2000,
	})
	var out struct {
		Summary     string `json:"summary"`
		CommitTitle string `json:"commit_title"`
		CommitBody  string `json:"commit_body"`
		Style       []struct {
			Path    string `json:"path"`
			Quote   string `json:"quote"`
			Message string `json:"message"`
		} `json:"style_issues"`
	}
	errText := ""
	if err != nil {
		var be *llm.ErrBudget
		if !errors.As(err, &be) {
			s.Log.Warn("review summary", "err", err, "revision", rev.ID)
		}
		errText = "The assistant couldn't write a summary."
	} else {
		for _, b := range res.Content {
			if b.Type == llm.BlockToolUse && b.Name == reviewTool.Name {
				_ = json.Unmarshal(b.Input, &out)
			}
		}
		if out.Summary == "" {
			errText = "The assistant didn't return a summary."
		}
	}
	findings := []Finding{}
	for _, st := range out.Style {
		findings = append(findings, Finding{Kind: "style", Path: st.Path, Quote: st.Quote, Message: st.Message})
	}
	fj, _ := json.Marshal(findings)
	title := strings.TrimSpace(out.CommitTitle)
	if r := []rune(title); len(r) > 72 {
		title = string(r[:71]) + "…"
	}
	model := ""
	if _, m, err := s.LLM.For(ctx, llm.TaskReviewSummary); err == nil {
		model = m
	}
	if _, err := store.Exec(ctx, s.DB, `INSERT INTO review_summaries (revision_id, content_hash, summary, commit_title, commit_body, findings, model, error, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (revision_id) DO UPDATE SET content_hash = excluded.content_hash, summary = excluded.summary, commit_title = excluded.commit_title, commit_body = excluded.commit_body,
		findings = excluded.findings, model = excluded.model, error = excluded.error, created_at = excluded.created_at`,
		rev.ID, hash, strings.TrimSpace(out.Summary), title, strings.TrimSpace(out.CommitBody), string(fj), model, errText, store.Millis(time.Now())); err != nil {
		return err
	}
	if s.Publish != nil {
		s.Publish("revision:"+rev.ID, map[string]any{"type": "review_summary", "revision": rev.ID})
	}
	return nil
}

// RequestChanges queues change summaries for a published revision.
func (s *Service) RequestChanges(ctx context.Context, rev revisions.Revision) {
	if !s.LLM.EnabledForRepo(ctx, rev.RepoID) || rev.PublishedSHA == "" {
		return
	}
	if _, err := s.Jobs.Enqueue(ctx, s.DB, JobChanges, map[string]string{"revision_id": rev.ID}, jobs.EnqueueOptions{Key: JobChanges + ":" + rev.ID, MaxAttempts: 2}); err != nil {
		s.Log.Error("queue change summaries", "err", err)
	}
}

// WriteChanges writes a one-sentence summary per page a published revision
// changed, for "Updated since your last visit" and followers.
func (s *Service) WriteChanges(ctx context.Context, revID string) error {
	rev, err := revisions.Get(ctx, s.DB, revID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && rev.PublishedSHA == "") {
		return nil // gone or not published: nothing to summarize
	}
	if err != nil {
		return err
	}
	files, err := revisions.Files(ctx, s.DB, rev.ID)
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.Op == revisions.OpDelete || !repos.IsMarkdown(f.Path) {
			continue
		}
		var diff strings.Builder
		for _, h := range textdiff.Hunks(f.BaseMD, f.ContentMD, 1) {
			for _, l := range h.Lines {
				fmt.Fprintf(&diff, "%s%s\n", l.Op, l.Text)
			}
			if diff.Len() > 12_000 {
				break
			}
		}
		res, err := s.LLM.Complete(ctx, llm.TaskShortText, llm.Run{RepoID: rev.RepoID, RevisionID: rev.ID}, llm.ChatRequest{
			System:    []llm.System{{Text: "Summarize for readers, in one or two short sentences, what changed in a documentation page. Say what's different, not that it was edited. No preamble.", Cache: true}},
			Messages:  []llm.Message{llm.Text(llm.RoleUser, fmt.Sprintf("Page %s (revision “%s”). The change:\n%s", f.Path, rev.Title, truncate(diff.String(), 12_000)))},
			MaxTokens: 200,
		})
		if err != nil {
			return err
		}
		sum := strings.TrimSpace(res.TextOf())
		if sum == "" {
			continue
		}
		if _, err := store.Exec(ctx, s.DB, `INSERT INTO change_summaries (repo_id, commit_sha, path, summary, created_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
			rev.RepoID, rev.PublishedSHA, f.Path, truncate(sum, 500), store.Millis(time.Now())); err != nil {
			return err
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ChangeSummaries returns the summaries of a page's published changes by commit.
func ChangeSummaries(ctx context.Context, q store.Querier, repoID, p string) (map[string]string, error) {
	rows, err := store.Query(ctx, q, `SELECT commit_sha, summary FROM change_summaries WHERE repo_id = ? AND path = ? ORDER BY created_at DESC LIMIT 50`, repoID, p)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var sha, sum string
		if err := rows.Scan(&sha, &sum); err != nil {
			return nil, err
		}
		out[sha] = sum
	}
	return out, rows.Err()
}
