package collab

import (
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/store"
)

// PresenceUser is someone with a page of the revision open.
type PresenceUser struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Paths []string `json:"paths"`
	// Editing is true when they can edit (rw) in at least one open page.
	Editing bool `json:"editing"`
}

// Presence lists who has pages of a revision open, across all its rooms.
func (h *Hub) Presence(revID string) []PresenceUser {
	h.init()
	byUser := map[string]*PresenceUser{}
	for _, r := range h.liveRooms(revID) {
		r.mu.Lock()
		p := r.path
		for peer := range r.peers {
			peer.mu.Lock()
			u := peer.caller.User
			editing := peer.rw
			peer.mu.Unlock()
			pu := byUser[u.ID]
			if pu == nil {
				pu = &PresenceUser{ID: u.ID, Name: u.Name}
				byUser[u.ID] = pu
			}
			if len(pu.Paths) == 0 || pu.Paths[len(pu.Paths)-1] != p {
				pu.Paths = append(pu.Paths, p)
			}
			pu.Editing = pu.Editing || editing
		}
		r.mu.Unlock()
	}
	out := make([]PresenceUser, 0, len(byUser))
	for _, u := range byUser {
		sort.Strings(u.Paths)
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// publishPresence tells everyone following the revision who's in it now.
func (h *Hub) publishPresence(revID string) {
	if h.Publish == nil {
		return
	}
	h.Publish("revision:"+revID, map[string]any{"type": "presence", "revision": revID, "users": h.Presence(revID)})
}

func (h *Hub) presence(w http.ResponseWriter, r *http.Request) {
	rev, _, _, ok := h.load(w, r)
	if !ok {
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": h.Presence(rev.ID)})
}

// sourceMap returns the source map of a page's document: the text it was
// created from and its block ranges, so source mode can show the original
// bytes of blocks nobody changed. 204 when the page has no document yet.
func (h *Hub) sourceMap(w http.ResponseWriter, r *http.Request) {
	rev, _, _, ok := h.load(w, r)
	if !ok {
		return
	}
	p := chi.URLParam(r, "*")
	if u, err := url.PathUnescape(p); err == nil {
		p = u
	}
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	var sm string
	if err := store.QueryRow(r.Context(), h.DB, `SELECT source_map FROM ydocs WHERE revision_id = ? AND path = ?`, rev.ID, p).Scan(&sm); err != nil || sm == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write([]byte(sm))
}
