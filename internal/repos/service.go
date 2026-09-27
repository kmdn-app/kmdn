package repos

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/forge"
	"github.com/kmdn-app/kmdn/internal/gitmirror"
	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/jobs"
	"github.com/kmdn-app/kmdn/internal/secrets"
	"github.com/kmdn-app/kmdn/internal/store"
)

// Job kinds.
const (
	JobSync = "repo.sync"
)

// HeadChanged is called after a sync moves the target branch head.
type HeadChanged func(ctx context.Context, r Repo, from, to string) error

// Service manages connected repositories.
type Service struct {
	DB       *store.DB
	Secrets  *secrets.Store
	Jobs     *jobs.Queue
	Git      *gitmirror.Git
	Adapters *Adapters
	DataDir  string
	BaseURL  string
	Log      *slog.Logger

	OnHeadChanged []HeadChanged
	// OnScopeChanged refreshes derived content after root or filter changes.
	OnScopeChanged []func(context.Context, Repo) error
	// OnChangeRequest runs when a pull/merge request is merged or closed.
	OnChangeRequest []ChangeRequestHook
}

// Register adds the service's job handlers.
func (s *Service) Register() {
	s.Jobs.Register(JobSync, func(ctx context.Context, j jobs.Job) (any, error) {
		var p struct {
			RepoID string `json:"repo_id"`
			Force  bool   `json:"force"`
		}
		if err := j.Decode(&p); err != nil {
			return nil, jobs.Permanent(err)
		}
		return nil, s.sync(ctx, p.RepoID, p.Force)
	})
}

// Mirror returns the bare mirror for r.
func (s *Service) Mirror(r Repo) *gitmirror.Mirror {
	url := r.CloneURL
	return &gitmirror.Mirror{Git: s.Git, Path: filepath.Join(s.DataDir, "mirrors", r.ForgeHostID, r.ID+".git"), URL: url, Branch: r.TargetBranch,
		Cred: func(ctx context.Context) (*gitmirror.Credential, error) { return s.credential(ctx, r) }}
}

// ConnectInput describes a repository to connect.
type ConnectInput struct {
	ForgeHostID  string   `json:"forge_host_id"`
	Owner        string   `json:"owner"`
	Name         string   `json:"name"`
	CloneURL     string   `json:"clone_url"` // plain git only
	Token        string   `json:"token"`     // GitLab access token or git token
	TargetBranch string   `json:"target_branch"`
	ContentRoot  string   `json:"content_root"`
	Include      []string `json:"include"`
	Exclude      []string `json:"exclude"`
	DisplayName  string   `json:"display_name"`
}

// ErrInvalid wraps user-fixable connection problems.
type ErrInvalid struct{ Field, Msg string }

func (e *ErrInvalid) Error() string { return e.Msg }

// Connect validates the repository with its forge, stores it and schedules the
// first sync. It returns the repo and the sync job id.
func (s *Service) Connect(ctx context.Context, by auth.Principal, in ConnectInput) (Repo, string, error) {
	h, err := GetHost(ctx, s.DB, in.ForgeHostID)
	if err != nil {
		return Repo{}, "", &ErrInvalid{"forge_host_id", "Pick a configured forge."}
	}
	fr := forge.Repo{Owner: in.Owner, Name: in.Name, CloneURL: in.CloneURL}
	var info forge.RepoInfo
	var adapter forge.Adapter
	switch h.Kind {
	case forge.KindGit:
		if in.CloneURL == "" {
			return Repo{}, "", &ErrInvalid{"clone_url", "Enter the repository's git URL."}
		}
		fr.Owner, fr.Name = forge.SplitCloneURL(in.CloneURL)
		adapter = &forge.PlainGit{Token: in.Token}
	case forge.KindGitLab:
		if in.Token == "" {
			return Repo{}, "", &ErrInvalid{"token", "Paste a project or group access token with api and write_repository scopes."}
		}
		adapter = &forge.GitLab{BaseURL: h.BaseURL, Token: in.Token, HTTP: s.Adapters.HTTP}
	case forge.KindGitHub:
		adapter, err = s.Adapters.GitHubApp(ctx, h)
		if err != nil {
			return Repo{}, "", err
		}
	}
	info, err = adapter.RepoInfo(ctx, fr)
	if err != nil {
		if errors.Is(err, forge.ErrNotFound) {
			return Repo{}, "", &ErrInvalid{"name", "kmdn can't see that repository. Check the name and that the app or token has access."}
		}
		// A refused token is the person's to fix, not a server error: the
		// usual cause is a token from another GitLab host, or an expired one.
		var refused *forge.APIError
		if errors.As(err, &refused) && (refused.Status == http.StatusUnauthorized || refused.Status == http.StatusForbidden) {
			if h.Kind == forge.KindGitHub {
				return Repo{}, "", &ErrInvalid{"name", "GitHub refused the app's access to that repository. Check the app is installed on it."}
			}
			return Repo{}, "", &ErrInvalid{"token", fmt.Sprintf("%s refused this token (%d). Check it was created on %s, hasn't expired or been revoked, and has the api and write_repository scopes.", hostOf(h.BaseURL), refused.Status, hostOf(h.BaseURL))}
		}
		return Repo{}, "", err
	}
	if info.ExternalID != "" {
		fr.ExternalID = info.ExternalID
	}
	if info.CloneURL != "" && h.Kind != forge.KindGit {
		fr.CloneURL = info.CloneURL
	}
	if info.Owner != "" {
		fr.Owner, fr.Name = info.Owner, info.Name
	}
	target := strings.TrimSpace(in.TargetBranch)
	if target == "" {
		target = info.DefaultBranch
	}
	if target == "" {
		target = "main"
	}
	display := strings.TrimSpace(in.DisplayName)
	if display == "" {
		display = fr.Owner + "/" + fr.Name
		if fr.Owner == "" {
			display = fr.Name
		}
	}
	var installID any
	if h.Kind == forge.KindGitHub {
		inst, err := adapter.(*forge.GitHubApp).RepoInstallation(ctx, fr)
		if err != nil {
			return Repo{}, "", fmt.Errorf("github installation: %w", err)
		}
		iid, err := UpsertInstall(ctx, s.DB, h.ID, inst)
		if err != nil {
			return Repo{}, "", err
		}
		installID = iid
	}
	id := ids.New(ids.Repo)
	var jobID string
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		var tokenRef, hookRef any
		if in.Token != "" {
			ref, err := s.Secrets.Put(ctx, tx, "repo_token", []byte(in.Token))
			if err != nil {
				return err
			}
			tokenRef = ref
		}
		hookSecret := auth.Token(24)
		ref, err := s.Secrets.Put(ctx, tx, "repo_webhook_secret", []byte(hookSecret))
		if err != nil {
			return err
		}
		hookRef = ref
		inc, exc := in.Include, in.Exclude
		if inc == nil {
			inc = []string{}
		}
		if exc == nil {
			exc = []string{}
		}
		_, err = store.Exec(ctx, tx, `INSERT INTO repos (id, forge_host_id, install_id, external_id, owner, name, display_name, clone_url, web_url, default_branch, target_branch,
			content_root, include_globs, exclude_globs, token_ref, webhook_secret_ref, created_by, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, h.ID, installID, fr.ExternalID, fr.Owner, fr.Name, display, fr.CloneURL, info.WebURL, info.DefaultBranch, target,
			NormalizeRoot(in.ContentRoot), mustJSON(inc), mustJSON(exc), tokenRef, hookRef, by.User.ID, store.Millis(time.Now()))
		if err != nil {
			if store.IsUniqueViolation(err) {
				return &ErrInvalid{"name", "This repository is already connected."}
			}
			return err
		}
		if !by.User.IsInstanceAdmin {
			if err := access.Grant(ctx, tx, id, access.UserPrincipal, by.User.ID, access.Admin); err != nil {
				return err
			}
		}
		if err := audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorUser, ActorID: by.User.ID, Action: "repo.connected", TargetType: "repo", TargetID: id, RepoID: id,
			Data: map[string]any{"forge": h.Kind, "slug": fr.Owner + "/" + fr.Name, "target_branch": target}}); err != nil {
			return err
		}
		jobID, err = s.Jobs.Enqueue(ctx, tx, JobSync, map[string]string{"repo_id": id}, jobs.EnqueueOptions{Key: id})
		return err
	})
	if err != nil {
		return Repo{}, "", err
	}
	s.Jobs.Notify()
	r, err := Get(ctx, s.DB, id)
	if err != nil {
		return r, jobID, err
	}
	if gl, ok := adapter.(*forge.GitLab); ok {
		secret, _ := s.webhookSecret(ctx, r)
		if err := gl.CreateWebhook(ctx, r.ForgeRepo(), strings.TrimRight(s.BaseURL, "/")+"/hooks/gitlab/"+r.ID, secret); err != nil {
			s.Log.Warn("gitlab webhook registration failed; kmdn will poll", "repo", r.Slug, "error", err)
		}
	}
	return r, jobID, nil
}

func (s *Service) webhookSecret(ctx context.Context, r Repo) (string, error) {
	if r.WebhookSecretRef == "" {
		return "", nil
	}
	b, err := s.Secrets.Get(ctx, s.DB, r.WebhookSecretRef)
	return string(b), err
}

// Sync fetches the mirror, reloads .kmdn.yml and branch protection, and
// updates health. It runs as a job.
func (s *Service) Sync(ctx context.Context, repoID string) error { return s.sync(ctx, repoID, false) }

// sync fetches the mirror; force also rechecks branch protection (a
// maintainer's "Refresh").
func (s *Service) sync(ctx context.Context, repoID string, force bool) error {
	r, err := Get(ctx, s.DB, repoID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return jobs.Permanent(err)
		}
		return err
	}
	adapter, err := s.Adapters.ForRepo(ctx, r)
	if err != nil {
		return s.setHealth(ctx, r, HealthDisconnected, err.Error())
	}
	cred, err := adapter.Credential(ctx, r.ForgeRepo())
	if err != nil {
		return s.setHealth(ctx, r, HealthDisconnected, "Can't get credentials from the forge: "+err.Error())
	}
	m := s.Mirror(r)
	if err := m.Init(ctx, cred); err != nil {
		_ = s.setHealth(ctx, r, HealthDegraded, "Fetching the repository failed: "+err.Error())
		return err
	}
	head, err := m.Head(ctx)
	if err != nil {
		return s.setHealth(ctx, r, HealthDegraded, fmt.Sprintf("Branch %q was not found in the repository.", r.TargetBranch))
	}
	kyml := KmdnYML{}
	if b, err := m.ReadFile(ctx, cred, head, ".kmdn.yml"); err == nil {
		kyml = ParseKmdnYML(b)
	}
	prot := r.Protection
	if force || r.LastFetchAt == nil || time.Since(*r.LastFetchAt) > 5*time.Minute || r.HeadSHA != head {
		if p, err := adapter.BranchProtection(ctx, r.ForgeRepo(), r.TargetBranch); err == nil {
			prot = p
		} else {
			s.Log.Warn("branch protection check failed", "repo", r.Slug, "error", err)
		}
	}
	now := store.Millis(time.Now())
	if _, err := store.Exec(ctx, s.DB, `UPDATE repos SET head_sha = ?, kmdn_yml = ?, protection = ?, protection_checked_at = ?, last_fetch_at = ?, health = ?, health_detail = '' WHERE id = ?`,
		head, mustJSON(kyml), mustJSON(prot), now, now, HealthOK, r.ID); err != nil {
		return err
	}
	if head != r.HeadSHA {
		r2, err := Get(ctx, s.DB, r.ID)
		if err != nil {
			return err
		}
		for _, f := range s.OnHeadChanged {
			if err := f(ctx, r2, r.HeadSHA, head); err != nil {
				s.Log.Error("head changed hook", "repo", r.Slug, "error", err)
			}
		}
	}
	return nil
}

func (s *Service) setHealth(ctx context.Context, r Repo, health, detail string) error {
	_, err := store.Exec(ctx, s.DB, `UPDATE repos SET health = ?, health_detail = ? WHERE id = ?`, health, detail, r.ID)
	return err
}

// EnqueueSync schedules a sync (deduplicated per repo).
func (s *Service) EnqueueSync(ctx context.Context, repoID string) (string, error) {
	return s.Jobs.Enqueue(ctx, s.DB, JobSync, map[string]any{"repo_id": repoID}, jobs.EnqueueOptions{Key: repoID})
}

// EnqueueFullSync schedules a sync that also rechecks branch protection.
func (s *Service) EnqueueFullSync(ctx context.Context, repoID string) (string, error) {
	return s.Jobs.Enqueue(ctx, s.DB, JobSync, map[string]any{"repo_id": repoID, "force": true}, jobs.EnqueueOptions{Key: repoID})
}

// credential returns git credentials for reads (lazy blob fetches).
func (s *Service) credential(ctx context.Context, r Repo) (*gitmirror.Credential, error) {
	a, err := s.Adapters.ForRepo(ctx, r)
	if err != nil {
		return nil, err
	}
	return a.Credential(ctx, r.ForgeRepo())
}

// Node is a file or folder in the content tree.
type Node struct {
	Path     string `json:"path"` // repo-relative
	Name     string `json:"name"`
	Type     string `json:"type"` // file | dir
	Markdown bool   `json:"markdown,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

// Tree lists files in scope (and their folders) at the published head.
func (s *Service) Tree(ctx context.Context, r Repo) ([]Node, error) {
	return s.TreeAt(ctx, r, r.HeadSHA)
}

// TreeAt lists files in scope at a commit (a revision's base).
func (s *Service) TreeAt(ctx context.Context, r Repo, sha string) ([]Node, error) {
	if sha == "" {
		return []Node{}, nil
	}
	sc := r.Scope()
	entries, err := s.Mirror(r).Tree(ctx, sha, sc.Root)
	if err != nil {
		if errors.Is(err, gitmirror.ErrNotFound) {
			return []Node{}, nil
		}
		return nil, err
	}
	dirs := map[string]bool{}
	var out []Node
	for _, e := range entries {
		if e.Type != "blob" || !sc.Contains(e.Path) {
			continue
		}
		out = append(out, Node{Path: e.Path, Name: path.Base(e.Path), Type: "file", Markdown: IsMarkdown(e.Path), Size: max(e.Size, 0)})
		for d := path.Dir(e.Path); d != "." && d != sc.Root && !dirs[d]; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	for d := range dirs {
		out = append(out, Node{Path: d, Name: path.Base(d), Type: "dir"})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	if out == nil {
		out = []Node{}
	}
	return out, nil
}

// ErrOutOfScope means the path isn't exposed by the repo's content settings.
var ErrOutOfScope = errors.New("repos: path outside the content root")

// File is a published file.
type File struct {
	Path      string `json:"path"`
	CommitSHA string `json:"commit_sha"`
	Content   string `json:"content"`
	Markdown  bool   `json:"markdown"`
	Size      int    `json:"size"`
}

// ReadFile returns a file in scope at rev (default: published head).
func (s *Service) ReadFile(ctx context.Context, r Repo, p, rev string) (File, error) {
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	if !r.Scope().Contains(p) {
		return File{}, ErrOutOfScope
	}
	if rev == "" {
		rev = r.HeadSHA
	}
	if rev == "" {
		return File{}, gitmirror.ErrNotFound
	}
	cred, err := s.credential(ctx, r)
	if err != nil {
		return File{}, err
	}
	b, err := s.Mirror(r).ReadFile(ctx, cred, rev, p)
	if err != nil {
		return File{}, err
	}
	return File{Path: p, CommitSHA: rev, Content: string(b), Markdown: IsMarkdown(p), Size: len(b)}, nil
}

// ReadConfigFile reads a file kmdn itself uses (style guides, .kmdn files),
// wherever it is, at the published head.
func (s *Service) ReadConfigFile(ctx context.Context, r Repo, p string) ([]byte, error) {
	if r.HeadSHA == "" {
		return nil, gitmirror.ErrNotFound
	}
	cred, err := s.credential(ctx, r)
	if err != nil {
		return nil, err
	}
	return s.Mirror(r).ReadFile(ctx, cred, r.HeadSHA, strings.TrimPrefix(path.Clean("/"+p), "/"))
}

// TemplatesDir holds page templates, outside the content root.
const TemplatesDir = ".kmdn/templates"

// Templates lists markdown templates at the published head.
func (s *Service) Templates(ctx context.Context, r Repo) ([]Node, error) {
	if r.HeadSHA == "" {
		return []Node{}, nil
	}
	entries, err := s.Mirror(r).Tree(ctx, r.HeadSHA, TemplatesDir)
	if err != nil {
		if errors.Is(err, gitmirror.ErrNotFound) {
			return []Node{}, nil
		}
		return nil, err
	}
	out := []Node{}
	for _, e := range entries {
		if e.Type == "blob" && IsMarkdown(e.Path) {
			out = append(out, Node{Path: e.Path, Name: path.Base(e.Path), Type: "file", Markdown: true, Size: max(e.Size, 0)})
		}
	}
	return out, nil
}

// ReadTemplate returns a template's markdown at the published head.
func (s *Service) ReadTemplate(ctx context.Context, r Repo, p string) (string, error) {
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	if !strings.HasPrefix(p, TemplatesDir+"/") || !IsMarkdown(p) || r.HeadSHA == "" {
		return "", gitmirror.ErrNotFound
	}
	cred, err := s.credential(ctx, r)
	if err != nil {
		return "", err
	}
	b, err := s.Mirror(r).ReadFile(ctx, cred, r.HeadSHA, p)
	return string(b), err
}

// History returns published versions of a file.
func (s *Service) History(ctx context.Context, r Repo, p string, n int) ([]gitmirror.Commit, error) {
	if !r.Scope().Contains(p) {
		return nil, ErrOutOfScope
	}
	if r.HeadSHA == "" {
		return []gitmirror.Commit{}, nil
	}
	return s.Mirror(r).Log(ctx, r.HeadSHA, p, n)
}

// Blame returns per-line attribution at the published head.
func (s *Service) Blame(ctx context.Context, r Repo, p string) ([]gitmirror.BlameLine, error) {
	if !r.Scope().Contains(p) {
		return nil, ErrOutOfScope
	}
	return s.Mirror(r).Blame(ctx, r.HeadSHA, p)
}

// UpdateInput changes repository settings.
type UpdateInput struct {
	DisplayName  *string   `json:"display_name"`
	TargetBranch *string   `json:"target_branch"`
	ContentRoot  *string   `json:"content_root"`
	Include      *[]string `json:"include"`
	Exclude      *[]string `json:"exclude"`
	Settings     *Settings `json:"settings"`
}

// Update applies settings changes; branch changes trigger a resync.
func (s *Service) Update(ctx context.Context, by auth.Principal, r Repo, in UpdateInput) (Repo, error) {
	set := []string{}
	args := []any{}
	resync := false
	if in.DisplayName != nil {
		n := strings.TrimSpace(*in.DisplayName)
		if n == "" {
			return r, &ErrInvalid{"display_name", "Enter a name."}
		}
		set, args = append(set, "display_name = ?"), append(args, n)
	}
	if in.TargetBranch != nil {
		b := strings.TrimSpace(*in.TargetBranch)
		if b == "" || strings.ContainsAny(b, " ~^:?*[\\") {
			return r, &ErrInvalid{"target_branch", "Enter a valid branch name."}
		}
		if b != r.TargetBranch {
			set, args = append(set, "target_branch = ?, head_sha = ''"), append(args, b)
			resync = true
		}
	}
	if in.ContentRoot != nil {
		set, args = append(set, "content_root = ?"), append(args, NormalizeRoot(*in.ContentRoot))
	}
	for _, g := range [][]string{deref(in.Include), deref(in.Exclude)} {
		for _, p := range g {
			if !validGlob(p) {
				return r, &ErrInvalid{"include", fmt.Sprintf("%q is not a valid glob.", p)}
			}
		}
	}
	if in.Include != nil {
		set, args = append(set, "include_globs = ?"), append(args, mustJSON(*in.Include))
	}
	if in.Exclude != nil {
		set, args = append(set, "exclude_globs = ?"), append(args, mustJSON(*in.Exclude))
	}
	if in.Settings != nil {
		set, args = append(set, "settings = ?"), append(args, mustJSON(*in.Settings))
	}
	if len(set) == 0 {
		return r, nil
	}
	args = append(args, r.ID)
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		if _, err := store.Exec(ctx, tx, `UPDATE repos SET `+strings.Join(set, ", ")+` WHERE id = ?`, args...); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorUser, ActorID: by.User.ID, Action: "repo.settings.updated", TargetType: "repo", TargetID: r.ID, RepoID: r.ID, Data: map[string]any{"fields": len(set)}})
	})
	if err != nil {
		return r, err
	}
	if resync {
		if _, err := s.EnqueueSync(ctx, r.ID); err != nil {
			return r, err
		}
	}
	updated, err := Get(ctx, s.DB, r.ID)
	if err != nil {
		return r, err
	}
	if in.ContentRoot != nil || in.Include != nil || in.Exclude != nil {
		for _, changed := range s.OnScopeChanged {
			if err := changed(ctx, updated); err != nil {
				return updated, err
			}
		}
	}
	return updated, nil
}

func validGlob(g string) bool { return doublestar.ValidatePattern(g) }

func removeAll(p string) error {
	if p == "" || p == "/" {
		return nil
	}
	return os.RemoveAll(p)
}

// UpsertInstall records a GitHub installation and returns its kmdn id.
func UpsertInstall(ctx context.Context, q store.Querier, hostID string, in forge.Install) (string, error) {
	var id string
	err := store.QueryRow(ctx, q, `SELECT id FROM forge_installs WHERE forge_host_id = ? AND external_id = ?`, hostID, in.ExternalID).Scan(&id)
	if err == nil {
		_, err = store.Exec(ctx, q, `UPDATE forge_installs SET account_login = ? WHERE id = ?`, in.AccountLogin, id)
		return id, err
	}
	id = ids.New("fi")
	_, err = store.Exec(ctx, q, `INSERT INTO forge_installs (id, forge_host_id, external_id, account_login, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, hostID, in.ExternalID, in.AccountLogin, store.Millis(time.Now()))
	return id, err
}

func deref(p *[]string) []string {
	if p == nil {
		return nil
	}
	return *p
}

// Disconnect removes a repository from kmdn (the forge repo is untouched).
func (s *Service) Disconnect(ctx context.Context, by auth.Principal, r Repo) error {
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		for _, ref := range []string{r.TokenRef, r.WebhookSecretRef} {
			if ref != "" {
				if err := s.Secrets.Delete(ctx, tx, ref); err != nil {
					return err
				}
			}
		}
		if _, err := store.Exec(ctx, tx, `DELETE FROM repos WHERE id = ?`, r.ID); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{ActorType: audit.ActorUser, ActorID: by.User.ID, Action: "repo.disconnected", TargetType: "repo", TargetID: r.ID, Data: map[string]any{"slug": r.Slug}})
	})
	if err != nil {
		return err
	}
	_ = removeAll(s.Mirror(r).Path)
	return nil
}

// hostOf is a base URL's host, for messages ("lab.example.com").
func hostOf(base string) string {
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		return u.Host
	}
	return base
}
