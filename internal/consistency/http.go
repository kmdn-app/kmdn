package consistency

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
)

// Routes registers the consistency endpoints.
func (s *Service) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Require)
		r.Get("/revisions/{revision}/consistency", s.revisionGet)
		r.Post("/revisions/{revision}/consistency", s.revisionRun)
		r.Get("/repos/{repo}/consistency", s.repoGet)
		r.Post("/repos/{repo}/consistency/scan", s.repoScan)
		r.Post("/consistency/findings/{finding}/ignore", s.ignore)
		r.Delete("/consistency/findings/{finding}/ignore", s.unignore)
		r.Post("/consistency/findings/{finding}/fix", s.fix)
	})
}

var errOff = api.Err(http.StatusConflict, "consistency_off", "Consistency checks need an AI provider and an embeddings model (Admin → AI).")

func (s *Service) role(r *http.Request, repoID string) access.Role {
	p, _ := auth.FromContext(r.Context())
	role, err := access.Effective(r.Context(), s.DB, p.User, repoID)
	if err != nil {
		return access.None
	}
	return role
}

func (s *Service) loadRevision(w http.ResponseWriter, r *http.Request) (revisions.Revision, revisions.Caller, bool) {
	p, _ := auth.FromContext(r.Context())
	rev, err := revisions.Get(r.Context(), s.DB, chi.URLParam(r, "revision"))
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return rev, revisions.Caller{}, false
	}
	role := s.role(r, rev.RepoID)
	if role == access.None {
		api.Error(w, r, api.ErrNotFound)
		return rev, revisions.Caller{}, false
	}
	return rev, revisions.Caller{User: p.User, Role: role}, true
}

func (s *Service) revisionGet(w http.ResponseWriter, r *http.Request) {
	rev, _, ok := s.loadRevision(w, r)
	if !ok {
		return
	}
	list, err := s.List(r.Context(), rev.RepoID, rev.ID, "")
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"available": s.Available(r.Context()), "pending": s.Pending(r.Context(), JobRevision, rev.ID), "findings": list})
}

func (s *Service) revisionRun(w http.ResponseWriter, r *http.Request) {
	rev, c, ok := s.loadRevision(w, r)
	if !ok {
		return
	}
	acc, err := s.Revisions.AccessFor(r.Context(), rev, c)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	if !acc.Member && !acc.Reviewer && !c.Role.AtLeast(access.Maintainer) {
		api.Error(w, r, api.Err(http.StatusForbidden, "forbidden", "Editors and reviewers of the revision can run the check."))
		return
	}
	if !s.Available(r.Context()) {
		api.Error(w, r, errOff)
		return
	}
	s.RequestRevision(r.Context(), rev.ID, 0)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Service) repoGet(w http.ResponseWriter, r *http.Request) {
	repoID := chi.URLParam(r, "repo")
	role := s.role(r, repoID)
	if role == access.None {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	status := r.URL.Query().Get("status")
	list, err := s.List(r.Context(), repoID, ScopePublished, status)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	scan, err := s.LastScan(r.Context(), repoID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{
		"available": s.Available(r.Context()), "pending": s.Pending(r.Context(), JobScan, repoID),
		"can_run": role.AtLeast(access.Maintainer), "can_fix": role.AtLeast(access.Contributor),
		"scan": scan, "findings": list,
	})
}

func (s *Service) repoScan(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	repoID := chi.URLParam(r, "repo")
	role := s.role(r, repoID)
	if role == access.None {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	if !role.AtLeast(access.Maintainer) {
		api.Error(w, r, api.Err(http.StatusForbidden, "forbidden", "Maintainers run consistency scans."))
		return
	}
	if !s.Available(r.Context()) {
		api.Error(w, r, errOff)
		return
	}
	if err := s.RequestScan(r.Context(), repoID, p.User.ID); err != nil {
		api.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Service) loadFinding(w http.ResponseWriter, r *http.Request) (Finding, access.Role, bool) {
	f, err := s.Get(r.Context(), chi.URLParam(r, "finding"))
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return f, access.None, false
	}
	role := s.role(r, f.RepoID)
	if role == access.None {
		api.Error(w, r, api.ErrNotFound)
		return f, role, false
	}
	if !role.AtLeast(access.Contributor) {
		api.Error(w, r, api.Err(http.StatusForbidden, "forbidden", "Contributors can act on consistency findings."))
		return f, role, false
	}
	return f, role, true
}

func (s *Service) ignore(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	f, _, ok := s.loadFinding(w, r)
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	if strings.TrimSpace(in.Reason) == "" {
		api.Error(w, r, api.Invalid("reason", "Say why this is fine, so others know."))
		return
	}
	if err := s.Ignore(r.Context(), f, p.User.ID, in.Reason); err != nil {
		api.Error(w, r, err)
		return
	}
	s.changed(f)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) unignore(w http.ResponseWriter, r *http.Request) {
	f, _, ok := s.loadFinding(w, r)
	if !ok {
		return
	}
	if err := s.Unignore(r.Context(), f); err != nil {
		api.Error(w, r, err)
		return
	}
	s.changed(f)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) changed(f Finding) {
	if s.Publish == nil {
		return
	}
	if f.Scope == ScopePublished {
		s.Publish("repo:"+f.RepoID, map[string]any{"type": "consistency", "repo": f.RepoID})
	} else {
		s.Publish("revision:"+f.Scope, map[string]any{"type": "consistency", "revision": f.Scope})
	}
}

// Brief is what the assistant is asked to do about a finding: "fix" aligns
// the text, "link" replaces a duplicate with a link to the other page.
func Brief(f Finding, action string, both bool) string {
	ref := func(sd Side) string {
		s := sd.Path
		if sd.Slug != "" {
			s += "#" + sd.Slug
		}
		if sd.Heading != "" {
			s += fmt.Sprintf(" (under “%s”)", sd.Heading)
		}
		return s
	}
	var b strings.Builder
	if action == "link" || (f.Kind == Duplicate && action != "fix") {
		fmt.Fprintf(&b, "%s repeats what %s says.\n\n", ref(f.A), ref(f.B))
		fmt.Fprintf(&b, "Replace the repeated passage in %s with a short sentence that links to %s (a relative markdown link), keeping anything only %s says.", f.A.Path, f.B.Path+anchor(f.B.Slug), f.A.Path)
		return b.String()
	}
	if f.Kind == Duplicate {
		fmt.Fprintf(&b, "%s and %s say the same thing. Remove the repetition: keep the fuller version and have the other page link to it.", ref(f.A), ref(f.B))
		return b.String()
	}
	b.WriteString("Make these two passages agree:\n\n")
	fmt.Fprintf(&b, "- %s: “%s”\n", ref(f.A), orText(f.ClaimA, f.A.Text))
	fmt.Fprintf(&b, "- %s: “%s”\n\n", ref(f.B), orText(f.ClaimB, f.B.Text))
	if f.Explanation != "" {
		b.WriteString(f.Explanation + "\n\n")
	}
	b.WriteString("Work out which is right from the rest of the repository. If you can't tell, ask me before changing anything.")
	if both {
		fmt.Fprintf(&b, " Suggest the edits in %s or %s, wherever the wrong one is.", f.A.Path, f.B.Path)
	}
	return b.String()
}

func anchor(slug string) string {
	if slug == "" {
		return ""
	}
	return "#" + slug
}

func orText(claim, text string) string {
	if claim != "" {
		return claim
	}
	return truncate(text, 300)
}

func (s *Service) fix(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	f, role, ok := s.loadFinding(w, r)
	if !ok {
		return
	}
	var in struct {
		Action string `json:"action"` // fix | link
	}
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	if in.Action != "fix" && in.Action != "link" {
		api.Error(w, r, api.Invalid("action", "action is fix or link."))
		return
	}
	if in.Action == "link" && f.Kind != Duplicate {
		api.Error(w, r, api.Invalid("action", "Only duplicates can be replaced with a link."))
		return
	}
	if !s.LLM.Enabled(r.Context()) || s.Brief == nil {
		api.Error(w, r, errOff)
		return
	}
	c := revisions.Caller{User: p.User, Role: role}
	repo, err := repos.Get(r.Context(), s.DB, f.RepoID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	// A revision's finding: its assistant makes suggestions in the revision.
	if f.Scope != ScopePublished {
		rev, err := revisions.Get(r.Context(), s.DB, f.Scope)
		if err != nil {
			api.Error(w, r, api.ErrNotFound)
			return
		}
		acc, err := s.Revisions.AccessFor(r.Context(), rev, c)
		if err != nil {
			api.Error(w, r, err)
			return
		}
		if !acc.CanEdit {
			api.Error(w, r, api.Err(http.StatusForbidden, "forbidden", "Only the revision's editors can ask for a fix."))
			return
		}
		if err := s.Brief(r.Context(), rev, p.User, Brief(f, in.Action, false), f.A.Path); err != nil {
			api.Error(w, r, err)
			return
		}
		api.JSON(w, http.StatusAccepted, map[string]any{"revision": rev})
		return
	}
	// The repo report: start a revision touching both pages.
	if f.Fix != nil && (f.Fix.State == string(revisions.Editing) || f.Fix.State == string(revisions.InReview) || f.Fix.State == string(revisions.Approved)) {
		api.Error(w, r, api.Err(http.StatusConflict, "already_fixing", fmt.Sprintf("Revision #%d is already fixing this.", f.Fix.Number)))
		return
	}
	title := fmt.Sprintf("Align %s and %s", pageName(f.A.Path), pageName(f.B.Path))
	if f.Kind == Duplicate {
		title = fmt.Sprintf("Remove duplicate text in %s and %s", pageName(f.A.Path), pageName(f.B.Path))
	}
	rev, err := s.Revisions.Create(r.Context(), repo, c, revisions.CreateInput{Title: truncate(title, 90), Description: f.Explanation})
	if err != nil {
		var cf *revisions.ErrConflict
		switch {
		case errors.Is(err, revisions.ErrForbidden):
			api.Error(w, r, api.Err(http.StatusForbidden, "forbidden", "Only contributors can start a revision."))
		case errors.As(err, &cf):
			api.Error(w, r, api.Err(http.StatusConflict, cf.Code, cf.Msg))
		default:
			api.Error(w, r, err)
		}
		return
	}
	for _, pth := range []string{f.A.Path, f.B.Path} {
		if _, err := s.Revisions.ApplyFileOp(r.Context(), repo, rev, c, revisions.FileOp{Op: revisions.OpModify, Path: pth}); err != nil {
			s.Log.Warn("add page to fix revision", "err", err, "path", pth)
		}
	}
	if err := s.SetFix(r.Context(), f, rev.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		api.Error(w, r, err)
		return
	}
	if err := s.Brief(r.Context(), rev, p.User, Brief(f, in.Action, true), f.A.Path); err != nil {
		s.Log.Warn("brief the assistant", "err", err, "revision", rev.ID)
	}
	s.changed(f)
	api.JSON(w, http.StatusCreated, map[string]any{"revision": rev})
}

// pageName is a file name without folder or extension.
func pageName(p string) string {
	return strings.TrimSuffix(path.Base(p), path.Ext(p))
}
