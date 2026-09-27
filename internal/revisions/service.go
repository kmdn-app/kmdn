package revisions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/gitmirror"
	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/textdiff"
	"github.com/kmdn-app/kmdn/internal/users"
)

// Limits (docs/specs/05-collaboration.md#limits).
const (
	MaxFiles    = 200
	MaxTitle    = 200
	MaxFileSize = 5 << 20
	reopenFor   = 90 * 24 * time.Hour
)

// Service runs revision operations.
type Service struct {
	DB    *store.DB
	Repos *repos.Service
	Log   *slog.Logger
	// DataDir holds uploads (<data>/uploads/<sha[:2]>/<sha>).
	DataDir string
	// UploadMaxMB caps uploads; repo settings and .kmdn.yml can only lower it.
	UploadMaxMB int

	// Changed is called after a revision or its manifest changes (realtime
	// fan-out and room mode switches hook in here). Optional.
	Changed func(ctx context.Context, rev Revision, kind string)
	// Removed is called when a file's collaborative document goes away
	// (deleted add, revision closed). Optional.
	Removed func(ctx context.Context, rev Revision, path string)
}

func (s *Service) changed(ctx context.Context, revID, kind string) {
	if s.Changed == nil {
		return
	}
	if rev, err := Get(ctx, s.DB, revID); err == nil {
		s.Changed(ctx, rev, kind)
	}
}

// Caller is who is acting and their role on the revision's repository.
type Caller struct {
	User users.User
	Role access.Role
}

// Access is what a caller can do on a revision right now.
type Access struct {
	Member bool `json:"member"`
	// CanEdit: content and file operations.
	CanEdit bool `json:"can_edit"`
	// CanManage: title, description, members, close/reopen.
	CanManage bool `json:"can_manage"`
	// Reason explains read-only access: viewer, not_member, in_review, approved, publishing, published, closed.
	Reason string `json:"reason,omitempty"`
}

// AccessFor evaluates docs/specs/07-review.md#who-can-do-what-by-state for the
// states that exist so far. Assigned reviewers (M3) will add edit rights while
// In review.
func (s *Service) AccessFor(ctx context.Context, rev Revision, c Caller) (Access, error) {
	member, err := IsMember(ctx, s.DB, rev.ID, c.User.ID)
	if err != nil {
		return Access{}, err
	}
	maint := c.Role.AtLeast(access.Maintainer)
	a := Access{Member: member, CanManage: (member && c.Role.AtLeast(access.Contributor)) || maint}
	switch {
	case !c.Role.AtLeast(access.Contributor):
		a.Reason = "viewer"
	case rev.State != Editing:
		a.Reason = string(rev.State)
	case member || maint:
		a.CanEdit = true
	default:
		a.Reason = "not_member"
	}
	if !rev.State.Open() {
		a.CanManage = a.CanManage && rev.State == Closed // reopen only
	}
	return a, nil
}

// CreateInput starts a revision.
type CreateInput struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	// Path, when set, adds that page to the revision right away (the first
	// keystroke in Published, or "New page" with Op add).
	Path     string `json:"path,omitempty"`
	Op       string `json:"op,omitempty"`       // modify (default) | add
	Template string `json:"template,omitempty"` // for Op add: .kmdn/templates/… path
}

// Create starts a revision on the repository's published head.
func (s *Service) Create(ctx context.Context, repo repos.Repo, c Caller, in CreateInput) (Revision, error) {
	if !c.Role.AtLeast(access.Contributor) {
		return Revision{}, ErrForbidden
	}
	if repo.HeadSHA == "" {
		return Revision{}, conflict("repo_not_synced", "The repository hasn't finished its first sync.")
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return Revision{}, invalid("title", "Give the revision a title.")
	}
	if utf8.RuneCountInString(in.Title) > MaxTitle {
		return Revision{}, invalid("title", fmt.Sprintf("Keep the title under %d characters.", MaxTitle))
	}
	var initial *FileOp
	if in.Path != "" {
		op := FileOp{Op: OpModify, Path: in.Path}
		if in.Op == OpAdd {
			op = FileOp{Op: OpAdd, Path: in.Path, Template: in.Template}
		}
		initial = &op
	}
	var rev Revision
	for attempt := 0; ; attempt++ {
		err := s.DB.InTx(ctx, func(tx *store.Tx) error {
			var next int
			if err := store.QueryRow(ctx, tx, `SELECT COALESCE(MAX(number), 0) + 1 FROM revisions WHERE repo_id = ?`, repo.ID).Scan(&next); err != nil {
				return err
			}
			now := store.Millis(time.Now())
			rev = Revision{ID: ids.New(ids.Revision), RepoID: repo.ID, Number: next, Title: in.Title, Description: strings.TrimSpace(in.Description),
				State: Editing, BaseSHA: repo.HeadSHA, CreatedBy: c.User.ID, CreatedAt: store.FromMillis(now), UpdatedAt: store.FromMillis(now)}
			if _, err := store.Exec(ctx, tx, `INSERT INTO revisions (id, repo_id, number, title, description, state, base_sha, created_by, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, rev.ID, rev.RepoID, rev.Number, rev.Title, rev.Description, rev.State, rev.BaseSHA, rev.CreatedBy, now, now); err != nil {
				return err
			}
			if _, err := store.Exec(ctx, tx, `INSERT INTO revision_members (revision_id, user_id, role, invited_by, added_at) VALUES (?, ?, ?, NULL, ?)`,
				rev.ID, c.User.ID, RoleOwner, now); err != nil {
				return err
			}
			if err := Record(ctx, tx, rev.ID, ActorUser, c.User.ID, "created", map[string]any{"title": rev.Title}); err != nil {
				return err
			}
			if initial != nil {
				if _, err := s.applyOp(ctx, tx, repo, rev, c, *initial); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil && store.IsUniqueViolation(err) && attempt < 3 {
			continue // two revisions raced for the same number
		}
		if err != nil {
			return Revision{}, err
		}
		break
	}
	s.changed(ctx, rev.ID, "created")
	return Get(ctx, s.DB, rev.ID)
}

// UpdateInput edits revision metadata.
type UpdateInput struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
}

// Update changes title and description.
func (s *Service) Update(ctx context.Context, rev Revision, c Caller, in UpdateInput) (Revision, error) {
	a, err := s.AccessFor(ctx, rev, c)
	if err != nil {
		return rev, err
	}
	if !a.CanManage || !rev.State.Open() {
		return rev, ErrForbidden
	}
	set, args := []string{}, []any{}
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if t == "" || utf8.RuneCountInString(t) > MaxTitle {
			return rev, invalid("title", fmt.Sprintf("Titles need 1 to %d characters.", MaxTitle))
		}
		set, args = append(set, "title = ?"), append(args, t)
	}
	if in.Description != nil {
		set, args = append(set, "description = ?"), append(args, strings.TrimSpace(*in.Description))
	}
	if len(set) == 0 {
		return rev, nil
	}
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		if _, err := store.Exec(ctx, tx, `UPDATE revisions SET `+strings.Join(set, ", ")+` WHERE id = ?`, append(args, rev.ID)...); err != nil {
			return err
		}
		return Record(ctx, tx, rev.ID, ActorUser, c.User.ID, "updated", nil)
	})
	if err != nil {
		return rev, err
	}
	s.changed(ctx, rev.ID, "updated")
	return Get(ctx, s.DB, rev.ID)
}

// Close stops work on a revision. It can be reopened for 90 days.
func (s *Service) Close(ctx context.Context, rev Revision, c Caller) (Revision, error) {
	a, err := s.AccessFor(ctx, rev, c)
	if err != nil {
		return rev, err
	}
	if !a.CanManage {
		return rev, ErrForbidden
	}
	if rev.State == Publishing || !rev.State.Open() {
		return rev, conflict("invalid_state", "Only open revisions that aren't publishing can be closed.")
	}
	return s.setState(ctx, rev, c, Closed, "closed", `, closed_at = ?`, store.Millis(time.Now()))
}

// Reopen returns a closed revision to Editing.
func (s *Service) Reopen(ctx context.Context, rev Revision, c Caller) (Revision, error) {
	a, err := s.AccessFor(ctx, rev, c)
	if err != nil {
		return rev, err
	}
	if !a.CanManage {
		return rev, ErrForbidden
	}
	if rev.State != Closed {
		return rev, conflict("invalid_state", "Only closed revisions can be reopened.")
	}
	if rev.ClosedAt != nil && time.Since(*rev.ClosedAt) > reopenFor {
		return rev, conflict("archived", "Revisions closed more than 90 days ago can't be reopened.")
	}
	return s.setState(ctx, rev, c, Editing, "reopened", `, closed_at = NULL`)
}

func (s *Service) setState(ctx context.Context, rev Revision, c Caller, to State, event, extra string, args ...any) (Revision, error) {
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		res, err := store.Exec(ctx, tx, `UPDATE revisions SET state = ?`+extra+` WHERE id = ? AND state = ?`, append(append([]any{string(to)}, args...), rev.ID, string(rev.State))...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return conflict("state_changed", "The revision changed state. Reload and try again.")
		}
		return Record(ctx, tx, rev.ID, ActorUser, c.User.ID, event, map[string]any{"from": rev.State, "to": to})
	})
	if err != nil {
		return rev, err
	}
	s.changed(ctx, rev.ID, event)
	return Get(ctx, s.DB, rev.ID)
}

// FileOp is a manifest operation.
type FileOp struct {
	Op       string `json:"op"` // add | modify | rename | delete
	Path     string `json:"path"`
	FromPath string `json:"from_path,omitempty"` // rename
	Template string `json:"template,omitempty"`  // add
	Content  string `json:"content,omitempty"`   // add: initial markdown (overrides template)
}

// ApplyFileOp changes the manifest.
func (s *Service) ApplyFileOp(ctx context.Context, repo repos.Repo, rev Revision, c Caller, op FileOp) (File, error) {
	a, err := s.AccessFor(ctx, rev, c)
	if err != nil {
		return File{}, err
	}
	if !a.CanEdit {
		return File{}, ErrForbidden
	}
	var f File
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		var err error
		f, err = s.applyOp(ctx, tx, repo, rev, c, op)
		return err
	})
	if err != nil {
		return File{}, err
	}
	s.changed(ctx, rev.ID, "files")
	return f, nil
}

// EnsureFile adds path to the manifest as modified if it isn't there yet (the
// first edit to an untouched page).
func (s *Service) EnsureFile(ctx context.Context, repo repos.Repo, rev Revision, c Caller, p string) (File, error) {
	if f, err := FileAt(ctx, s.DB, rev.ID, cleanPath(p)); err == nil {
		if f.Op == OpDelete {
			return File{}, conflict("file_deleted", "This page is deleted in the revision.")
		}
		return f, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return File{}, err
	}
	return s.ApplyFileOp(ctx, repo, rev, c, FileOp{Op: OpModify, Path: p})
}

// contentHash identifies materialized content (approvals will pin it).
func contentHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:16])
}

func cleanPath(p string) string { return strings.TrimPrefix(path.Clean("/"+strings.TrimSpace(p)), "/") }

func (s *Service) checkTarget(repo repos.Repo, p string) error {
	if p == "" || p == "." {
		return invalid("path", "Choose a file name.")
	}
	if !repos.IsMarkdown(p) {
		return invalid("path", "Pages are markdown files (.md).")
	}
	if !repo.Scope().Contains(p) {
		return invalid("path", "That path is outside the repository's content folder.")
	}
	return nil
}

// base reads a path at the revision's base commit. ok is false when absent.
func (s *Service) base(ctx context.Context, repo repos.Repo, rev Revision, p string) (string, bool, error) {
	f, err := s.Repos.ReadFile(ctx, repo, p, rev.BaseSHA)
	if err != nil {
		if errors.Is(err, gitmirror.ErrNotFound) || errors.Is(err, repos.ErrOutOfScope) {
			return "", false, nil
		}
		return "", false, err
	}
	return f.Content, true, nil
}

func (s *Service) applyOp(ctx context.Context, tx *store.Tx, repo repos.Repo, rev Revision, c Caller, op FileOp) (File, error) {
	op.Path = cleanPath(op.Path)
	if err := s.checkTarget(repo, op.Path); err != nil {
		return File{}, err
	}
	existing, err := FileAt(ctx, tx, rev.ID, op.Path)
	has := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return File{}, err
	}
	now := store.Millis(time.Now())
	insert := func(p, kind, from, base, content string) error {
		var n int
		if err := store.QueryRow(ctx, tx, `SELECT COUNT(*) FROM revision_files WHERE revision_id = ?`, rev.ID).Scan(&n); err != nil {
			return err
		}
		if n >= MaxFiles {
			return conflict("too_many_files", fmt.Sprintf("A revision can change at most %d files.", MaxFiles))
		}
		add, del := textdiff.Stats(base, content)
		_, err := store.Exec(ctx, tx, `INSERT INTO revision_files (id, revision_id, path, op, from_path, base_md, content_md, content_hash, additions, deletions, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, ids.New(ids.RevisionFile), rev.ID, p, kind, from, base, content, contentHash(content), add, del, now, now)
		return err
	}
	event := map[string]any{"path": op.Path}
	switch op.Op {
	case OpModify:
		if has {
			if existing.Op == OpDelete {
				return File{}, conflict("file_deleted", "This page is deleted in the revision. Restore it first.")
			}
			return existing, nil // already in the revision
		}
		if _, err := fileFrom(ctx, tx, rev.ID, op.Path); err == nil {
			return File{}, conflict("file_renamed", "This page was renamed in the revision.")
		}
		base, ok, err := s.base(ctx, repo, rev, op.Path)
		if err != nil {
			return File{}, err
		}
		if !ok {
			return File{}, conflict("file_missing", "This page doesn't exist in the revision's base.")
		}
		if len(base) > MaxFileSize {
			return File{}, conflict("file_too_large", "This file is too large to edit in kmdn.")
		}
		if err := insert(op.Path, OpModify, "", base, base); err != nil {
			return File{}, err
		}
		// Adding a page to a revision isn't an event worth listing; edits are.
		return FileAt(ctx, tx, rev.ID, op.Path)
	case OpAdd:
		if has && existing.Op != OpDelete {
			return File{}, conflict("file_exists", "A page with this name is already in the revision.")
		}
		content := op.Content
		if content == "" && op.Template != "" {
			if content, err = s.Repos.ReadTemplate(ctx, repo, op.Template); err != nil {
				return File{}, invalid("template", "That template doesn't exist.")
			}
		}
		if content == "" {
			content = "# " + titleFromPath(op.Path) + "\n"
		}
		if has { // re-adding a page deleted in the revision: it's a modify now
			add, del := textdiff.Stats(existing.BaseMD, content)
			if _, err := store.Exec(ctx, tx, `UPDATE revision_files SET op = ?, content_md = ?, content_hash = ?, additions = ?, deletions = ?, updated_at = ? WHERE id = ?`,
				OpModify, content, contentHash(content), add, del, now, existing.ID); err != nil {
				return File{}, err
			}
		} else {
			if _, ok, err := s.base(ctx, repo, rev, op.Path); err != nil {
				return File{}, err
			} else if ok {
				if _, err := fileFrom(ctx, tx, rev.ID, op.Path); err != nil { // not renamed away
					return File{}, conflict("file_exists", "A published page already has this name.")
				}
			}
			if err := insert(op.Path, OpAdd, "", "", content); err != nil {
				return File{}, err
			}
		}
		if op.Template != "" {
			event["template"] = op.Template
		}
		if err := Record(ctx, tx, rev.ID, ActorUser, c.User.ID, "file_added", event); err != nil {
			return File{}, err
		}
	case OpDelete:
		switch {
		case has && existing.Op == OpDelete:
			return existing, nil
		case has && existing.Op == OpAdd:
			if _, err := store.Exec(ctx, tx, `DELETE FROM revision_files WHERE id = ?`, existing.ID); err != nil {
				return File{}, err
			}
			if err := s.dropDoc(ctx, tx, rev.ID, op.Path); err != nil {
				return File{}, err
			}
		case has && existing.Op == OpRename:
			// Deleting a renamed page deletes the original.
			if _, err := store.Exec(ctx, tx, `DELETE FROM revision_files WHERE id = ?`, existing.ID); err != nil {
				return File{}, err
			}
			if err := s.dropDoc(ctx, tx, rev.ID, op.Path); err != nil {
				return File{}, err
			}
			if err := insert(existing.FromPath, OpDelete, "", existing.BaseMD, ""); err != nil {
				return File{}, err
			}
			op.Path, event["path"] = existing.FromPath, existing.FromPath
		case has: // modify
			add, del := textdiff.Stats(existing.BaseMD, "")
			if _, err := store.Exec(ctx, tx, `UPDATE revision_files SET op = ?, content_md = '', content_hash = '', additions = ?, deletions = ?, updated_at = ? WHERE id = ?`,
				OpDelete, add, del, now, existing.ID); err != nil {
				return File{}, err
			}
			if err := s.dropDoc(ctx, tx, rev.ID, op.Path); err != nil {
				return File{}, err
			}
		default:
			base, ok, err := s.base(ctx, repo, rev, op.Path)
			if err != nil {
				return File{}, err
			}
			if !ok {
				return File{}, conflict("file_missing", "This page doesn't exist in the revision.")
			}
			if err := insert(op.Path, OpDelete, "", base, ""); err != nil {
				return File{}, err
			}
		}
		if err := Record(ctx, tx, rev.ID, ActorUser, c.User.ID, "file_deleted", event); err != nil {
			return File{}, err
		}
		if f, err := FileAt(ctx, tx, rev.ID, op.Path); err == nil {
			return f, nil
		}
		return File{Path: op.Path, Op: OpDelete}, nil
	case OpRename:
		from := cleanPath(op.FromPath)
		if from == op.Path {
			return File{}, invalid("path", "Pick a different name.")
		}
		if has && existing.Op != OpDelete {
			return File{}, conflict("file_exists", "A page with this name is already in the revision.")
		}
		if _, ok, err := s.base(ctx, repo, rev, op.Path); err != nil {
			return File{}, err
		} else if ok && (!has || existing.Op != OpDelete) {
			if _, err := fileFrom(ctx, tx, rev.ID, op.Path); err != nil {
				return File{}, conflict("file_exists", "A published page already has this name.")
			}
		}
		if has { // the target was deleted in the revision; the rename replaces that
			if _, err := store.Exec(ctx, tx, `DELETE FROM revision_files WHERE id = ?`, existing.ID); err != nil {
				return File{}, err
			}
		}
		src, err := FileAt(ctx, tx, rev.ID, from)
		switch {
		case err == nil && src.Op == OpDelete:
			return File{}, conflict("file_deleted", "That page is deleted in the revision.")
		case err == nil:
			kind, orig := OpRename, src.FromPath
			switch src.Op {
			case OpAdd:
				kind, orig = OpAdd, ""
			case OpModify:
				orig = from
			}
			if orig == op.Path { // renamed back to where it started
				kind, orig = OpModify, ""
			}
			if _, err := store.Exec(ctx, tx, `UPDATE revision_files SET path = ?, op = ?, from_path = ?, updated_at = ? WHERE id = ?`, op.Path, kind, orig, now, src.ID); err != nil {
				return File{}, err
			}
			if _, err := store.Exec(ctx, tx, `UPDATE ydocs SET path = ? WHERE revision_id = ? AND path = ?`, op.Path, rev.ID, from); err != nil {
				return File{}, err
			}
		case errors.Is(err, store.ErrNotFound):
			base, ok, err := s.base(ctx, repo, rev, from)
			if err != nil {
				return File{}, err
			}
			if !ok {
				return File{}, conflict("file_missing", "That page doesn't exist in the revision.")
			}
			if err := insert(op.Path, OpRename, from, base, base); err != nil {
				return File{}, err
			}
		default:
			return File{}, err
		}
		event["from_path"] = from
		if err := Record(ctx, tx, rev.ID, ActorUser, c.User.ID, "file_renamed", event); err != nil {
			return File{}, err
		}
	default:
		return File{}, invalid("op", "op must be add, modify, rename or delete.")
	}
	return FileAt(ctx, tx, rev.ID, op.Path)
}

// dropDoc removes a file's collaborative document.
func (s *Service) dropDoc(ctx context.Context, tx *store.Tx, revID, p string) error {
	_, err := store.Exec(ctx, tx, `DELETE FROM ydocs WHERE revision_id = ? AND path = ?`, revID, p)
	if err == nil && s.Removed != nil {
		if rev, gerr := Get(ctx, tx, revID); gerr == nil {
			s.Removed(ctx, rev, p)
		}
	}
	return err
}

func titleFromPath(p string) string {
	name := strings.TrimSuffix(path.Base(p), path.Ext(p))
	name = strings.NewReplacer("-", " ", "_", " ").Replace(name)
	if name == "" {
		return "Untitled"
	}
	r, size := utf8.DecodeRuneInString(name)
	return strings.ToUpper(string(r)) + name[size:]
}

// SetContent stores a file's materialized markdown (from the collaborative
// document) and refreshes its +/− counts.
func (s *Service) SetContent(ctx context.Context, revID, p, md string) error {
	f, err := FileAt(ctx, s.DB, revID, p)
	if err != nil {
		return err
	}
	h := contentHash(md)
	if h == f.ContentHash && f.MaterializedAt != nil {
		return nil
	}
	add, del := textdiff.Stats(f.BaseMD, md)
	now := store.Millis(time.Now())
	if _, err := store.Exec(ctx, s.DB, `UPDATE revision_files SET content_md = ?, content_hash = ?, additions = ?, deletions = ?, materialized_at = ?, updated_at = ? WHERE id = ?`,
		md, h, add, del, now, now, f.ID); err != nil {
		return err
	}
	_, err = store.Exec(ctx, s.DB, `UPDATE revisions SET updated_at = ? WHERE id = ?`, now, revID)
	return err
}

// Content is a page as seen inside a revision.
type Content struct {
	Path       string `json:"path"`
	Op         string `json:"op,omitempty"` // empty when the revision doesn't touch it
	FromPath   string `json:"from_path,omitempty"`
	Content    string `json:"content"`
	Base       string `json:"base"`
	Markdown   bool   `json:"markdown"`
	InRevision bool   `json:"in_revision"`
}

// Read returns a page in the revision: its current content when the revision
// touches it, otherwise the base.
func (s *Service) Read(ctx context.Context, repo repos.Repo, rev Revision, p string) (Content, error) {
	p = cleanPath(p)
	f, err := FileAt(ctx, s.DB, rev.ID, p)
	switch {
	case err == nil:
		if f.Op == OpDelete {
			return Content{}, conflict("file_deleted", "This page is deleted in the revision.")
		}
		return Content{Path: p, Op: f.Op, FromPath: f.FromPath, Content: f.ContentMD, Base: f.BaseMD, Markdown: repos.IsMarkdown(p), InRevision: true}, nil
	case !errors.Is(err, store.ErrNotFound):
		return Content{}, err
	}
	if moved, err := fileFrom(ctx, s.DB, rev.ID, p); err == nil {
		return Content{}, (&ErrConflict{Code: "file_renamed", Msg: "This page was renamed to " + moved.Path + " in the revision."})
	}
	base, err := s.Repos.ReadFile(ctx, repo, p, rev.BaseSHA)
	if err != nil {
		return Content{}, err
	}
	return Content{Path: p, Content: base.Content, Base: base.Content, Markdown: base.Markdown}, nil
}

// TreeNode is a file or folder as seen in the revision.
type TreeNode struct {
	repos.Node
	Op string `json:"op,omitempty"`
}

// Tree lists the revision's files: the base tree with the manifest applied.
func (s *Service) Tree(ctx context.Context, repo repos.Repo, rev Revision) ([]TreeNode, error) {
	base, err := s.Repos.TreeAt(ctx, repo, rev.BaseSHA)
	if err != nil {
		return nil, err
	}
	files, err := Files(ctx, s.DB, rev.ID)
	if err != nil {
		return nil, err
	}
	nodes := map[string]TreeNode{}
	for _, n := range base {
		if n.Type == "file" {
			nodes[n.Path] = TreeNode{Node: n}
		}
	}
	for _, f := range files {
		switch f.Op {
		case OpDelete:
			delete(nodes, f.Path)
		case OpRename:
			delete(nodes, f.FromPath)
			fallthrough
		default:
			nodes[f.Path] = TreeNode{Node: repos.Node{Path: f.Path, Name: path.Base(f.Path), Type: "file", Markdown: repos.IsMarkdown(f.Path), Size: int64(len(f.ContentMD))}, Op: f.Op}
		}
	}
	root := repo.Scope().Root
	out := make([]TreeNode, 0, len(nodes))
	dirs := map[string]bool{}
	for _, n := range nodes {
		out = append(out, n)
		for d := path.Dir(n.Path); d != "." && d != root && !dirs[d]; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	for d := range dirs {
		out = append(out, TreeNode{Node: repos.Node{Path: d, Name: path.Base(d), Type: "dir"}})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// AddMember invites a repo contributor to edit the revision.
func (s *Service) AddMember(ctx context.Context, rev Revision, c Caller, userID string, targetRole access.Role) error {
	a, err := s.AccessFor(ctx, rev, c)
	if err != nil {
		return err
	}
	if !a.CanManage || !rev.State.Open() {
		return ErrForbidden
	}
	if !targetRole.AtLeast(access.Contributor) {
		return invalid("user_id", "Only people who can edit the repository can edit a revision. Ask an admin to make them a Contributor.")
	}
	err = s.DB.InTx(ctx, func(tx *store.Tx) error {
		if _, err := store.Exec(ctx, tx, `INSERT INTO revision_members (revision_id, user_id, role, invited_by, added_at) VALUES (?, ?, ?, ?, ?)`,
			rev.ID, userID, RoleEditor, c.User.ID, store.Millis(time.Now())); err != nil {
			if store.IsUniqueViolation(err) {
				return nil
			}
			return err
		}
		return Record(ctx, tx, rev.ID, ActorUser, c.User.ID, "member_added", map[string]any{"user_id": userID})
	})
	if err == nil {
		s.changed(ctx, rev.ID, "members")
	}
	return err
}

// RemoveMember removes an editor. People can always remove themselves; the
// owner stays.
func (s *Service) RemoveMember(ctx context.Context, rev Revision, c Caller, userID string) error {
	if userID != c.User.ID {
		a, err := s.AccessFor(ctx, rev, c)
		if err != nil {
			return err
		}
		if !a.CanManage {
			return ErrForbidden
		}
	}
	err := s.DB.InTx(ctx, func(tx *store.Tx) error {
		res, err := store.Exec(ctx, tx, `DELETE FROM revision_members WHERE revision_id = ? AND user_id = ? AND role <> ?`, rev.ID, userID, RoleOwner)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return conflict("not_removable", "The revision's owner can't be removed.")
		}
		return Record(ctx, tx, rev.ID, ActorUser, c.User.ID, "member_removed", map[string]any{"user_id": userID})
	})
	if err == nil {
		s.changed(ctx, rev.ID, "members")
	}
	return err
}
