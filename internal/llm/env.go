package llm

import (
	"context"

	"github.com/kmdn-app/kmdn/internal/config"
	"github.com/kmdn-app/kmdn/internal/settings"
)

// Env is an AI provider fixed by the server config (assistant.provider and
// the keys next to it, or KMDN_ASSISTANT_* variables). It replaces the
// provider, key, base URL and models saved in the admin console, which shows
// them read-only; embeddings too when Embeddings.Model is set. Budgets and
// scan settings stay editable.
type Env struct {
	Provider string
	APIKey   string
	BaseURL  string
	// Models by task; tasks left out use the provider's defaults.
	Models     map[string]string
	Embeddings struct {
		BaseURL string
		APIKey  string
		Model   string
	}
}

// EnvFrom is the AI provider fixed by the server config, if any. kmdn serve
// and kmdn doctor both use it, so they see the same provider.
func EnvFrom(c config.Assistant) *Env {
	if c.Provider == "" && c.Embeddings.Model == "" {
		return nil
	}
	e := &Env{Provider: c.Provider, APIKey: c.APIKey, BaseURL: c.BaseURL,
		Models: map[string]string{TaskChat: c.Model, TaskReviewSummary: c.ReviewModel, TaskShortText: c.ShortModel}}
	e.Embeddings.BaseURL, e.Embeddings.APIKey, e.Embeddings.Model = c.Embeddings.BaseURL, c.Embeddings.APIKey, c.Embeddings.Model
	return e
}

// Managed reports whether the provider comes from the server config.
func (s *Service) Managed() bool { return s.managed() }

const openAIBaseURL = "https://api.openai.com/v1"

func (s *Service) managed() bool { return s.Env != nil && s.Env.Provider != "" }

func (s *Service) embeddingsManaged() bool { return s.Env != nil && s.Env.Embeddings.Model != "" }

// overlay applies the config's provider to the console's settings.
func (s *Service) overlay(st Settings) Settings {
	if s.managed() {
		e := s.Env
		st.Provider, st.BaseURL, st.KeyRef = e.Provider, e.BaseURL, ""
		if st.Provider == ProviderOpenAI && st.BaseURL == "" {
			st.BaseURL = openAIBaseURL
		}
		st.Models = map[string]string{}
		for task, m := range e.Models {
			if m != "" {
				st.Models[task] = m
			}
		}
	}
	if s.embeddingsManaged() {
		e := s.Env.Embeddings
		st.Embeddings.BaseURL, st.Embeddings.Model, st.Embeddings.KeyRef = e.BaseURL, e.Model, ""
		if st.Embeddings.BaseURL == "" {
			st.Embeddings.BaseURL = openAIBaseURL
		}
	}
	return st
}

// CheckEnv runs the capability check on the config's provider at startup
// and saves the result: the admin console shows it, and a failing key turns
// the assistant off until the next restart or "Save and check".
func (s *Service) CheckEnv(ctx context.Context) {
	if !s.managed() || s.Disabled {
		return
	}
	st, err := s.stored(ctx)
	if err != nil {
		s.Log.Error("ai settings", "err", err)
		return
	}
	c := s.RunCheck(ctx, s.overlay(st), "")
	st.Check = &c
	if err := settings.Set(ctx, s.DB, settingsKey, st); err != nil {
		s.Log.Error("ai settings", "err", err)
		return
	}
	if c.OK {
		s.Log.Info("ai provider from config", "provider", s.Env.Provider, "check", c.Message)
	} else {
		s.Log.Warn("ai provider from config failed its check; the assistant is off", "provider", s.Env.Provider, "check", c.Message)
	}
}
