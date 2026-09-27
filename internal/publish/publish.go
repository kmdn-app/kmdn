// Package publish turns an approved revision into one commit on the target
// branch, made by the kmdn bot and co-signed by the people whose work it
// contains, or a pull/merge request when the branch is protected.
// See docs/specs/06-git-and-forges.md#publishing and #attribution.
package publish

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/branches"
	"github.com/kmdn-app/kmdn/internal/docengine"
	"github.com/kmdn-app/kmdn/internal/forge"
	"github.com/kmdn-app/kmdn/internal/gitmirror"
	"github.com/kmdn-app/kmdn/internal/jobs"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/telemetry"
	"github.com/kmdn-app/kmdn/internal/users"
)

// JobPublish runs a publish (idempotent per revision).
const JobPublish = "revision.publish"

// Docs gives the revision's collaborative documents (collab.Hub).
type Docs interface {
	FlushRevision(ctx context.Context, revID string)
	StateOf(ctx context.Context, revID, p string) (docID string, state []byte, err error)
	// SaveBeforePublish commits unsaved work on the branch (Save all) as by;
	// the caller holds the branch lock.
	SaveBeforePublish(ctx context.Context, repo repos.Repo, rev revisions.Revision, by users.User) error
}

// Service publishes revisions.
type Service struct {
	DB        *store.DB
	Repos     *repos.Service
	Revisions *revisions.Service
	Docs      Docs
	Engine    *docengine.Engine
	Jobs      *jobs.Queue
	Branches  *branches.Service
	Log       *slog.Logger
	// SuggestedCommit is the review assistant's title and body, when current. Optional.
	SuggestedCommit func(ctx context.Context, rev revisions.Revision) (title, body string, ok bool)
}

// Person is a name and commit email.
type Person struct {
	UserID string `json:"user_id,omitempty"`
	Name   string `json:"name"`
	Email  string `json:"email"`
}

// Preview is the commit publishing would make.
type Preview struct {
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	CoAuthors []Person `json:"co_authors"`
	Reviewers []Person `json:"reviewers"`
	Assisted  bool     `json:"assisted"`
	Message   string   `json:"message"`
	Target    string   `json:"target_branch"`
	// Protected: the target branch is protected (kmdn still merges through
	// the forge's API; with required checks, auto-merge waits for them).
	Protected bool `json:"protected"`
	// RequiredReviews: approvals the forge's protection wants, which kmdn
	// can't give unless it's on the rule's bypass list.
	RequiredReviews int `json:"required_reviews"`
	// Blocked explains why it can't be published right now ("" when it can).
	Blocked string `json:"blocked,omitempty"`
}

// Register wires the publish job and pull/merge request webhooks.
func (s *Service) Register() {
	s.Jobs.Register(JobPublish, func(ctx context.Context, j jobs.Job) (any, error) {
		var p jobInput
		if err := j.Decode(&p); err != nil {
			return nil, jobs.Permanent(err)
		}
		sha, url, err := s.run(ctx, p)
		switch {
		case err != nil:
			telemetry.Publishes.WithLabelValues("failed").Inc()
		case url != "":
			telemetry.Publishes.WithLabelValues("pull_request").Inc()
		case sha != "":
			telemetry.Publishes.WithLabelValues("pushed").Inc()
		}
		return map[string]string{"sha": sha, "url": url}, err
	})
	s.Repos.OnChangeRequest = append(s.Repos.OnChangeRequest, s.onChangeRequest)
}

// contributions counts surviving content per user across the revision's
// documents, from Yjs client attribution. Server-seeded content (the base)
// isn't anyone's contribution; page deletions and renames count a little so
// their authors are credited.
func (s *Service) contributions(ctx context.Context, rev revisions.Revision) (map[string]int, bool, error) {
	files, err := revisions.Files(ctx, s.DB, rev.ID)
	if err != nil {
		return nil, false, err
	}
	out := map[string]int{}
	assisted := false
	for _, f := range files {
		if f.Op == revisions.OpDelete {
			continue
		}
		docID, state, err := s.Docs.StateOf(ctx, rev.ID, f.Path)
		if err != nil {
			return nil, false, err
		}
		if state == nil {
			continue
		}
		counts, err := s.Engine.YContributions(ctx, state)
		if err != nil {
			return nil, false, err
		}
		rows, err := store.Query(ctx, s.DB, `SELECT client_id, user_id, kind FROM ydoc_clients WHERE ydoc_id = ?`, docID)
		if err != nil {
			return nil, false, err
		}
		type owner struct{ user, kind string }
		clients := map[uint64]owner{}
		for rows.Next() {
			var id int64
			var o owner
			if err := rows.Scan(&id, &o.user, &o.kind); err != nil {
				rows.Close()
				return nil, false, err
			}
			clients[uint64(id)] = o
		}
		rows.Close()
		for client, n := range counts {
			o, ok := clients[client]
			if !ok || o.kind == "sync" || o.user == "" {
				continue
			}
			if o.kind == "assistant" {
				assisted = true
			}
			out[o.user] += n
		}
	}
	events, err := revisions.Events(ctx, s.DB, rev.ID)
	if err != nil {
		return nil, false, err
	}
	for _, e := range events {
		if e.ActorType == revisions.ActorUser && (e.Kind == "file_deleted" || e.Kind == "file_renamed" || e.Kind == "file_added" || e.Kind == "asset_added") {
			out[e.ActorID]++
		}
	}
	return out, assisted, nil
}

func wrap(text string, width int) string {
	var out []string
	for _, para := range strings.Split(strings.ReplaceAll(strings.TrimSpace(text), "\r\n", "\n"), "\n\n") {
		lines := strings.Split(para, "\n")
		// Keep lists and already-short lines as written.
		if len(lines) > 1 || strings.HasPrefix(para, "- ") || strings.HasPrefix(para, "* ") {
			out = append(out, para)
			continue
		}
		var b strings.Builder
		n := 0
		for _, w := range strings.Fields(para) {
			if n > 0 && n+1+utf8.RuneCountInString(w) > width {
				b.WriteString("\n")
				n = 0
			} else if n > 0 {
				b.WriteString(" ")
				n++
			}
			b.WriteString(w)
			n += utf8.RuneCountInString(w)
		}
		out = append(out, b.String())
	}
	return strings.Join(out, "\n\n")
}

func shortTitle(t string) string {
	t = strings.TrimSpace(strings.ReplaceAll(t, "\n", " "))
	if utf8.RuneCountInString(t) <= 72 {
		return t
	}
	r := []rune(t)
	return strings.TrimSpace(string(r[:71])) + "…"
}

// Preview builds the commit message and who's credited.
func (s *Service) Preview(ctx context.Context, repo repos.Repo, rev revisions.Revision, title, body string) (Preview, error) {
	s.Docs.FlushRevision(ctx, rev.ID)
	if title == "" && body == "" && s.SuggestedCommit != nil {
		if t, b, ok := s.SuggestedCommit(ctx, rev); ok {
			title, body = t, b
		}
	}
	if title == "" {
		title = rev.Title
	}
	if body == "" {
		body = rev.Description
	}
	p := Preview{Title: shortTitle(title), Body: wrap(body, 72), Target: repo.TargetBranch, CoAuthors: []Person{}, Reviewers: []Person{}}
	counts, assisted, err := s.contributions(ctx, rev)
	if err != nil {
		return p, err
	}
	p.Assisted = assisted
	type credit struct {
		u users.User
		n int
	}
	var credits []credit
	for id, n := range counts {
		if u, err := users.ByID(ctx, s.DB, id); err == nil {
			credits = append(credits, credit{u, n})
		}
	}
	sort.Slice(credits, func(i, j int) bool {
		if credits[i].n != credits[j].n {
			return credits[i].n > credits[j].n
		}
		return credits[i].u.Name < credits[j].u.Name
	})
	for _, c := range credits {
		p.CoAuthors = append(p.CoAuthors, Person{UserID: c.u.ID, Name: c.u.Name, Email: branches.CommitEmail(ctx, s.DB, c.u, repo.ForgeHostID)})
	}
	rs, err := revisions.Reviewers(ctx, s.DB, rev)
	if err != nil {
		return p, err
	}
	for _, r := range rs {
		if r.State != revisions.ApprovalApproved {
			continue
		}
		if u, err := users.ByID(ctx, s.DB, r.UserID); err == nil {
			p.Reviewers = append(p.Reviewers, Person{UserID: u.ID, Name: u.Name, Email: branches.CommitEmail(ctx, s.DB, u, repo.ForgeHostID)})
		}
	}
	var msg strings.Builder
	msg.WriteString(p.Title + "\n")
	if p.Body != "" {
		msg.WriteString("\n" + p.Body + "\n")
	}
	msg.WriteString("\nKmdn-Revision: " + s.Branches.RevisionURL(repo, rev) + "\n")
	for _, c := range p.CoAuthors {
		msg.WriteString("Co-authored-by: " + c.Name + " <" + c.Email + ">\n")
	}
	for _, r := range p.Reviewers {
		msg.WriteString("Reviewed-by: " + r.Name + " <" + r.Email + ">\n")
	}
	if assisted {
		msg.WriteString("Assisted-by: kmdn-assistant\n")
	}
	p.Message = msg.String()
	if a, err := s.Repos.Adapters.ForRepo(ctx, repo); err == nil && repo.ForgeKind != forge.KindGit {
		if prot, err := a.BranchProtection(ctx, repo.ForgeRepo(), repo.TargetBranch); err == nil {
			p.Protected, p.RequiredReviews = prot.Protected, prot.RequiredReviews
		}
	}
	p.Blocked = s.blocked(ctx, rev)
	return p, nil
}

func (s *Service) blocked(ctx context.Context, rev revisions.Revision) string {
	switch {
	case rev.State != revisions.Approved:
		return "not_approved"
	case rev.HasConflicts:
		return "conflicts"
	}
	if s.Revisions.PendingUpdates != nil {
		if pending, err := s.Revisions.PendingUpdates(ctx, rev); err == nil && pending {
			return "updates_pending"
		}
	}
	if n, err := s.pendingSuggestions(ctx, rev); err != nil || n > 0 {
		return "suggestions_pending"
	}
	return ""
}

// pendingSuggestions counts suggestions nobody accepted or rejected yet.
func (s *Service) pendingSuggestions(ctx context.Context, rev revisions.Revision) (int, error) {
	files, err := revisions.Files(ctx, s.DB, rev.ID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, f := range files {
		if f.Op == revisions.OpDelete || !repos.IsMarkdown(f.Path) {
			continue
		}
		_, state, err := s.Docs.StateOf(ctx, rev.ID, f.Path)
		if err != nil {
			return 0, err
		}
		if state == nil {
			continue
		}
		list, err := s.Engine.YSuggestions(ctx, state)
		if err != nil {
			return 0, err
		}
		n += len(list)
	}
	return n, nil
}

type jobInput struct {
	RevisionID string `json:"revision_id"`
	By         string `json:"by"`
	Title      string `json:"title"`
	Body       string `json:"body"`
}

// Publish checks the caller may publish and queues the publish job.
func (s *Service) Publish(ctx context.Context, repo repos.Repo, rev revisions.Revision, c revisions.Caller, title, body string) (string, error) {
	a, err := s.Revisions.AccessFor(ctx, rev, c)
	if err != nil {
		return "", err
	}
	if !a.CanPublish {
		return "", revisions.ErrForbidden
	}
	if b := s.blocked(ctx, rev); b != "" {
		return "", &revisions.ErrConflict{Code: b, Msg: blockedMessage(b)}
	}
	return s.Jobs.Enqueue(ctx, s.DB, JobPublish, jobInput{RevisionID: rev.ID, By: c.User.ID, Title: title, Body: body}, jobs.EnqueueOptions{Key: JobPublish + ":" + rev.ID, MaxAttempts: 3})
}

func blockedMessage(code string) string {
	switch code {
	case "not_approved":
		return "Every reviewer has to approve before publishing."
	case "conflicts":
		return "Resolve the conflicts before publishing."
	case "updates_pending":
		return "Apply the updates from Published first."
	case "suggestions_pending":
		return "Accept or reject the pending suggestions first."
	case "merge_commits_disabled":
		return "The repository doesn't allow merge commits. Allow them in its settings on the forge: kmdn keeps every saved commit in history."
	case "approvals_required":
		return "Branch protection on the forge requires approvals kmdn can't give. Add kmdn to the rule's bypass list, or merge the pull request on the forge."
	case "not_mergeable":
		return "The forge can't merge the pull request right now. Open it on the forge to see why."
	case "published_moved":
		return "Published changed some of these pages since the revision started. Apply the updates from Published first."
	}
	return "This revision can't be published right now."
}

// run is the publish job.
func (s *Service) run(ctx context.Context, in jobInput) (string, string, error) {
	rev, err := revisions.Get(ctx, s.DB, in.RevisionID)
	if err != nil {
		return "", "", jobs.Permanent(err)
	}
	if rev.State == revisions.Published {
		return rev.PublishedSHA, "", nil
	}
	if rev.State == revisions.Publishing && rev.ChangeRequestURL != "" {
		return "", rev.ChangeRequestURL, nil // waiting for the merge
	}
	if rev.State != revisions.Approved {
		return "", "", jobs.Permanent(errors.New("revision isn't approved"))
	}
	repo, err := repos.Get(ctx, s.DB, rev.RepoID)
	if err != nil {
		return "", "", jobs.Permanent(err)
	}
	by, err := users.ByID(ctx, s.DB, in.By)
	if err != nil {
		return "", "", jobs.Permanent(err)
	}
	role, err := access.Effective(ctx, s.DB, by, repo.ID)
	if err != nil {
		return "", "", err
	}
	if a, err := s.Revisions.AccessFor(ctx, rev, revisions.Caller{User: by, Role: role}); err != nil || !a.CanPublish {
		return "", "", jobs.Permanent(errors.New("not allowed to publish"))
	}
	adapter, err := s.Repos.Adapters.ForRepo(ctx, repo)
	if err != nil {
		return "", "", err
	}
	cred, err := adapter.Credential(ctx, repo.ForgeRepo())
	if err != nil {
		return "", "", err
	}
	m := s.Repos.Mirror(repo)
	if err := m.Fetch(ctx, cred); err != nil {
		return "", "", err
	}
	head, err := m.Head(ctx)
	if err != nil {
		return "", "", err
	}
	trailer := "Kmdn-Revision: " + s.Branches.RevisionURL(repo, rev)
	// Resume: a previous attempt may have pushed before crashing.
	if sha, ok, err := m.FindTrailer(ctx, rev.BaseSHA, head, trailer); err == nil && ok {
		return sha, "", s.finish(ctx, repo, rev, in.By, sha)
	}
	_, touched, err := s.Branches.Changes(ctx, rev)
	if err != nil {
		return "", "", err
	}
	// Published moved: fine unless it touched the revision's pages.
	if head != rev.BaseSHA {
		moved, err := m.ChangedPaths(ctx, rev.BaseSHA, head)
		if err != nil {
			return "", "", err
		}
		for _, p := range moved {
			if touched[p] {
				_ = s.Revisions.PublishStopped(ctx, rev.ID, "publish_blocked", map[string]any{"reason": "published_moved", "path": p})
				return "", "", jobs.Permanent(&revisions.ErrConflict{Code: "published_moved", Msg: blockedMessage("published_moved")})
			}
		}
	}

	unlock := s.Branches.Lock(rev.ID)
	defer unlock()
	// What gets merged is committed on the branch first.
	if err := s.Docs.SaveBeforePublish(ctx, repo, rev, by); err != nil {
		return "", "", err
	}
	if rev, err = revisions.Get(ctx, s.DB, rev.ID); err != nil {
		return "", "", err
	}
	if rev, err = s.Branches.Ensure(ctx, repo, rev); err != nil {
		return "", "", err
	}
	preview, err := s.Preview(ctx, repo, rev, in.Title, in.Body)
	if err != nil {
		return "", "", err
	}
	title, body, _ := strings.Cut(preview.Message, "\n")

	cr, ok := adapter.(forge.ChangeRequester)
	if !ok || rev.ChangeRequestRef == "" {
		// Plain git: kmdn writes the merge commit itself.
		sha, err := s.mergeLocally(ctx, repo, rev, head, preview.Message)
		if err != nil {
			return "", "", err
		}
		return sha, "", s.finish(ctx, repo, rev, in.By, sha)
	}
	if err := s.Branches.MarkReady(ctx, repo, rev); err != nil {
		return "", "", err
	}
	ref := forge.ChangeRequest{URL: rev.ChangeRequestURL, Ref: rev.ChangeRequestRef, Node: rev.ChangeRequestNode}
	res, err := cr.MergeChangeRequest(ctx, repo.ForgeRepo(), ref, forge.MergeInput{Title: title, Body: strings.TrimSpace(body), HeadSHA: rev.BranchSHA})
	if err != nil {
		for code, e := range map[string]error{"merge_commits_disabled": forge.ErrMergeCommitsDisabled, "approvals_required": forge.ErrApprovalsRequired, "not_mergeable": forge.ErrNotMergeable} {
			if errors.Is(err, e) {
				_ = s.Revisions.PublishStopped(ctx, rev.ID, "publish_blocked", map[string]any{"reason": code, "detail": err.Error()})
				return "", "", jobs.Permanent(&revisions.ErrConflict{Code: code, Msg: blockedMessage(code)})
			}
		}
		return "", "", err // forge down, head changed: retried
	}
	if res.Queued {
		// Required checks are running: the forge merges when they pass and
		// its webhook finishes the publish.
		if err := s.Revisions.MarkPublishing(ctx, rev.ID, in.By, rev.ChangeRequestURL, rev.ChangeRequestRef); err != nil {
			return "", "", err
		}
		_ = audit.Write(ctx, s.DB, audit.Entry{ActorType: "user", ActorID: in.By, Action: "revision.auto_merge_enabled", TargetType: "revision", TargetID: rev.ID, RepoID: repo.ID, Data: map[string]any{"url": rev.ChangeRequestURL}})
		return "", rev.ChangeRequestURL, nil
	}
	if err := s.finish(ctx, repo, rev, in.By, res.SHA); err != nil {
		return res.SHA, "", err
	}
	s.deleteBranch(ctx, repo, rev.Branch)
	return res.SHA, "", nil
}

// mergeLocally merges the revision's branch into the target branch in the
// mirror (plain git remotes have no pull requests): the tree is the target's
// head with the revision's changes, the parents the head and the branch tip.
func (s *Service) mergeLocally(ctx context.Context, repo repos.Repo, rev revisions.Revision, head, message string) (string, error) {
	_, cred, err := s.Branches.Forge(ctx, repo)
	if err != nil {
		return "", err
	}
	tip, err := s.Branches.Tip(ctx, repo, rev, cred)
	if err != nil {
		return "", err
	}
	changes, _, err := s.Branches.Changes(ctx, rev)
	if err != nil {
		return "", err
	}
	m := s.Repos.Mirror(repo)
	bot := s.Branches.Bot(ctx, repo)
	sha, err := m.Commit(ctx, gitmirror.CommitInput{From: head, Changes: changes, Parents: []string{head, tip}, Message: message, Author: bot})
	if err != nil {
		return "", err
	}
	if err := m.Push(ctx, cred, sha, repo.TargetBranch, head); err != nil {
		return "", err // the target moved while we built (ErrStale): the job retries on the new head
	}
	s.deleteBranch(ctx, repo, rev.Branch)
	return sha, nil
}

// deleteBranch removes a merged kmdn branch on the forge (best effort).
func (s *Service) deleteBranch(ctx context.Context, repo repos.Repo, branch string) {
	if !strings.HasPrefix(branch, "kmdn/") {
		return
	}
	if _, cred, err := s.Branches.Forge(ctx, repo); err == nil {
		if err := s.Repos.Mirror(repo).DeleteRemoteBranch(ctx, cred, branch); err != nil {
			s.Log.Warn("delete merged branch", "err", err, "branch", branch)
		}
	}
}

func (s *Service) finish(ctx context.Context, repo repos.Repo, rev revisions.Revision, by, sha string) error {
	if err := s.Revisions.MarkPublished(ctx, rev.ID, by, sha); err != nil {
		return err
	}
	_ = audit.Write(ctx, s.DB, audit.Entry{ActorType: "user", ActorID: by, Action: "revision.published", TargetType: "revision", TargetID: rev.ID, RepoID: repo.ID, Data: map[string]any{"sha": sha}})
	// The mirror, indexes and other revisions catch up with the new head.
	if _, err := s.Repos.EnqueueSync(ctx, repo.ID); err != nil {
		s.Log.Warn("sync after publish", "err", err, "repo", repo.ID)
	}
	return nil
}

// onChangeRequest follows a revision's pull/merge request: merged (by kmdn's
// publish or on the forge) publishes the revision; closed unmerged stops a
// publish, or is noted in the activity.
func (s *Service) onChangeRequest(ctx context.Context, repo repos.Repo, ev forge.ChangeRequestEvent) error {
	rev, err := revisions.ByChangeRequest(ctx, s.DB, repo.ID, ev.Ref)
	if errors.Is(err, store.ErrNotFound) {
		return nil // not ours
	}
	if err != nil {
		return err
	}
	if ev.Merged {
		if err := s.finish(ctx, repo, rev, "", ev.MergeSHA); err != nil {
			return err
		}
		s.deleteBranch(ctx, repo, ev.Head)
		return nil
	}
	if rev.State == revisions.Publishing {
		return s.Revisions.PublishStopped(ctx, rev.ID, "change_request_closed", map[string]any{"ref": ev.Ref})
	}
	if err := revisions.Record(ctx, s.DB, rev.ID, revisions.ActorSystem, "", "change_request_closed", map[string]any{"ref": ev.Ref}); err != nil {
		return err
	}
	s.Revisions.Notify(ctx, rev.ID, "change_request_closed")
	return nil
}
