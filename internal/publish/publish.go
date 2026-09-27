// Package publish turns an approved revision into one commit on the target
// branch, made by the kmdn bot and co-signed by the people whose work it
// contains, or a pull/merge request when the branch is protected.
// See docs/specs/06-git-and-forges.md#publishing and #attribution.
package publish

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/audit"
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
}

// Service publishes revisions.
type Service struct {
	DB        *store.DB
	Repos     *repos.Service
	Revisions *revisions.Service
	Docs      Docs
	Engine    *docengine.Engine
	Jobs      *jobs.Queue
	BaseURL   string // for Kmdn-Revision links
	DataDir   string // uploads
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
	// Protected: publishing opens a pull/merge request instead of pushing.
	Protected bool `json:"protected"`
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

func (s *Service) revisionURL(repo repos.Repo, rev revisions.Revision) string {
	return fmt.Sprintf("%s/%s/%s/revisions/%d", strings.TrimRight(s.BaseURL, "/"), url.PathEscape(repo.Owner), url.PathEscape(repo.Name), rev.Number)
}

// commitEmail picks a user's co-author address: the linked forge's noreply
// address (default), the account email, or a custom one.
func (s *Service) commitEmail(ctx context.Context, u users.User, hostID string) string {
	var mode, custom string
	_ = store.QueryRow(ctx, s.DB, `SELECT commit_email_mode, COALESCE(commit_email_custom, '') FROM users WHERE id = ?`, u.ID).Scan(&mode, &custom)
	switch mode {
	case "custom":
		if custom != "" {
			return custom
		}
	case "forge_noreply", "":
		var noreply string
		_ = store.QueryRow(ctx, s.DB, `SELECT COALESCE(noreply_email, '') FROM linked_accounts WHERE user_id = ? AND forge_host_id = ?`, u.ID, hostID).Scan(&noreply)
		if noreply != "" {
			return noreply
		}
	}
	return u.Email
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
		p.CoAuthors = append(p.CoAuthors, Person{UserID: c.u.ID, Name: c.u.Name, Email: s.commitEmail(ctx, c.u, repo.ForgeHostID)})
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
			p.Reviewers = append(p.Reviewers, Person{UserID: u.ID, Name: u.Name, Email: s.commitEmail(ctx, u, repo.ForgeHostID)})
		}
	}
	var msg strings.Builder
	msg.WriteString(p.Title + "\n")
	if p.Body != "" {
		msg.WriteString("\n" + p.Body + "\n")
	}
	msg.WriteString("\nKmdn-Revision: " + s.revisionURL(repo, rev) + "\n")
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
			p.Protected = prot.Protected
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
	case "published_moved":
		return "Published changed some of these pages since the revision started. Apply the updates from Published first."
	}
	return "This revision can't be published right now."
}

// botIdentity is who authors kmdn's commits on this forge.
func (s *Service) botIdentity(ctx context.Context, repo repos.Repo) gitmirror.Identity {
	host := "kmdn"
	if u, err := url.Parse(s.BaseURL); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	if repo.ForgeKind == forge.KindGitHub {
		if h, err := repos.GetHost(ctx, s.DB, repo.ForgeHostID); err == nil && h.AppSlug != "" && h.AppID != "" {
			return gitmirror.Identity{Name: h.AppSlug + "[bot]", Email: h.AppID + "+" + h.AppSlug + "[bot]@users.noreply.github.com"}
		}
	}
	return gitmirror.Identity{Name: "kmdn", Email: "kmdn@" + host}
}

func branchName(rev revisions.Revision) string {
	var b strings.Builder
	for _, r := range strings.ToLower(rev.Title) {
		if b.Len() >= 40 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "revision"
	}
	return fmt.Sprintf("kmdn/%d-%s", rev.Number, slug)
}

// changes turns the manifest and uploads into commit changes.
func (s *Service) changes(ctx context.Context, rev revisions.Revision) ([]gitmirror.Change, map[string]bool, error) {
	files, err := revisions.Files(ctx, s.DB, rev.ID)
	if err != nil {
		return nil, nil, err
	}
	touched := map[string]bool{}
	var out []gitmirror.Change
	for _, f := range files {
		touched[f.Path] = true
		switch f.Op {
		case revisions.OpDelete:
			out = append(out, gitmirror.Change{Path: f.Path, Delete: true})
		case revisions.OpRename:
			touched[f.FromPath] = true
			out = append(out, gitmirror.Change{Path: f.FromPath, Delete: true}, gitmirror.Change{Path: f.Path, Content: []byte(f.ContentMD)})
		default:
			out = append(out, gitmirror.Change{Path: f.Path, Content: []byte(f.ContentMD)})
		}
	}
	assets, err := revisions.Assets(ctx, s.DB, rev.ID)
	if err != nil {
		return nil, nil, err
	}
	for _, a := range assets {
		b, err := os.ReadFile(filepath.Join(s.DataDir, "uploads", a.SHA256[:2], a.SHA256))
		if err != nil {
			return nil, nil, fmt.Errorf("asset %s: %w", a.Path, err)
		}
		touched[a.Path] = true
		out = append(out, gitmirror.Change{Path: a.Path, Content: b})
	}
	return out, touched, nil
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
	trailer := "Kmdn-Revision: " + s.revisionURL(repo, rev)
	// Resume: a previous attempt may have pushed before crashing.
	if sha, ok, err := m.FindTrailer(ctx, rev.BaseSHA, head, trailer); err == nil && ok {
		return sha, "", s.finish(ctx, repo, rev, in.By, sha)
	}
	changes, touched, err := s.changes(ctx, rev)
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
	preview, err := s.Preview(ctx, repo, rev, in.Title, in.Body)
	if err != nil {
		return "", "", err
	}
	sha, err := m.BuildCommit(ctx, head, changes, preview.Message, s.botIdentity(ctx, repo))
	if err != nil {
		return "", "", err
	}
	prot := forge.Protection{}
	if repo.ForgeKind != forge.KindGit {
		if prot, err = adapter.BranchProtection(ctx, repo.ForgeRepo(), repo.TargetBranch); err != nil {
			return "", "", err
		}
	}
	if !prot.Protected {
		if err := m.Push(ctx, cred, sha, repo.TargetBranch, head); err != nil {
			if errors.Is(err, gitmirror.ErrStale) {
				return "", "", err // the branch moved while we built: the job retries on the new head
			}
			return "", "", err
		}
		return sha, "", s.finish(ctx, repo, rev, in.By, sha)
	}
	cr, ok := adapter.(forge.ChangeRequester)
	if !ok {
		return "", "", jobs.Permanent(errors.New("the target branch is protected and this forge can't open pull requests"))
	}
	branch := branchName(rev)
	if err := m.Push(ctx, cred, sha, branch, ""); err != nil && !errors.Is(err, gitmirror.ErrStale) {
		return "", "", err
	}
	body := preview.Body
	if body != "" {
		body += "\n\n"
	}
	body += "---\nReviewed and approved in kmdn: " + s.revisionURL(repo, rev) + "\n"
	if len(preview.Reviewers) > 0 {
		names := make([]string, len(preview.Reviewers))
		for i, r := range preview.Reviewers {
			names[i] = r.Name
		}
		body += "Approved by " + strings.Join(names, ", ") + ".\n"
	}
	req, err := cr.OpenChangeRequest(ctx, repo.ForgeRepo(), branch, repo.TargetBranch, preview.Title, body)
	if err != nil {
		return "", "", err
	}
	if err := s.Revisions.MarkPublishing(ctx, rev.ID, in.By, req.URL, req.Ref); err != nil {
		return "", "", err
	}
	_ = audit.Write(ctx, s.DB, audit.Entry{ActorType: "user", ActorID: in.By, Action: "revision.pull_request_opened", TargetType: "revision", TargetID: rev.ID, RepoID: repo.ID, Data: map[string]any{"url": req.URL}})
	return "", req.URL, nil
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

// onChangeRequest finishes a publish when kmdn's pull/merge request merges.
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
		if ev.Head != "" && strings.HasPrefix(ev.Head, "kmdn/") {
			if a, err := s.Repos.Adapters.ForRepo(ctx, repo); err == nil {
				if cred, err := a.Credential(ctx, repo.ForgeRepo()); err == nil {
					_ = s.Repos.Mirror(repo).DeleteRemoteBranch(ctx, cred, ev.Head)
				}
			}
		}
		return nil
	}
	return s.Revisions.PublishStopped(ctx, rev.ID, "change_request_closed", map[string]any{"ref": ev.Ref})
}
