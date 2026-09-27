package llm

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kmdn-app/kmdn/internal/api"
	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/settings"
	"github.com/kmdn-app/kmdn/internal/store"
)

// Routes registers the admin AI settings and the assistant status.
func (s *Service) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Get("/admin/ai", s.get)
		r.Put("/admin/ai", s.put)
		r.Post("/admin/ai/check", s.check)
		r.Get("/admin/ai/usage", s.usage)
	})
}

// OrgRoutes registers the assistant's status for an org (under /orgs/{org}).
func (s *Service) OrgRoutes(r chi.Router) {
	r.Get("/assistant/status", s.status)
}

// status says whether AI features are available in the org: set up on the
// instance and on in the org.
func (s *Service) status(w http.ResponseWriter, r *http.Request) {
	c, _ := orgs.FromContext(r.Context())
	on := s.OrgAllows == nil || s.OrgAllows(r.Context(), c.Org.ID)
	api.JSON(w, http.StatusOK, map[string]any{"enabled": on && s.Enabled(r.Context()), "consistency": on && s.EmbeddingsEnabled(r.Context())})
}

// view is the settings without secrets.
func (s *Service) view(st Settings) map[string]any {
	return map[string]any{
		"provider": st.Provider, "base_url": st.BaseURL, "key_set": st.KeyRef != "" || (s.managed() && s.Env.APIKey != ""),
		"models": st.Models, "defaults": DefaultModels, "tasks": Tasks,
		"embeddings": map[string]any{"base_url": st.Embeddings.BaseURL, "model": st.Embeddings.Model,
			"key_set": st.Embeddings.KeyRef != "" || (s.embeddingsManaged() && s.Env.Embeddings.APIKey != ""), "managed": s.embeddingsManaged()},
		"managed":                 s.managed(),
		"user_daily_tokens":       st.UserDailyTokens,
		"instance_monthly_tokens": st.InstanceMonthlyTokens,
		"consistency":             st.Consistency,
		"check":                   st.Check,
		"disabled":                s.Disabled,
	}
}

// get returns the effective settings (the server config's provider on top).
func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	st, err := s.Settings(r.Context())
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, s.view(st))
}

type putInput struct {
	Provider   string            `json:"provider"`
	BaseURL    string            `json:"base_url"`
	APIKey     *string           `json:"api_key"` // nil: keep; "": remove
	Models     map[string]string `json:"models"`
	Embeddings *struct {
		BaseURL string  `json:"base_url"`
		Model   string  `json:"model"`
		APIKey  *string `json:"api_key"`
	} `json:"embeddings"`
	UserDailyTokens       *int `json:"user_daily_tokens"`
	InstanceMonthlyTokens *int `json:"instance_monthly_tokens"`
	Consistency           *struct {
		ScanEveryDays *int `json:"scan_every_days"`
		ScanMaxCalls  *int `json:"scan_max_calls"`
	} `json:"consistency"`
}

// setKey stores, replaces or removes a secret and returns its ref.
func (s *Service) setKey(r *http.Request, ref string, key *string, kind string) (string, error) {
	switch {
	case key == nil:
		return ref, nil
	case *key == "":
		if ref != "" {
			if err := s.Secrets.Delete(r.Context(), s.DB, ref); err != nil {
				return ref, err
			}
		}
		return "", nil
	case ref != "":
		return ref, s.Secrets.Update(r.Context(), s.DB, ref, []byte(strings.TrimSpace(*key)))
	default:
		return s.Secrets.Put(r.Context(), s.DB, kind, []byte(strings.TrimSpace(*key)))
	}
}

func (s *Service) put(w http.ResponseWriter, r *http.Request) {
	var in putInput
	if err := api.Decode(r, &in); err != nil {
		api.Error(w, r, err)
		return
	}
	// Saved as the console's settings; fields the server config fixes are
	// left as they are.
	st, err := s.stored(r.Context())
	if err != nil {
		api.Error(w, r, err)
		return
	}
	if err := s.applyProvider(r, &st, in); err != nil {
		api.Error(w, r, err)
		return
	}
	if e := in.Embeddings; e != nil && !s.embeddingsManaged() {
		st.Embeddings.BaseURL, st.Embeddings.Model = strings.TrimSpace(e.BaseURL), strings.TrimSpace(e.Model)
		if st.Embeddings.KeyRef, err = s.setKey(r, st.Embeddings.KeyRef, e.APIKey, "ai_embeddings_key"); err != nil {
			api.Error(w, r, err)
			return
		}
	}
	if in.UserDailyTokens != nil && *in.UserDailyTokens >= 0 {
		st.UserDailyTokens = *in.UserDailyTokens
	}
	if in.InstanceMonthlyTokens != nil && *in.InstanceMonthlyTokens >= 0 {
		st.InstanceMonthlyTokens = *in.InstanceMonthlyTokens
	}
	if c := in.Consistency; c != nil {
		if c.ScanEveryDays != nil && *c.ScanEveryDays >= 0 {
			st.Consistency.ScanEveryDays = min(*c.ScanEveryDays, 365)
		}
		if c.ScanMaxCalls != nil && *c.ScanMaxCalls >= 0 {
			st.Consistency.ScanMaxCalls = min(*c.ScanMaxCalls, 100_000)
		}
	}
	st.Check = nil
	if eff := s.overlay(st); eff.Provider != "" {
		c := s.RunCheck(r.Context(), eff, "")
		st.Check = &c
	}
	if err := settings.Set(r.Context(), s.DB, settingsKey, st); err != nil {
		api.Error(w, r, err)
		return
	}
	// Secrets changed are recorded, never their values.
	p, _ := auth.FromContext(r.Context())
	eff := s.overlay(st)
	_ = audit.Write(r.Context(), s.DB, audit.Entry{ActorType: audit.ActorUser, ActorID: p.User.ID, Action: "ai.settings_updated", TargetType: "settings", TargetID: settingsKey,
		Data: map[string]any{"provider": eff.Provider, "key_changed": in.APIKey != nil && !s.managed(), "embeddings_key_changed": in.Embeddings != nil && in.Embeddings.APIKey != nil && !s.embeddingsManaged(), "check_ok": st.Check != nil && st.Check.OK}})
	api.JSON(w, http.StatusOK, s.view(eff))
}

// applyProvider sets the provider, base URL, key and models from the
// console, unless the server config fixes them.
func (s *Service) applyProvider(r *http.Request, st *Settings, in putInput) error {
	if s.managed() {
		return nil
	}
	switch in.Provider {
	case "", ProviderAnthropic, ProviderOpenAI:
	default:
		return api.Invalid("provider", "provider is anthropic, openai or empty (off).")
	}
	base := strings.TrimSpace(in.BaseURL)
	if base != "" && !strings.HasPrefix(base, "https://") && !strings.HasPrefix(base, "http://") {
		return api.Invalid("base_url", "Use an http or https URL.")
	}
	if in.Provider == ProviderOpenAI && base == "" {
		return api.Invalid("base_url", "An OpenAI-compatible provider needs its base URL (for example https://api.openai.com/v1).")
	}
	for task := range in.Models {
		if !contains(Tasks, task) {
			return api.Invalid("models", "Unknown task: "+task)
		}
	}
	st.Provider, st.BaseURL = in.Provider, base
	if in.Models != nil {
		st.Models = map[string]string{}
		for k, v := range in.Models {
			if v = strings.TrimSpace(v); v != "" {
				st.Models[k] = v
			}
		}
	}
	var err error
	st.KeyRef, err = s.setKey(r, st.KeyRef, in.APIKey, "ai_key")
	return err
}

func (s *Service) check(w http.ResponseWriter, r *http.Request) {
	st, err := s.stored(r.Context())
	if err != nil {
		api.Error(w, r, err)
		return
	}
	eff := s.overlay(st)
	if eff.Provider == "" {
		api.Error(w, r, api.Err(http.StatusConflict, "not_configured", "Pick a provider first."))
		return
	}
	c := s.RunCheck(r.Context(), eff, "")
	st.Check = &c
	if err := settings.Set(r.Context(), s.DB, settingsKey, st); err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, s.view(s.overlay(st)))
}

// UsageRow is tokens and runs for a user or a task.
type UsageRow struct {
	Key    string `json:"key"`
	Name   string `json:"name,omitempty"`
	Tokens int    `json:"tokens"`
	Runs   int    `json:"runs"`
}

func (s *Service) usage(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	today, err := s.used(r.Context(), startOfDay(now), "")
	if err != nil {
		api.Error(w, r, err)
		return
	}
	month, err := s.used(r.Context(), startOfMonth(now), "")
	if err != nil {
		api.Error(w, r, err)
		return
	}
	group := func(q string) ([]UsageRow, error) {
		rows, err := store.Query(r.Context(), s.DB, q, store.Millis(startOfMonth(now)))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []UsageRow{}
		for rows.Next() {
			var u UsageRow
			if err := rows.Scan(&u.Key, &u.Name, &u.Tokens, &u.Runs); err != nil {
				return nil, err
			}
			out = append(out, u)
		}
		return out, rows.Err()
	}
	sum := `COALESCE(SUM(a.input_tokens + a.output_tokens + a.cache_read_tokens + a.cache_write_tokens), 0)`
	byUser, err := group(`SELECT COALESCE(a.user_id, ''), COALESCE(u.name, ''), ` + sum + `, COUNT(*) FROM assistant_runs a LEFT JOIN users u ON u.id = a.user_id WHERE a.started_at >= ? GROUP BY a.user_id, u.name ORDER BY 3 DESC LIMIT 20`)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	byTask, err := group(`SELECT a.task, '', ` + sum + `, COUNT(*) FROM assistant_runs a WHERE a.started_at >= ? GROUP BY a.task ORDER BY 3 DESC`)
	if err != nil {
		api.Error(w, r, err)
		return
	}
	api.JSON(w, http.StatusOK, map[string]any{"today_tokens": today, "month_tokens": month, "by_user": byUser, "by_task": byTask})
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
