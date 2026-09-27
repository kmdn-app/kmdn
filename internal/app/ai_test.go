package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/config"
	"github.com/kmdn-app/kmdn/internal/llm"
	"github.com/kmdn-app/kmdn/internal/settings"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// fakeAnthropic answers every call with a text delta and, when the request
// offers tools and toolCalls is set, a call to the first one.
func fakeAnthropic(t *testing.T, toolCalls bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "sk-ant-test" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid x-api-key"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":20,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Sure."}}`,
			`{"type":"content_block_stop","index":0}`,
		}
		stop := "end_turn"
		if toolCalls {
			events = append(events,
				`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_x","name":"report_status","input":{}}}`,
				`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"ok\":true}"}}`,
				`{"type":"content_block_stop","index":1}`)
			stop = "tool_use"
		}
		events = append(events, fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q},"usage":{"output_tokens":5}}`, stop), `{"type":"message_stop"}`)
		for _, e := range events {
			fmt.Fprintf(w, "data: %s\n\n", e)
		}
	}))
}

func TestAIProviderAdmin(t *testing.T) {
	good := fakeAnthropic(t, true)
	defer good.Close()
	textOnly := fakeAnthropic(t, false)
	defer textOnly.Close()
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	sam, _ := users.Create(ctx, a.DB, "sam@northwind.dev", "Sam", false)
	samC := &tc{t: t, base: admin.base, c: newClient()}
	signIn(t, a, samC, sam)

	if _, st := samC.do("GET", "/orgs/default/assistant/status", nil); st["enabled"] != false {
		t.Fatalf("status before setup: %v", st)
	}
	if code, _ := samC.do("GET", "/admin/ai", nil); code != 403 {
		t.Fatalf("non-admin: %d", code)
	}
	if code, _ := admin.do("PUT", "/admin/ai", map[string]any{"provider": "openai"}); code != 422 {
		t.Fatalf("openai without base url: %d", code)
	}
	// A wrong key fails the check; the right one passes, with default models.
	_, v := admin.do("PUT", "/admin/ai", map[string]any{"provider": "anthropic", "base_url": good.URL, "api_key": "sk-wrong"})
	if c := v["check"].(map[string]any); c["ok"] != false || !strings.Contains(c["message"].(string), "invalid x-api-key") {
		t.Fatalf("wrong key: %v", v)
	}
	code, v := admin.do("PUT", "/admin/ai", map[string]any{"provider": "anthropic", "base_url": good.URL, "api_key": "sk-ant-test", "models": map[string]string{"short_text": "claude-haiku-4-5-20251001"}, "user_daily_tokens": 30})
	if code != 200 || v["check"].(map[string]any)["ok"] != true || v["key_set"] != true || v["api_key"] != nil {
		t.Fatalf("configure: %d %v", code, v)
	}
	if strings.Contains(fmt.Sprint(v), "sk-ant-test") {
		t.Fatal("the key came back")
	}
	if _, st := samC.do("GET", "/orgs/default/assistant/status", nil); st["enabled"] != true {
		t.Fatalf("status after setup: %v", st)
	}
	// Leaving the key out keeps it.
	if _, v := admin.do("PUT", "/admin/ai", map[string]any{"provider": "anthropic", "base_url": good.URL, "user_daily_tokens": 30}); v["check"].(map[string]any)["ok"] != true {
		t.Fatalf("kept key: %v", v)
	}
	// A model that can't call tools fails the check.
	if _, v := admin.do("PUT", "/admin/ai", map[string]any{"provider": "anthropic", "base_url": textOnly.URL, "user_daily_tokens": 30}); !strings.Contains(v["check"].(map[string]any)["message"].(string), "tool calling") {
		t.Fatalf("no tools: %v", v)
	}
	admin.do("PUT", "/admin/ai", map[string]any{"provider": "anthropic", "base_url": good.URL, "user_daily_tokens": 30})

	// Metered calls, and the daily budget (30 tokens: one call is 25).
	res, err := a.LLM.Complete(ctx, llm.TaskShortText, llm.Run{UserID: sam.ID}, llm.ChatRequest{Messages: []llm.Message{llm.Text(llm.RoleUser, "Title this.")}})
	if err != nil || res.TextOf() != "Sure." {
		t.Fatalf("complete: %v %+v", err, res)
	}
	var model string
	_ = store.QueryRow(ctx, a.DB, `SELECT model FROM assistant_runs WHERE user_id = ? AND task = 'short_text'`, sam.ID).Scan(&model)
	if model != "claude-haiku-4-5-20251001" {
		t.Fatalf("routed to %q", model)
	}
	if _, err := a.LLM.Complete(ctx, llm.TaskShortText, llm.Run{UserID: sam.ID}, llm.ChatRequest{Messages: []llm.Message{llm.Text(llm.RoleUser, "Again.")}}); err != nil {
		t.Fatalf("second call (25 < 30): %v", err)
	}
	var be *llm.ErrBudget
	if _, err := a.LLM.Complete(ctx, llm.TaskShortText, llm.Run{UserID: sam.ID}, llm.ChatRequest{Messages: []llm.Message{llm.Text(llm.RoleUser, "Once more.")}}); !errors.As(err, &be) {
		t.Fatalf("over budget: %v", err)
	}
	_, u := admin.do("GET", "/admin/ai/usage", nil)
	if u["month_tokens"].(float64) < 50 || len(u["by_user"].([]any)) == 0 || len(u["by_task"].([]any)) < 2 {
		t.Fatalf("usage: %v", u)
	}
}

// A provider fixed by the server config (KMDN_ASSISTANT_*) overrides the
// console's: it's checked at startup, shown read-only, and saving the form
// only changes budgets and scan settings.
func TestAIProviderFromConfig(t *testing.T) {
	good := fakeAnthropic(t, true)
	defer good.Close()
	a, admin := newApp(t, func(c *config.Config) {
		c.Assistant.Provider, c.Assistant.APIKey, c.Assistant.BaseURL, c.Assistant.Model = "anthropic", "sk-ant-test", good.URL, "claude-sonnet-5"
	})
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	if _, st := admin.do("GET", "/orgs/default/assistant/status", nil); st["enabled"] != true {
		t.Fatalf("status before the startup check: %v", st)
	}
	a.LLM.CheckEnv(ctx)
	_, v := admin.do("GET", "/admin/ai", nil)
	if v["managed"] != true || v["provider"] != "anthropic" || v["key_set"] != true || v["check"].(map[string]any)["ok"] != true || v["models"].(map[string]any)["chat"] != "claude-sonnet-5" {
		t.Fatalf("settings: %v", v)
	}
	code, v := admin.do("PUT", "/admin/ai", map[string]any{"provider": "openai", "base_url": "http://localhost:1/v1", "api_key": "sk-other", "user_daily_tokens": 42})
	if code != 200 || v["provider"] != "anthropic" || v["base_url"] != good.URL || v["user_daily_tokens"] != float64(42) || v["check"].(map[string]any)["ok"] != true {
		t.Fatalf("save: %d %v", code, v)
	}
	var stored llm.Settings
	if err := settings.Get(ctx, a.DB, "ai", &stored); err != nil || stored.Provider != "" || stored.KeyRef != "" || stored.UserDailyTokens != 42 {
		t.Fatalf("stored: %+v %v", stored, err)
	}
	if strings.Contains(fmt.Sprint(v), "sk-ant-test") {
		t.Fatal("the key came back")
	}

	// A key the provider refuses turns the assistant off at startup.
	a.LLM.Env.APIKey = "sk-wrong"
	a.LLM.CheckEnv(ctx)
	if _, st := admin.do("GET", "/orgs/default/assistant/status", nil); st["enabled"] != false {
		t.Fatalf("status with a refused key: %v", st)
	}
}
