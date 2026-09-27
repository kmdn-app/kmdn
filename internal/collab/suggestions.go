package collab

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/docengine"
	"github.com/kmdn-app/kmdn/internal/realtime"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
)

// PageSuggestion is a pending suggestion and the page it's in.
type PageSuggestion struct {
	docengine.Suggestion
	Path string `json:"path"`
}

// Suggestions lists pending suggestions in the revision's pages (one page
// when p is set), in manifest then document order.
func (h *Hub) Suggestions(ctx context.Context, rev revisions.Revision, p string) ([]PageSuggestion, error) {
	h.init()
	var paths []string
	if p != "" {
		paths = []string{p}
	} else {
		files, err := revisions.Files(ctx, h.DB, rev.ID)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if f.Op != revisions.OpDelete && repos.IsMarkdown(f.Path) {
				paths = append(paths, f.Path)
			}
		}
	}
	out := []PageSuggestion{}
	for _, fp := range paths {
		_, state, err := h.stateOf(ctx, rev.ID, fp)
		if err != nil {
			return nil, err
		}
		if state == nil {
			continue
		}
		list, err := h.Engine.YSuggestions(ctx, state)
		if err != nil {
			return nil, err
		}
		for _, s := range list {
			out = append(out, PageSuggestion{Suggestion: s, Path: fp})
		}
	}
	return out, nil
}

// ResolveInput picks suggestions in a page: by id, by author, or all of them.
type ResolveInput struct {
	Path   string   `json:"path"`
	Action string   `json:"action"` // accept | reject
	IDs    []string `json:"ids,omitempty"`
	Author string   `json:"author,omitempty"`
}

// ResolveSuggestions accepts or rejects suggestions on c's behalf and returns
// how many it resolved. Revision editors and assigned reviewers can resolve
// any suggestion (editors too while In review); anyone else who can edit
// only their own.
func (h *Hub) ResolveSuggestions(ctx context.Context, repo repos.Repo, rev revisions.Revision, c revisions.Caller, in ResolveInput) (int, error) {
	h.init()
	if in.Action != "accept" && in.Action != "reject" {
		return 0, errors.New("collab: action is accept or reject")
	}
	acc, err := h.Revisions.AccessFor(ctx, rev, c)
	if err != nil {
		return 0, err
	}
	if !rev.State.Open() || !c.Role.AtLeast(access.Contributor) {
		return 0, revisions.ErrForbidden
	}
	room, err := h.room(ctx, repo, rev, in.Path, false, c.User.ID)
	if err != nil {
		return 0, err
	}
	state := room.merged(ctx)
	if state == nil {
		return 0, errors.New("collab: document unavailable")
	}
	list, err := h.Engine.YSuggestions(ctx, state)
	if err != nil {
		return 0, err
	}
	want := map[string]bool{}
	for _, id := range in.IDs {
		want[id] = true
	}
	var ids []string
	for _, s := range list {
		if len(want) > 0 && !want[s.ID] || in.Author != "" && s.Author != in.Author {
			continue
		}
		if !acc.CanResolve && s.Author != c.User.ID {
			return 0, revisions.ErrForbidden
		}
		ids = append(ids, s.ID)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	client := uint64(rand.Uint32())
	update, err := h.Engine.YResolveSuggestions(ctx, state, uint32(client), ids, in.Action == "accept")
	if err != nil {
		return 0, err
	}
	if len(update) > 2 {
		if err := room.ingest(ctx, update, []uint64{client}, c, nil, "human"); err != nil {
			return 0, err
		}
	}
	ev := "suggestions_accepted"
	if in.Action == "reject" {
		ev = "suggestions_rejected"
	}
	_ = revisions.Record(ctx, h.DB, rev.ID, revisions.ActorUser, c.User.ID, ev, map[string]any{"path": in.Path, "count": len(ids)})
	return len(ids), nil
}

// ApplyDoc turns a page into the JSON document (which may carry suggestion
// marks) as a change written on c's behalf; kind labels the writer's client.
func (h *Hub) ApplyDoc(ctx context.Context, repo repos.Repo, rev revisions.Revision, c revisions.Caller, p string, doc json.RawMessage, kind string) error {
	h.init()
	room, err := h.room(ctx, repo, rev, p, true, c.User.ID)
	if err != nil {
		return err
	}
	state := room.merged(ctx)
	if state == nil {
		return errors.New("collab: document unavailable")
	}
	client := uint64(rand.Uint32())
	update, err := h.Engine.YApplyDoc(ctx, state, doc, uint32(client))
	if err != nil {
		return err
	}
	if len(update) <= 2 {
		return nil
	}
	return room.ingest(ctx, update, []uint64{client}, c, nil, kind)
}

func cleanPagePath(p string) string {
	if u, err := url.PathUnescape(p); err == nil {
		p = u
	}
	return strings.TrimPrefix(path.Clean("/"+strings.TrimSpace(p)), "/")
}

func (h *Hub) listSuggestions(w http.ResponseWriter, r *http.Request) {
	rev, _, _, ok := h.load(w, r)
	if !ok {
		return
	}
	p := ""
	if q := r.URL.Query().Get("path"); q != "" {
		p = cleanPagePath(q)
	}
	list, err := h.Suggestions(r.Context(), rev, p)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": list})
}

func (h *Hub) resolveSuggestions(w http.ResponseWriter, r *http.Request) {
	rev, repo, c, ok := h.load(w, r)
	if !ok {
		return
	}
	var in ResolveInput
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	in.Path = cleanPagePath(in.Path)
	if in.Action != "accept" && in.Action != "reject" {
		api.Error(w, r, api.Invalid("action", "action is accept or reject."))
		return
	}
	if !repos.IsMarkdown(in.Path) {
		api.Error(w, r, api.Invalid("path", "Pick a page of the revision."))
		return
	}
	n, err := h.ResolveSuggestions(r.Context(), repo, rev, c, in)
	if err != nil {
		var je *realtime.JoinError
		switch {
		case errors.Is(err, revisions.ErrForbidden):
			api.Error(w, r, api.Err(http.StatusForbidden, "forbidden", "You can't resolve these suggestions."))
		case errors.As(err, &je):
			api.Error(w, r, api.ErrNotFound)
		default:
			api.Error(w, r, err)
		}
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"resolved": n})
}

// Suggest proposes markdown as a page's content, as suggestions by attrs
// (the assistant's edits, on c's behalf; the client is labeled kind).
func (h *Hub) Suggest(ctx context.Context, repo repos.Repo, rev revisions.Revision, c revisions.Caller, p, markdown string, attrs docengine.SuggestAttrs, kind string) (bool, error) {
	h.init()
	room, err := h.room(ctx, repo, rev, p, true, c.User.ID)
	if err != nil {
		return false, err
	}
	state := room.merged(ctx)
	if state == nil {
		return false, errors.New("collab: document unavailable")
	}
	client := uint64(rand.Uint32())
	update, err := h.Engine.YSuggest(ctx, state, markdown, attrs, uint32(client))
	if err != nil {
		return false, err
	}
	if len(update) <= 2 {
		return false, nil
	}
	return true, room.ingest(ctx, update, []uint64{client}, c, nil, kind)
}
