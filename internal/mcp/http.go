package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/orghttp"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/store"
)

type callKey struct{}

var initOnce sync.Mutex

func (s *Service) init() {
	initOnce.Lock()
	defer initOnce.Unlock()
	if s.lim == nil {
		s.lim = newLimiter(s.PerMinute, s.Concurrent)
		s.schema = sdk.NewSchemaCache()
	}
}

// Handler serves the Streamable HTTP transport at /mcp: stateless, JSON
// responses, agent keys as bearer tokens, rate limited per key.
func (s *Service) Handler(trusted []*net.IPNet) http.Handler {
	s.init()
	h := sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server {
		c, ok := r.Context().Value(callKey{}).(call)
		if !ok {
			return nil
		}
		return s.server(c)
	}, &sdk.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		// kmdn usually sits behind a reverse proxy on localhost that keeps
		// the public Host; bearer keys protect the endpoint, and browsers
		// are held to same-origin below.
		DisableLocalhostProtection: true,
		Logger:                     s.Log,
		MaxRequestBodyBytes:        1 << 20,
	})
	// Browsers may only call from kmdn's own origin (no CORS).
	protected := http.NewCrossOriginProtection().Handler(h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			unauthorized(w, "Send an agent key: Authorization: Bearer kmdn_ak_…")
			return
		}
		k, err := Authenticate(r.Context(), s.DB, strings.TrimSpace(token))
		if err != nil {
			var exp ErrExpired
			if errors.Is(err, ErrBadKey) || errors.Is(err, ErrRevoked) || errors.As(err, &exp) {
				unauthorized(w, err.Error())
				return
			}
			api.Error(w, r, err)
			return
		}
		release, retry, ok := s.lim.acquire(k.ID)
		if !ok {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(retry.Round(time.Second)/time.Second))))
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "Too many requests for this agent key. Slow down and retry."})
			return
		}
		defer release()
		ip := api.ClientIP(r, trusted)
		if s.lim.shouldTouch(k.ID) {
			if err := touch(r.Context(), s.DB, k.ID, ip); err != nil {
				s.Log.Error("touch agent key", "err", err)
			}
		}
		// A key belongs to one org: on Postgres its statements see only that org's rows.
		ctx := store.WithOrg(context.WithValue(r.Context(), callKey{}, call{key: k, ip: ip}), k.OrgID)
		protected.ServeHTTP(w, r.WithContext(ctx))
	})
}

func unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="kmdn"`)
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// OrgRoutes registers the org console's agent key endpoints (under
// /orgs/{org}).
func (s *Service) OrgRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(orghttp.RequireAdmin)
		r.Get("/admin/agent-keys", s.list)
		r.Post("/admin/agent-keys", s.create)
		r.Post("/admin/agent-keys/{id}/revoke", s.revoke)
		r.Get("/admin/agent-keys/{id}/calls", s.calls)
	})
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	keys, err := ListKeys(r.Context(), s.DB, orghttp.Current(r).ID)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": keys, "endpoint": strings.TrimRight(s.BaseURL, "/") + "/mcp"})
}

type createIn struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	AllRepos      bool     `json:"all_repos"`
	RepoIDs       []string `json:"repo_ids"`
	ExpiresInDays *int     `json:"expires_in_days"` // 30 | 90 | 365 | 0 (never); default 90
}

// Config is a ready-to-paste MCP client configuration for a key.
func (s *Service) Config(k Key, token string) string {
	type server struct {
		Type    string            `json:"type"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}
	b, _ := json.MarshalIndent(map[string]any{"mcpServers": map[string]server{"kmdn-" + slugify(k.Name): {
		Type: "http", URL: strings.TrimRight(s.BaseURL, "/") + "/mcp", Headers: map[string]string{"Authorization": "Bearer " + token},
	}}}, "", "  ")
	return string(b)
}

func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.TrimSuffix(b.String(), "-")
	if out == "" {
		return "docs"
	}
	return out
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	var in createIn
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 80 {
		api.Error(w, r, api.Invalid("name", "Name the key (up to 80 characters), e.g. after the agent that uses it."))
		return
	}
	if len(in.Description) > 500 {
		api.Error(w, r, api.Invalid("description", "Keep the description under 500 characters."))
		return
	}
	days := 90
	if in.ExpiresInDays != nil {
		days = *in.ExpiresInDays
	}
	switch days {
	case 0, 30, 90, 365:
	default:
		api.Error(w, r, api.Invalid("expires_in_days", "Keys expire after 30, 90 or 365 days, or never (0)."))
		return
	}
	if !in.AllRepos {
		if len(in.RepoIDs) == 0 {
			api.Error(w, r, api.Invalid("repo_ids", "Pick the repositories the key can read, or all of them."))
			return
		}
		list, err := repos.List(r.Context(), s.DB, in.RepoIDs, false)
		if err != nil {
			api.Error(w, r, err)
			return
		}
		inOrg := 0
		for _, rp := range list {
			if rp.OrgID == orghttp.Current(r).ID {
				inOrg++
			}
		}
		if inOrg != len(dedupe(in.RepoIDs)) {
			api.Error(w, r, api.Invalid("repo_ids", "One of these repositories doesn't exist in this organization."))
			return
		}
		in.RepoIDs = dedupe(in.RepoIDs)
	}
	k, token, err := CreateKey(r.Context(), s.DB, NewKey{OrgID: orghttp.Current(r).ID, Name: in.Name, Description: strings.TrimSpace(in.Description), AllRepos: in.AllRepos, RepoIDs: in.RepoIDs,
		ExpiresIn: time.Duration(days) * 24 * time.Hour, CreatedBy: p.User.ID})
	if err != nil {
		api.Error(w, r, err)
		return
	}
	k.CreatorName, k.Usage = p.User.Name, make([]int, 14)
	_ = audit.Write(r.Context(), s.DB, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, OrgID: k.OrgID, Action: "agent_key.created", TargetType: "agent_key", TargetID: k.ID,
		Data: map[string]any{"name": k.Name, "all_repos": k.AllRepos, "repo_ids": k.RepoIDs, "expires_in_days": days}})
	api.JSON(w, http.StatusCreated, map[string]any{"key": k, "token": token, "config": s.Config(k, token)})
}

func dedupe(ids []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func (s *Service) revoke(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	id := chi.URLParam(r, "id")
	if k, err := GetKey(r.Context(), s.DB, id); err != nil || k.OrgID != orghttp.Current(r).ID {
		api.Error(w, r, api.Err(http.StatusConflict, "not_active", "This key doesn't exist or is already revoked."))
		return
	}
	if err := RevokeKey(r.Context(), s.DB, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			api.Error(w, r, api.Err(http.StatusConflict, "not_active", "This key doesn't exist or is already revoked."))
			return
		}
		api.Error(w, r, err)
		return
	}
	_ = audit.Write(r.Context(), s.DB, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, OrgID: orghttp.Current(r).ID, Action: "agent_key.revoked", TargetType: "agent_key", TargetID: id})
	w.WriteHeader(http.StatusNoContent)
}

// calls lists a key's recent MCP calls from the audit log.
func (s *Service) calls(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if k, err := GetKey(r.Context(), s.DB, id); err != nil || k.OrgID != orghttp.Current(r).ID {
		api.Error(w, r, api.ErrNotFound)
		return
	}
	list, err := audit.ByActor(r.Context(), s.DB, audit.ActorAgentKey, id, 50)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"items": list})
}
