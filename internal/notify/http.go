package notify

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/store"
)

// Routes registers the inbox, preferences and push endpoints.
func (s *Service) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Require)
		r.Get("/notifications", s.list)
		r.Post("/notifications/read", s.read)
		r.Get("/me/notification-prefs", s.getPrefs)
		r.Put("/me/notification-prefs", s.putPrefs)
		r.Get("/push/key", s.pushKey)
		r.Post("/me/push-subscriptions", s.subscribe)
		r.Post("/me/push-subscriptions/remove", s.unsubscribe)
	})
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	items, unread, err := List(r.Context(), s.DB, p.User.ID, r.URL.Query().Get("unread") == "true", 100)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": items, "unread": unread})
}

func (s *Service) read(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	var in struct {
		IDs []string `json:"ids"`
		All bool     `json:"all"`
	}
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	if !in.All && len(in.IDs) == 0 {
		api.Error(w, r, api.Invalid("ids", "Pass ids, or all."))
		return
	}
	if len(in.IDs) > 500 {
		api.Error(w, r, api.Invalid("ids", "Too many ids."))
		return
	}
	ids := in.IDs
	if in.All {
		ids = nil
	}
	if err := MarkRead(r.Context(), s.DB, p.User.ID, ids); err != nil {
		api.Error(w, r, err)
		return
	}
	if s.PublishUser != nil {
		s.PublishUser(p.User.ID, map[string]any{"type": "notification", "kind": "read"})
	}
	w.WriteHeader(http.StatusNoContent)
}

// Pref is a kind's delivery preference.
type Pref struct {
	Kind  string `json:"kind"`
	InApp bool   `json:"in_app"`
	Push  bool   `json:"push"`
}

func (s *Service) getPrefs(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	out := make([]Pref, 0, len(Kinds))
	for _, k := range Kinds {
		pr := s.prefsFor(r.Context(), p.User.ID, k)
		out = append(out, Pref{Kind: k, InApp: pr.inApp, Push: pr.push})
	}
	subs, _ := Subscriptions(r.Context(), s.DB, p.User.ID)
	api.JSON(w, http.StatusOK, map[string]any{"items": out, "push_available": s.Push != nil, "push_subscriptions": len(subs)})
}

func (s *Service) putPrefs(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	var in struct {
		Items []Pref `json:"items"`
	}
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	known := map[string]bool{}
	for _, k := range Kinds {
		known[k] = true
	}
	for _, pr := range in.Items {
		if !known[pr.Kind] {
			api.Error(w, r, api.Invalid("kind", "Unknown notification kind: "+pr.Kind))
			return
		}
		if _, err := store.Exec(r.Context(), s.DB, `INSERT INTO notification_prefs (user_id, kind, in_app, push) VALUES (?, ?, ?, ?)
			ON CONFLICT (user_id, kind) DO UPDATE SET in_app = excluded.in_app, push = excluded.push`, p.User.ID, pr.Kind, pr.InApp, pr.Push); err != nil {
			api.Error(w, r, err)
			return
		}
	}
	s.getPrefs(w, r)
}

func (s *Service) pushKey(w http.ResponseWriter, r *http.Request) {
	if s.Push == nil {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	keys, err := s.Push.Keys(r.Context())
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]string{"public_key": keys.Public})
}

type subscriptionInput struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func (s *Service) subscribe(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	var in subscriptionInput
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	if err := Subscribe(r.Context(), s.DB, p.User.ID, Subscription{Endpoint: in.Endpoint, P256dh: in.Keys.P256dh, Auth: in.Keys.Auth}, r.UserAgent()); err != nil {
		if errors.Is(err, errEndpoint) {
			api.Error(w, r, api.Invalid("endpoint", err.Error()))
			return
		}
		api.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) unsubscribe(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	var in subscriptionInput
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	if _, err := store.Exec(r.Context(), s.DB, `DELETE FROM push_subscriptions WHERE user_id = ? AND endpoint = ?`, p.User.ID, in.Endpoint); err != nil {
		api.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
