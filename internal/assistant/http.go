package assistant

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/gitmirror"
	"github.com/kmdn-app/kmdn/internal/llm"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/users"
)

// Routes registers the assistant endpoints.
func (s *Service) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Require)
		r.Get("/repos/{repo}/assistant/threads", s.listThreads)
		r.Post("/repos/{repo}/assistant/threads", s.createThread)
		r.Get("/assistant/threads/{thread}", s.getThread)
		r.Delete("/assistant/threads/{thread}", s.deleteThread)
		r.Post("/assistant/threads/{thread}/messages", s.postMessage)
		r.Post("/assistant/threads/{thread}/proposals/{call}/accept", s.acceptProposal)
		r.Get("/revisions/{revision}/assistant", s.revisionThread)
	})
}

// CanRead reports whether u may see a thread: its owner for Q&A, anyone
// who can see the repository for a revision's thread.
func (s *Service) CanRead(ctx context.Context, u users.User, t Thread) bool {
	if t.OwnerID != "" {
		return t.OwnerID == u.ID
	}
	role, err := access.Effective(ctx, s.DB, u, t.RepoID)
	return err == nil && role != access.None
}

func (s *Service) repo(w http.ResponseWriter, r *http.Request) (repos.Repo, users.User, access.Role, bool) {
	p, _ := auth.FromContext(r.Context())
	repo, err := repos.Get(r.Context(), s.DB, chi.URLParam(r, "repo"))
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return repo, p.User, "", false
	}
	role, err := access.Effective(r.Context(), s.DB, p.User, repo.ID)
	if err != nil || role == access.None {
		api.Error(w, r, api.ErrNotFound)
		return repo, p.User, role, false
	}
	return repo, p.User, role, true
}

func (s *Service) thread(w http.ResponseWriter, r *http.Request) (Thread, users.User, bool) {
	p, _ := auth.FromContext(r.Context())
	t, err := GetThread(r.Context(), s.DB, chi.URLParam(r, "thread"))
	if err != nil || !s.CanRead(r.Context(), p.User, t) {
		api.Error(w, r, api.ErrNotFound)
		return t, p.User, false
	}
	return t, p.User, true
}

func (s *Service) enabled(w http.ResponseWriter, r *http.Request) bool {
	if !s.LLM.Enabled(r.Context()) {
		api.Error(w, r, api.Err(http.StatusConflict, "assistant_off", "The assistant isn't set up on this instance."))
		return false
	}
	return true
}

type postInput struct {
	Text    string  `json:"text"`
	Context Context `json:"context"`
}

func (s *Service) listThreads(w http.ResponseWriter, r *http.Request) {
	repo, u, _, ok := s.repo(w, r)
	if !ok {
		return
	}
	list, err := ListQA(r.Context(), s.DB, repo.ID, u.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": list})
}

func title(text string) string {
	t := strings.Join(strings.Fields(text), " ")
	if r := []rune(t); len(r) > 70 {
		t = string(r[:69]) + "…"
	}
	return t
}

// createThread starts a private Q&A thread with its first question.
func (s *Service) createThread(w http.ResponseWriter, r *http.Request) {
	repo, u, _, ok := s.repo(w, r)
	if !ok || !s.enabled(w, r) {
		return
	}
	var in postInput
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	if strings.TrimSpace(in.Text) == "" {
		api.Error(w, r, api.Invalid("text", errBadText.Error()))
		return
	}
	t, err := CreateQA(r.Context(), s.DB, repo.ID, u.ID, title(in.Text))
	if err != nil {
		api.Error(w, r, err)
		return
	}
	m, err := s.Post(r.Context(), t, u, in.Text, in.Context)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusCreated, map[string]any{"thread": t, "message": view(m)})
}

func (s *Service) getThread(w http.ResponseWriter, r *http.Request) {
	t, _, ok := s.thread(w, r)
	if !ok {
		return
	}
	msgs, err := Messages(r.Context(), s.DB, t.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"thread": t, "messages": views(msgs), "running": s.Running(t.ID)})
}

// views are the messages the UI shows, with confirmed proposals marked.
func views(msgs []Message) []MessageView {
	accepted := map[string]bool{}
	for _, m := range msgs {
		if strings.HasPrefix(m.RunID, acceptedPrefix) {
			accepted[strings.TrimPrefix(m.RunID, acceptedPrefix)] = true
		}
	}
	out := make([]MessageView, 0, len(msgs))
	for _, m := range msgs {
		if v := viewWith(m, accepted); len(v.Parts) > 0 {
			out = append(out, v)
		}
	}
	return out
}

// revisionThread returns (creating it the first time) a revision's shared
// thread, and whether the caller can prompt in it.
func (s *Service) revisionThread(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	rev, err := revisions.Get(r.Context(), s.DB, chi.URLParam(r, "revision"))
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	role, err := access.Effective(r.Context(), s.DB, p.User, rev.RepoID)
	if err != nil || role == access.None {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	t, err := RevisionThread(r.Context(), s.DB, rev.RepoID, rev.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	msgs, err := Messages(r.Context(), s.DB, t.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	acc, _ := s.Revisions.AccessFor(r.Context(), rev, revisions.Caller{User: p.User, Role: role})
	api.JSON(w, http.StatusOK, map[string]any{"thread": t, "messages": views(msgs), "running": s.Running(t.ID), "can_prompt": acc.CanEdit && s.LLM.Enabled(r.Context())})
}

func (s *Service) deleteThread(w http.ResponseWriter, r *http.Request) {
	t, u, ok := s.thread(w, r)
	if !ok {
		return
	}
	if t.OwnerID != u.ID {
		api.Error(w, r, api.Err(http.StatusForbidden, "forbidden", "Only private conversations can be deleted."))
		return
	}
	if err := DeleteThread(r.Context(), s.DB, t.ID); err != nil {
		api.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) postMessage(w http.ResponseWriter, r *http.Request) {
	t, u, ok := s.thread(w, r)
	if !ok || !s.enabled(w, r) {
		return
	}
	// Revision threads: anyone who can edit the revision can prompt.
	if t.RevisionID != "" {
		rev, err := revisions.Get(r.Context(), s.DB, t.RevisionID)
		if err != nil {
			api.Error(w, r, api.ErrNotFound)
			return
		}
		role, _ := access.Effective(r.Context(), s.DB, u, t.RepoID)
		acc, err := s.Revisions.AccessFor(r.Context(), rev, revisions.Caller{User: u, Role: role})
		if err != nil || !acc.CanEdit {
			api.Error(w, r, api.Err(http.StatusForbidden, "forbidden", "Only people who can edit this revision can ask the assistant here."))
			return
		}
	}
	var in postInput
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	m, err := s.Post(r.Context(), t, u, in.Text, in.Context)
	if errors.Is(err, errBadText) {
		api.Error(w, r, api.Invalid("text", err.Error()))
		return
	}
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusAccepted, map[string]any{"message": view(m), "queued": s.Running(t.ID)})
}

// acceptProposal carries out a proposal: in Q&A, starts the revision,
// adds its pages and moves the conversation into its shared thread; in a
// revision, applies a rename or delete the assistant proposed.
func (s *Service) acceptProposal(w http.ResponseWriter, r *http.Request) {
	t, u, ok := s.thread(w, r)
	if !ok {
		return
	}
	msgs, err := Messages(r.Context(), s.DB, t.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	callID := chi.URLParam(r, "call")
	for _, m := range msgs {
		if m.RunID == acceptedPrefix+callID {
			api.Error(w, r, api.Err(http.StatusConflict, "already_done", "That's already done."))
			return
		}
	}
	if t.RevisionID != "" {
		s.acceptFileOp(w, r, t, u, msgs, callID)
		return
	}
	if t.OwnerID != u.ID {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	p, found := findProposal(msgs, chi.URLParam(r, "call"))
	if !found {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	repo, err := repos.Get(r.Context(), s.DB, t.RepoID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	role, _ := access.Effective(r.Context(), s.DB, u, repo.ID)
	c := revisions.Caller{User: u, Role: role}
	in := revisions.CreateInput{Title: p.Title, Description: p.Description}
	var files []string
	for _, f := range p.Files {
		if f = strings.TrimPrefix(strings.TrimSpace(f), "/"); repos.IsMarkdown(f) {
			files = append(files, f)
		}
	}
	rev, err := s.Revisions.Create(r.Context(), repo, c, in)
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
	for _, f := range files {
		op := revisions.OpModify
		if _, err := s.Repos.ReadFile(r.Context(), repo, f, ""); errors.Is(err, gitmirror.ErrNotFound) {
			op = revisions.OpAdd
		}
		if _, err := s.Revisions.ApplyFileOp(r.Context(), repo, rev, c, revisions.FileOp{Op: op, Path: f}); err != nil {
			s.Log.Warn("add proposed page", "err", err, "path", f)
		}
	}
	shared, err := RevisionThread(r.Context(), s.DB, repo.ID, rev.ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	for _, m := range msgs {
		m.ID = ""
		if _, err := AddMessage(r.Context(), s.DB, shared.ID, m); err != nil {
			api.Error(w, r, err)
			return
		}
	}
	// Anyone already following the revision's thread sees the conversation.
	s.emit(shared.ID, map[string]any{"kind": "message"})
	_, _ = AddMessage(r.Context(), s.DB, t.ID, Message{Role: llm.RoleAssistant, RunID: acceptedPrefix + callID, Content: []llm.Block{{Type: llm.BlockText, Text: "Started revision #" + itoa(rev.Number) + ": " + rev.Title + ". The conversation continues there."}}})
	s.emit(t.ID, map[string]any{"kind": "message"})
	api.JSON(w, http.StatusCreated, map[string]any{"revision": rev, "thread": shared})
}

func (s *Service) acceptFileOp(w http.ResponseWriter, r *http.Request, t Thread, u users.User, msgs []Message, callID string) {
	var name string
	var input []byte
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolUse && b.ID == callID && fileOps[b.Name] {
				name, input = b.Name, b.Input
			}
		}
	}
	if name == "" {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	rev, err := revisions.Get(r.Context(), s.DB, t.RevisionID)
	if err != nil {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	repo, err := repos.Get(r.Context(), s.DB, rev.RepoID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	role, _ := access.Effective(r.Context(), s.DB, u, repo.ID)
	done, err := applyFileOp(r.Context(), s, repo, rev, revisions.Caller{User: u, Role: role}, name, input)
	if err != nil {
		var cf *revisions.ErrConflict
		switch {
		case errors.Is(err, revisions.ErrForbidden):
			api.Error(w, r, api.Err(http.StatusForbidden, "forbidden", "You can't change this revision's pages right now."))
		case errors.As(err, &cf):
			api.Error(w, r, api.Err(http.StatusConflict, cf.Code, cf.Msg))
		default:
			api.Error(w, r, err)
		}
		return
	}
	m, _ := AddMessage(r.Context(), s.DB, t.ID, Message{Role: llm.RoleAssistant, RunID: acceptedPrefix + callID, Content: []llm.Block{{Type: llm.BlockText, Text: done + " (confirmed by " + u.Name + ")"}}})
	s.emit(t.ID, map[string]any{"kind": "message"})
	api.JSON(w, http.StatusOK, map[string]any{"message": view(m)})
}
