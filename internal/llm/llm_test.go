package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sseWrite(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		fmt.Fprint(w, e, "\n\n")
		w.(http.Flusher).Flush()
	}
}

func TestAnthropicStream(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "sk-test" || r.Header.Get("anthropic-version") == "" || r.URL.Path != "/v1/messages" {
			t.Errorf("request: %s %v", r.URL.Path, r.Header)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		sseWrite(w,
			`event: message_start`+"\n"+`data: {"type":"message_start","message":{"usage":{"input_tokens":12,"output_tokens":1,"cache_read_input_tokens":100,"cache_creation_input_tokens":0}}}`,
			`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me "}}`,
			`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"look."}}`,
			`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
			`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file","input":{}}}`,
			`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
			`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"docs/a.md\"}"}}`,
			`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":1}`,
			`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":30}}`,
			`event: message_stop`+"\n"+`data: {"type":"message_stop"}`,
		)
	}))
	defer srv.Close()
	p := &Anthropic{BaseURL: srv.URL, Key: "sk-test"}
	var deltas []string
	res, err := Collect(context.Background(), p, ChatRequest{
		Model: "claude-sonnet-5", System: []System{{Text: "You are kmdn.", Cache: true}},
		Messages: []Message{Text(RoleUser, "What's in a.md?")},
		Tools:    []Tool{{Name: "read_file", Description: "Read a file", Schema: json.RawMessage(`{"type":"object"}`)}},
	}, func(s string) { deltas = append(deltas, s) })
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != StopToolUse || res.TextOf() != "Let me look." || strings.Join(deltas, "|") != "Let me |look." {
		t.Fatalf("result: %+v", res)
	}
	if len(res.Content) != 2 || res.Content[1].Name != "read_file" || string(res.Content[1].Input) != `{"path":"docs/a.md"}` || res.Content[1].ID != "toolu_1" {
		t.Fatalf("tool call: %+v", res.Content)
	}
	if res.Usage != (Usage{InputTokens: 12, OutputTokens: 30, CacheReadTokens: 100}) {
		t.Fatalf("usage: %+v", res.Usage)
	}
	// Cache breakpoints on the system prompt and the tools.
	sys := body["system"].([]any)[0].(map[string]any)
	tools := body["tools"].([]any)
	if sys["cache_control"] == nil || tools[len(tools)-1].(map[string]any)["cache_control"] == nil || body["stream"] != true || body["max_tokens"] == nil {
		t.Fatalf("body: %v", body)
	}
}

func TestAnthropicError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	defer srv.Close()
	_, err := (&Anthropic{BaseURL: srv.URL, Key: "nope"}).Stream(context.Background(), ChatRequest{Model: "m", Messages: []Message{Text(RoleUser, "hi")}})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 401 || ae.Message != "invalid x-api-key" {
		t.Fatalf("error: %v", err)
	}
}

func TestOpenAIStream(t *testing.T) {
	var body struct {
		Messages []map[string]any `json:"messages"`
		Tools    []map[string]any `json:"tools"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-local" || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request: %s %v", r.URL.Path, r.Header)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		sseWrite(w,
			`data: {"choices":[{"delta":{"content":"On it"}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"search","arguments":"{\"que"}}]}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ry\":\"laptop\"}"}}]}}]}`,
			`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			`data: {"choices":[],"usage":{"prompt_tokens":50,"completion_tokens":9,"prompt_tokens_details":{"cached_tokens":20}}}`,
			`data: [DONE]`,
		)
	}))
	defer srv.Close()
	p := &OpenAI{BaseURL: srv.URL + "/v1", Key: "sk-local"}
	res, err := Collect(context.Background(), p, ChatRequest{
		Model: "gpt-x", System: []System{{Text: "Be brief."}},
		Messages: []Message{
			Text(RoleUser, "Laptops?"),
			{Role: RoleAssistant, Content: []Block{{Type: BlockToolUse, ID: "call_0", Name: "search", Input: json.RawMessage(`{"query":"x"}`)}}},
			{Role: RoleUser, Content: []Block{{Type: BlockToolResult, ToolUseID: "call_0", Content: "nothing"}}},
		},
		Tools: []Tool{{Name: "search", Description: "Search", Schema: json.RawMessage(`{"type":"object"}`)}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != StopToolUse || res.TextOf() != "On it" || len(res.Content) != 2 || string(res.Content[1].Input) != `{"query":"laptop"}` || res.Content[1].ID != "call_a" {
		t.Fatalf("result: %+v", res)
	}
	if res.Usage != (Usage{InputTokens: 30, OutputTokens: 9, CacheReadTokens: 20}) {
		t.Fatalf("usage: %+v", res.Usage)
	}
	roles := []string{}
	for _, m := range body.Messages {
		roles = append(roles, m["role"].(string))
	}
	if strings.Join(roles, ",") != "system,user,assistant,tool" || body.Messages[3]["tool_call_id"] != "call_0" || body.Tools[0]["type"] != "function" {
		t.Fatalf("messages: %v", body.Messages)
	}
}

func TestOpenAIMaxTokens(t *testing.T) {
	for base, want := range map[string]string{
		"https://api.openai.com/v1":    "max_completion_tokens=4496",
		"http://localhost:11434/v1":    "max_tokens=400",
		"https://openrouter.ai/api/v1": "max_tokens=400",
	} {
		k, v := (&OpenAI{BaseURL: base}).maxTokens(400)
		if got := fmt.Sprintf("%s=%d", k, v); got != want {
			t.Errorf("%s: %s, want %s", base, got, want)
		}
	}
}

func TestSSEMultiline(t *testing.T) {
	var got []string
	err := sse(strings.NewReader("event: a\ndata: one\ndata: two\n\n: comment\ndata: three\n\n"), func(ev, data string) error {
		got = append(got, ev+"="+data)
		return nil
	})
	if err != nil || strings.Join(got, "|") != "a=one\ntwo|=three" {
		t.Fatalf("sse: %q %v", got, err)
	}
}

func TestModelRouting(t *testing.T) {
	st := Settings{Provider: ProviderAnthropic, Models: map[string]string{TaskShortText: "claude-haiku-custom"}}
	if st.Model(TaskChat) != "claude-sonnet-5" || st.Model(TaskShortText) != "claude-haiku-custom" || st.Model(TaskReviewSummary) != "claude-opus-5-5" {
		t.Fatalf("routing: %s %s %s", st.Model(TaskChat), st.Model(TaskShortText), st.Model(TaskReviewSummary))
	}
	oa := Settings{Provider: ProviderOpenAI, Models: map[string]string{TaskChat: "llama3.3"}}
	if oa.Model(TaskShortText) != "llama3.3" {
		t.Fatalf("fallback to the chat model: %s", oa.Model(TaskShortText))
	}
}
