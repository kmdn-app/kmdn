package collab

import (
	"net/http"
	"sort"

	"github.com/kmdn-app/kmdn/internal/api"
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
			u := peer.caller.User
			pu := byUser[u.ID]
			if pu == nil {
				pu = &PresenceUser{ID: u.ID, Name: u.Name}
				byUser[u.ID] = pu
			}
			if len(pu.Paths) == 0 || pu.Paths[len(pu.Paths)-1] != p {
				pu.Paths = append(pu.Paths, p)
			}
			peer.mu.Lock()
			pu.Editing = pu.Editing || peer.rw
			peer.mu.Unlock()
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
