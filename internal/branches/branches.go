// Package branches keeps each revision's branch and pull/merge request on
// the forge: a revision is the branch kmdn/<number>-<slug> with a draft pull
// request against the target branch, ready for review while In review.
// See docs/specs/06-git-and-forges.md#revision-branches.
package branches

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/kmdn-app/kmdn/internal/forge"
	"github.com/kmdn-app/kmdn/internal/gitmirror"
	"github.com/kmdn-app/kmdn/internal/jobs"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// JobSync brings a revision's branch and pull request in line with the revision.
const JobSync = "revision.branch"

// Service manages revision branches.
type Service struct {
	DB        *store.DB
	Repos     *repos.Service
	Revisions *revisions.Service
	Jobs      *jobs.Queue
	BaseURL   string // for Kmdn-Revision links
	DataDir   string // uploads
	Log       *slog.Logger

	locks sync.Map // revision id → *sync.Mutex
}

// Register wires the sync job.
func (s *Service) Register() {
	s.Jobs.Register(JobSync, func(ctx context.Context, j jobs.Job) (any, error) {
		var p struct {
			RevisionID string `json:"revision_id"`
		}
		if err := j.Decode(&p); err != nil {
			return nil, jobs.Permanent(err)
		}
		return nil, s.Sync(ctx, p.RevisionID)
	})
}

// Lock serializes work on one revision's branch (sync, saves, publish).
func (s *Service) Lock(revID string) func() {
	m, _ := s.locks.LoadOrStore(revID, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// wantDraft: the pull request is a draft while the revision is being edited.
func wantDraft(rev revisions.Revision) bool { return rev.State == revisions.Editing }

// Changed is the revisions.Changed hook: it queues a sync when the forge is
// behind (no branch yet, no pull request, or the wrong draft state).
func (s *Service) Changed(ctx context.Context, rev revisions.Revision) {
	switch rev.State {
	case revisions.Editing, revisions.InReview, revisions.Approved:
	default:
		return
	}
	behind := rev.Branch == "" || (rev.ChangeRequestRef != "" && rev.ChangeRequestDraft != wantDraft(rev))
	if !behind && rev.ChangeRequestRef == "" {
		if repo, err := repos.Get(ctx, s.DB, rev.RepoID); err == nil && repo.ForgeKind != forge.KindGit {
			behind = true
		}
	}
	if !behind {
		return
	}
	// The key names the state asked for, so a change while a sync runs
	// queues another one instead of being folded into the running job.
	key := fmt.Sprintf("%s:%s:%t", JobSync, rev.ID, wantDraft(rev))
	if _, err := s.Jobs.Enqueue(ctx, s.DB, JobSync, map[string]string{"revision_id": rev.ID}, jobs.EnqueueOptions{Key: key}); err != nil {
		s.Log.Error("queue branch sync", "err", err, "revision", rev.ID)
	}
}

// Sync makes sure the revision's branch and pull request exist and the pull
// request's draft state matches the revision's state.
func (s *Service) Sync(ctx context.Context, revID string) error {
	ctx, release, err := s.Revisions.Gate(ctx, revID)
	if err != nil {
		return err
	}
	defer release()
	unlock := s.Lock(revID)
	defer unlock()
	for range 3 {
		rev, err := revisions.Get(ctx, s.DB, revID)
		if err != nil {
			return jobs.Permanent(err)
		}
		switch rev.State {
		case revisions.Editing, revisions.InReview, revisions.Approved:
		default:
			return nil // publish and close handle the rest
		}
		repo, err := repos.Get(ctx, s.DB, rev.RepoID)
		if err != nil {
			return jobs.Permanent(err)
		}
		if rev, err = s.ensureLocked(ctx, repo, rev); err != nil {
			return err
		}
		if rev.ChangeRequestRef == "" || rev.ChangeRequestDraft == wantDraft(rev) {
			return nil
		}
		if err := s.setDraft(ctx, repo, rev, wantDraft(rev)); err != nil {
			return err
		}
		// The state may have moved meanwhile: go round again.
	}
	return nil
}

// Ensure makes sure the branch and pull request exist and returns the
// revision as stored afterwards. Callers hold Lock.
func (s *Service) Ensure(ctx context.Context, repo repos.Repo, rev revisions.Revision) (revisions.Revision, error) {
	return s.ensureLocked(ctx, repo, rev)
}

// Forge returns the repo's adapter and git credentials.
func (s *Service) Forge(ctx context.Context, repo repos.Repo) (forge.Adapter, *gitmirror.Credential, error) {
	a, err := s.Repos.Adapters.ForRepo(ctx, repo)
	if err != nil {
		return nil, nil, err
	}
	cred, err := a.Credential(ctx, repo.ForgeRepo())
	return a, cred, err
}

// LocalRef keeps the branch tip in the mirror (refs/kmdn/...), so kmdn's own
// commits survive gc between saves.
func LocalRef(rev revisions.Revision) string { return "refs/kmdn/revisions/" + rev.ID }

// Tip returns the branch tip as kmdn last pushed it, fetching it when the
// mirror lost it (restored from a backup, re-cloned).
func (s *Service) Tip(ctx context.Context, repo repos.Repo, rev revisions.Revision, cred *gitmirror.Credential) (string, error) {
	m := s.Repos.Mirror(repo)
	if m.HasCommit(ctx, rev.BranchSHA) {
		return rev.BranchSHA, nil
	}
	tip, err := m.FetchBranch(ctx, cred, rev.Branch, LocalRef(rev))
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", rev.Branch, err)
	}
	if tip != rev.BranchSHA {
		return "", fmt.Errorf("the branch %s changed outside kmdn", rev.Branch)
	}
	return tip, nil
}

func (s *Service) ensureLocked(ctx context.Context, repo repos.Repo, rev revisions.Revision) (revisions.Revision, error) {
	a, cred, err := s.Forge(ctx, repo)
	if err != nil {
		return rev, err
	}
	if rev.Branch == "" {
		if err := s.createBranch(ctx, repo, rev, cred); err != nil {
			return rev, err
		}
		if rev, err = revisions.Get(ctx, s.DB, rev.ID); err != nil {
			return rev, err
		}
	}
	cr, ok := a.(forge.ChangeRequester)
	if !ok || rev.ChangeRequestRef != "" {
		return rev, nil
	}
	req, found, err := cr.FindChangeRequest(ctx, repo.ForgeRepo(), rev.Branch)
	if err != nil {
		return rev, err
	}
	if !found {
		req, err = cr.OpenChangeRequest(ctx, repo.ForgeRepo(), forge.ChangeRequestInput{
			Head: rev.Branch, Base: repo.TargetBranch, Title: rev.Title, Body: s.prBody(repo, rev), Draft: wantDraft(rev),
		})
		if err != nil {
			return rev, err
		}
	}
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		if _, err := store.Exec(ctx, tx, `UPDATE revisions SET change_request_url = ?, change_request_ref = ?, change_request_node = ?, change_request_draft = ? WHERE id = ?`,
			req.URL, req.Ref, req.Node, req.Draft, rev.ID); err != nil {
			return err
		}
		return revisions.Record(ctx, tx, rev.ID, revisions.ActorSystem, "", "change_request_opened", map[string]any{"url": req.URL, "draft": req.Draft})
	})
	if err != nil {
		return rev, err
	}
	s.Revisions.Notify(ctx, rev.ID, "change_request_opened")
	return revisions.Get(ctx, s.DB, rev.ID)
}

// createBranch pushes kmdn/<number>-<slug> with an empty first commit (GitHub
// won't open a pull request without one).
func (s *Service) createBranch(ctx context.Context, repo repos.Repo, rev revisions.Revision, cred *gitmirror.Credential) error {
	m := s.Repos.Mirror(repo)
	if !m.HasCommit(ctx, rev.BaseSHA) {
		if err := m.Fetch(ctx, cred); err != nil {
			return err
		}
	}
	trailer := "Kmdn-Revision: " + s.RevisionURL(repo, rev)
	author := s.Bot(ctx, repo)
	if rev.CreatedBy != "" {
		if u, err := users.ByID(ctx, s.DB, rev.CreatedBy); err == nil {
			author = gitmirror.Identity{Name: u.Name, Email: CommitEmail(ctx, s.DB, u, repo.ForgeHostID)}
		}
	}
	start, err := m.Commit(ctx, gitmirror.CommitInput{
		From:    rev.BaseSHA,
		Message: fmt.Sprintf("Start revision #%d: %s\n\n%s\n", rev.Number, shortTitle(rev.Title, 50), trailer),
		Author:  author, Committer: s.Bot(ctx, repo),
	})
	if err != nil {
		return err
	}
	name := BranchName(rev)
	tip := start
	for attempt := 2; ; attempt++ {
		err = m.Push(ctx, cred, start, name, "")
		if err == nil {
			break
		}
		if !errors.Is(err, gitmirror.ErrStale) {
			return err
		}
		// The branch exists: ours from an attempt that didn't get recorded
		// (its history carries this revision's trailer), or someone else's.
		existing, ferr := m.FetchBranch(ctx, cred, name, LocalRef(rev))
		if ferr != nil {
			return ferr
		}
		if _, mine, _ := m.FindTrailer(ctx, rev.BaseSHA, existing, trailer); mine {
			tip = existing
			break
		}
		if attempt > 5 {
			return fmt.Errorf("the branch %s already exists", name)
		}
		name = fmt.Sprintf("%s-%d", BranchName(rev), attempt)
	}
	if err := m.KeepRef(ctx, LocalRef(rev), tip); err != nil {
		return err
	}
	return s.DB.InTx(ctx, func(tx *store.Tx) error {
		res, err := store.Exec(ctx, tx, `UPDATE revisions SET branch = ?, branch_sha = ?, branch_base_sha = ? WHERE id = ? AND branch = ''`, name, tip, rev.BaseSHA, rev.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		return revisions.Record(ctx, tx, rev.ID, revisions.ActorSystem, "", "branch_created", map[string]any{"branch": name})
	})
}

func (s *Service) setDraft(ctx context.Context, repo repos.Repo, rev revisions.Revision, draft bool) error {
	a, err := s.Repos.Adapters.ForRepo(ctx, repo)
	if err != nil {
		return err
	}
	cr, ok := a.(forge.ChangeRequester)
	if !ok {
		return nil
	}
	ref := forge.ChangeRequest{URL: rev.ChangeRequestURL, Ref: rev.ChangeRequestRef, Node: rev.ChangeRequestNode, Draft: rev.ChangeRequestDraft}
	if err := cr.SetDraft(ctx, repo.ForgeRepo(), ref, draft); err != nil {
		return err
	}
	if _, err := store.Exec(ctx, s.DB, `UPDATE revisions SET change_request_draft = ? WHERE id = ?`, draft, rev.ID); err != nil {
		return err
	}
	s.Revisions.Notify(ctx, rev.ID, "change_request_draft")
	return nil
}

// MarkReady takes the pull request out of draft now (publishing merges it).
// Callers hold Lock.
func (s *Service) MarkReady(ctx context.Context, repo repos.Repo, rev revisions.Revision) error {
	if rev.ChangeRequestRef == "" || !rev.ChangeRequestDraft {
		return nil
	}
	return s.setDraft(ctx, repo, rev, false)
}

func (s *Service) prBody(repo repos.Repo, rev revisions.Revision) string {
	var b strings.Builder
	if d := strings.TrimSpace(rev.Description); d != "" {
		b.WriteString(d + "\n\n---\n")
	}
	b.WriteString("Edited and reviewed in kmdn: " + s.RevisionURL(repo, rev) + "\n\n")
	b.WriteString("kmdn commits every **Save all** to this branch and merges it when the revision is published. Edit the pages in kmdn rather than pushing here.\n")
	return b.String()
}

// RevisionURL links back to the revision (the Kmdn-Revision trailer).
func (s *Service) RevisionURL(repo repos.Repo, rev revisions.Revision) string {
	return fmt.Sprintf("%s/%s/%s/revisions/%d", strings.TrimRight(s.BaseURL, "/"), url.PathEscape(repo.Owner), url.PathEscape(repo.Name), rev.Number)
}

// Bot is who commits for kmdn on this forge.
func (s *Service) Bot(ctx context.Context, repo repos.Repo) gitmirror.Identity {
	return BotIdentity(ctx, s.DB, s.BaseURL, repo)
}

// BotIdentity is who commits for kmdn on this forge: the GitHub App's bot,
// or kmdn@<host>.
func BotIdentity(ctx context.Context, db *store.DB, baseURL string, repo repos.Repo) gitmirror.Identity {
	host := "kmdn"
	if u, err := url.Parse(baseURL); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	if repo.ForgeKind == forge.KindGitHub {
		if h, err := repos.GetHost(ctx, db, repo.ForgeHostID); err == nil && h.AppSlug != "" && h.AppID != "" {
			return gitmirror.Identity{Name: h.AppSlug + "[bot]", Email: h.AppID + "+" + h.AppSlug + "[bot]@users.noreply.github.com"}
		}
	}
	return gitmirror.Identity{Name: "kmdn", Email: "kmdn@" + host}
}

// CommitEmail picks a user's commit address: the linked forge's noreply
// address (default), the account email, or a custom one.
func CommitEmail(ctx context.Context, db *store.DB, u users.User, hostID string) string {
	var mode, custom string
	_ = store.QueryRow(ctx, db, `SELECT commit_email_mode, COALESCE(commit_email_custom, '') FROM users WHERE id = ?`, u.ID).Scan(&mode, &custom)
	switch mode {
	case "custom":
		if custom != "" {
			return custom
		}
	case "forge_noreply", "":
		var noreply string
		_ = store.QueryRow(ctx, db, `SELECT COALESCE(noreply_email, '') FROM linked_accounts WHERE user_id = ? AND forge_host_id = ?`, u.ID, hostID).Scan(&noreply)
		if noreply != "" {
			return noreply
		}
	}
	return u.Email
}

// BranchName is kmdn/<number>-<slug of the title, up to 40 characters>.
func BranchName(rev revisions.Revision) string {
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

func shortTitle(t string, n int) string {
	t = strings.TrimSpace(strings.ReplaceAll(t, "\n", " "))
	if r := []rune(t); len(r) > n {
		return strings.TrimSpace(string(r[:n-1])) + "…"
	}
	return t
}

// Changes turns the revision's manifest and uploads into commit changes
// against its base, with the paths they touch.
func (s *Service) Changes(ctx context.Context, rev revisions.Revision) ([]gitmirror.Change, map[string]bool, error) {
	files, err := revisions.Files(ctx, s.DB, rev.ID)
	if err != nil {
		return nil, nil, err
	}
	assets, err := revisions.Assets(ctx, s.DB, rev.ID)
	if err != nil {
		return nil, nil, err
	}
	return s.SnapshotChanges(files, assets)
}

// SnapshotChanges builds a patch from captured data without reading live state.
func (s *Service) SnapshotChanges(files []revisions.File, assets []revisions.Asset) ([]gitmirror.Change, map[string]bool, error) {
	touched := map[string]bool{}
	var out []gitmirror.Change
	// A renamed source can be reused by an added page or another rename.
	for _, f := range files {
		touched[f.Path] = true
		switch f.Op {
		case revisions.OpDelete:
			out = append(out, gitmirror.Change{Path: f.Path, Delete: true})
		case revisions.OpRename:
			touched[f.FromPath] = true
			out = append(out, gitmirror.Change{Path: f.FromPath, Delete: true})
		}
	}
	for _, f := range files {
		if f.Op != revisions.OpDelete {
			out = append(out, gitmirror.Change{Path: f.Path, Content: []byte(f.ContentMD)})
		}
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

// ErrBranchMoved: someone pushed to the revision's branch outside kmdn.
var ErrBranchMoved = errors.New("the revision's branch changed outside kmdn")

// SaveRef retains a prepared save before its intent is persisted and pushed.
func SaveRef(revID string) string { return "refs/kmdn/saves/" + revID }

// PrepareSave writes an immutable commit and retains it locally. Callers hold
// Lock and have ensured the revision branch exists. It never pushes.
func (s *Service) PrepareSave(ctx context.Context, repo repos.Repo, rev revisions.Revision, changes []gitmirror.Change, message string, author gitmirror.Identity) (string, []byte, error) {
	_, cred, err := s.Forge(ctx, repo)
	if err != nil {
		return "", nil, err
	}
	tip, err := s.Tip(ctx, repo, rev, cred)
	if err != nil {
		return "", nil, err
	}
	m := s.Repos.Mirror(repo)
	if !m.HasCommit(ctx, rev.BaseSHA) {
		if err := m.Fetch(ctx, cred); err != nil {
			return "", nil, err
		}
	}
	parents := []string{tip}
	if rev.BaseSHA != rev.BranchBaseSHA {
		parents = append(parents, rev.BaseSHA)
	}
	sha, err := m.Commit(ctx, gitmirror.CommitInput{From: rev.BaseSHA, Changes: changes, Parents: parents, Message: message, Author: author, Committer: s.Bot(ctx, repo)})
	if err != nil {
		return "", nil, err
	}
	if err := m.KeepRef(ctx, SaveRef(rev.ID), sha); err != nil {
		return "", nil, err
	}
	objects, err := m.PackCommit(ctx, cred, sha, []string{tip, rev.BaseSHA})
	return sha, objects, err
}

// PushSave resumes only the exact prepared commit or its recorded parent.
// Metadata and the checkpoint are finalized together by the save journal.
func (s *Service) PushSave(ctx context.Context, repo repos.Repo, rev revisions.Revision, sha, prior string, objects []byte) error {
	_, cred, err := s.Forge(ctx, repo)
	if err != nil {
		return err
	}
	m := s.Repos.Mirror(repo)
	if !m.Exists() {
		if err := m.Init(ctx, cred); err != nil {
			return err
		}
	}
	remote, err := m.FetchBranch(ctx, cred, rev.Branch, "refs/kmdn/save-remote/"+rev.ID)
	if errors.Is(err, gitmirror.ErrNotFound) {
		return ErrBranchMoved
	}
	if err != nil {
		return err
	}
	switch remote {
	case sha:
		// A prior attempt pushed successfully. Never replace its commit.
	case prior:
		if !m.HasCommit(ctx, sha) {
			if err := m.RestoreCommitPack(ctx, cred, sha, objects); err != nil {
				return &revisions.ErrConflict{Code: "save_commit_missing", Msg: "The prepared save could not be recovered from its saved Git objects: " + err.Error()}
			}
			if err := m.KeepRef(ctx, SaveRef(rev.ID), sha); err != nil {
				return err
			}
		}
		if err := m.Push(ctx, cred, sha, rev.Branch, prior); err != nil {
			if errors.Is(err, gitmirror.ErrStale) {
				return ErrBranchMoved
			}
			return err
		}
	default:
		return ErrBranchMoved
	}
	return m.KeepRef(ctx, LocalRef(rev), sha)
}

// FinishSave releases temporary refs only after database finalization.
func (s *Service) FinishSave(ctx context.Context, repo repos.Repo, revID string) {
	for _, ref := range []string{SaveRef(revID), "refs/kmdn/save-remote/" + revID} {
		if err := s.Repos.Mirror(repo).DropRef(ctx, ref); err != nil {
			s.Log.Warn("remove completed save ref", "err", err, "ref", ref)
		}
	}
}
