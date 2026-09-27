package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// OpenAI calls an OpenAI-compatible Chat Completions endpoint (OpenAI,
// OpenRouter, Ollama, vLLM, LM Studio…). Tool calling is required.
type OpenAI struct {
	BaseURL string // e.g. https://api.openai.com/v1 or http://localhost:11434/v1
	Key     string // optional for local servers
	HTTP    *http.Client
}

// Name implements Provider.
func (o *OpenAI) Name() string { return "openai" }

func (o *OpenAI) client() *http.Client {
	if o.HTTP != nil {
		return o.HTTP
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

type oaiToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    *string       `json:"content"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

func str(s string) *string { return &s }

// messages turns the conversation into OpenAI's shape: system first, tool
// results as role "tool" messages after the assistant turn that called them.
func (o *OpenAI) messages(req ChatRequest) []oaiMessage {
	var out []oaiMessage
	var sys []string
	for _, s := range req.System {
		sys = append(sys, s.Text)
	}
	if len(sys) > 0 {
		out = append(out, oaiMessage{Role: "system", Content: str(strings.Join(sys, "\n\n"))})
	}
	for _, m := range req.Messages {
		var text []string
		var calls []oaiToolCall
		var results []oaiMessage
		for _, b := range m.Content {
			switch b.Type {
			case BlockText:
				text = append(text, b.Text)
			case BlockToolUse:
				c := oaiToolCall{ID: b.ID, Type: "function"}
				c.Function.Name = b.Name
				c.Function.Arguments = string(b.Input)
				if c.Function.Arguments == "" {
					c.Function.Arguments = "{}"
				}
				calls = append(calls, c)
			case BlockToolResult:
				content := b.Content
				if b.IsError {
					content = "Error: " + content
				}
				results = append(results, oaiMessage{Role: "tool", ToolCallID: b.ToolUseID, Content: str(content)})
			}
		}
		if len(results) > 0 {
			out = append(out, results...)
		}
		if len(text) > 0 || len(calls) > 0 {
			msg := oaiMessage{Role: m.Role, ToolCalls: calls}
			if len(text) > 0 {
				msg.Content = str(strings.Join(text, ""))
			}
			out = append(out, msg)
		}
	}
	return out
}

// Stream implements Provider.
func (o *OpenAI) Stream(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error) {
	body := map[string]any{"model": req.Model, "messages": o.messages(req), "stream": true, "stream_options": map[string]any{"include_usage": true}}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, len(req.Tools))
		for i, t := range req.Tools {
			tools[i] = map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": t.Schema}}
		}
		body["tools"] = tools
		if req.ToolChoice != "" {
			body["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": req.ToolChoice}}
		}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(o.BaseURL, "/")+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	if o.Key != "" {
		r.Header.Set("Authorization", "Bearer "+o.Key)
	}
	resp, err := o.client().Do(r) //nolint:bodyclose // the streaming goroutine closes it
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, apiError(resp)
	}
	ch := make(chan ChatEvent, 16)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		calls := map[int]*oaiToolCall{}
		stop := ""
		err := sse(resp.Body, func(_, data string) error {
			if data == "[DONE]" {
				return nil
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content   string        `json:"content"`
						ToolCalls []oaiToolCall `json:"tool_calls"`
					} `json:"delta"`
					FinishReason string `json:"finish_reason"`
				} `json:"choices"`
				Usage *struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
					Details          struct {
						CachedTokens int `json:"cached_tokens"`
					} `json:"prompt_tokens_details"`
				} `json:"usage"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				return fmt.Errorf("llm: bad chunk: %w", err)
			}
			if chunk.Error != nil {
				return &APIError{Message: chunk.Error.Message}
			}
			for _, c := range chunk.Choices {
				if c.Delta.Content != "" {
					ch <- ChatEvent{Kind: EventText, Text: c.Delta.Content}
				}
				for _, tc := range c.Delta.ToolCalls {
					p := calls[tc.Index]
					if p == nil {
						p = &oaiToolCall{Index: tc.Index}
						calls[tc.Index] = p
					}
					if tc.ID != "" {
						p.ID = tc.ID
					}
					if tc.Function.Name != "" {
						p.Function.Name = tc.Function.Name
					}
					p.Function.Arguments += tc.Function.Arguments
				}
				if c.FinishReason != "" {
					stop = c.FinishReason
				}
			}
			if u := chunk.Usage; u != nil {
				ch <- ChatEvent{Kind: EventUsage, Usage: &Usage{InputTokens: u.PromptTokens - u.Details.CachedTokens, OutputTokens: u.CompletionTokens, CacheReadTokens: u.Details.CachedTokens}}
			}
			return nil
		})
		if err != nil {
			ch <- ChatEvent{Kind: EventError, Err: err}
			return
		}
		idx := make([]int, 0, len(calls))
		for i := range calls {
			idx = append(idx, i)
		}
		sort.Ints(idx)
		for _, i := range idx {
			c := calls[i]
			in := strings.TrimSpace(c.Function.Arguments)
			if in == "" {
				in = "{}"
			}
			if !json.Valid([]byte(in)) {
				ch <- ChatEvent{Kind: EventError, Err: fmt.Errorf("llm: the model sent invalid arguments for %s", c.Function.Name)}
				return
			}
			id := c.ID
			if id == "" {
				id = fmt.Sprintf("call_%d", i)
			}
			ch <- ChatEvent{Kind: EventToolUse, ToolUse: &Block{Type: BlockToolUse, ID: id, Name: c.Function.Name, Input: json.RawMessage(in)}}
		}
		switch stop {
		case "tool_calls", "function_call":
			stop = StopToolUse
		case "length":
			stop = StopMaxTokens
		case "stop", "":
			stop = StopEndTurn
			if len(calls) > 0 {
				stop = StopToolUse
			}
		}
		ch <- ChatEvent{Kind: EventStop, StopReason: stop}
	}()
	return ch, nil
}

// CountTokens implements Provider: OpenAI-compatible servers can't count,
// so it's an estimate.
func (o *OpenAI) CountTokens(_ context.Context, req ChatRequest) (int, error) {
	return estimate(req), nil
}
