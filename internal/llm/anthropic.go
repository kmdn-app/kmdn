package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// DefaultAnthropicURL is Anthropic's API.
const DefaultAnthropicURL = "https://api.anthropic.com"

// Anthropic calls the Messages API with streaming, tool use and prompt caching.
type Anthropic struct {
	BaseURL string
	Key     string
	HTTP    *http.Client
}

// Name implements Provider.
func (a *Anthropic) Name() string { return "anthropic" }

func (a *Anthropic) client() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

func (a *Anthropic) url(p string) string {
	base := strings.TrimRight(a.BaseURL, "/")
	if base == "" {
		base = DefaultAnthropicURL
	}
	return base + p
}

type antBlock struct {
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	Content      string          `json:"content,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
	CacheControl *struct {
		Type string `json:"type"`
	} `json:"cache_control,omitempty"`
}

func (a *Anthropic) body(req ChatRequest, stream bool) map[string]any {
	ephemeral := &struct {
		Type string `json:"type"`
	}{"ephemeral"}
	var system []antBlock
	for _, s := range req.System {
		b := antBlock{Type: "text", Text: s.Text}
		if s.Cache {
			b.CacheControl = ephemeral
		}
		system = append(system, b)
	}
	msgs := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		var blocks []antBlock
		for _, b := range m.Content {
			ab := antBlock{Type: b.Type, Text: b.Text, ID: b.ID, Name: b.Name, Input: b.Input, ToolUseID: b.ToolUseID, Content: b.Content, IsError: b.IsError}
			if b.Type == BlockToolUse && len(ab.Input) == 0 {
				ab.Input = json.RawMessage("{}")
			}
			blocks = append(blocks, ab)
		}
		msgs = append(msgs, map[string]any{"role": m.Role, "content": blocks})
	}
	body := map[string]any{"model": req.Model, "messages": msgs}
	if len(system) > 0 {
		body["system"] = system
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, len(req.Tools))
		for i, t := range req.Tools {
			tools[i] = map[string]any{"name": t.Name, "description": t.Description, "input_schema": t.Schema}
		}
		// Cache the tool definitions with the system prompt.
		tools[len(tools)-1]["cache_control"] = ephemeral
		body["tools"] = tools
		if req.ToolChoice != "" {
			body["tool_choice"] = map[string]any{"type": "tool", "name": req.ToolChoice}
		}
	}
	if stream {
		max := req.MaxTokens
		if max <= 0 {
			max = 4096
		}
		body["max_tokens"] = max
		body["stream"] = true
	}
	return body
}

func (a *Anthropic) post(ctx context.Context, path string, body any) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url(path), bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("x-api-key", a.Key)
	r.Header.Set("anthropic-version", "2023-06-01")
	resp, err := a.client().Do(r)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, apiError(resp)
	}
	return resp, nil
}

// Stream implements Provider.
func (a *Anthropic) Stream(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error) {
	resp, err := a.post(ctx, "/v1/messages", a.body(req, true)) //nolint:bodyclose // the streaming goroutine closes it
	if err != nil {
		return nil, err
	}
	ch := make(chan ChatEvent, 16)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		type pending struct {
			block Block
			json  strings.Builder
		}
		blocks := map[int]*pending{}
		stop := ""
		err := sse(resp.Body, func(event, data string) error {
			var ev struct {
				Type    string `json:"type"`
				Index   int    `json:"index"`
				Message struct {
					Usage antUsage `json:"usage"`
				} `json:"message"`
				ContentBlock struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"content_block"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
					StopReason  string `json:"stop_reason"`
				} `json:"delta"`
				Usage antUsage `json:"usage"`
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				return fmt.Errorf("llm: bad event %q: %w", event, err)
			}
			switch ev.Type {
			case "message_start":
				// Input and cache tokens come here; output tokens with message_delta (cumulative).
				u := ev.Message.Usage.usage()
				u.OutputTokens = 0
				ch <- ChatEvent{Kind: EventUsage, Usage: &u}
			case "content_block_start":
				if ev.ContentBlock.Type == "tool_use" {
					blocks[ev.Index] = &pending{block: Block{Type: BlockToolUse, ID: ev.ContentBlock.ID, Name: ev.ContentBlock.Name}}
				}
			case "content_block_delta":
				switch ev.Delta.Type {
				case "text_delta":
					ch <- ChatEvent{Kind: EventText, Text: ev.Delta.Text}
				case "input_json_delta":
					if p := blocks[ev.Index]; p != nil {
						p.json.WriteString(ev.Delta.PartialJSON)
					}
				}
			case "content_block_stop":
				if p := blocks[ev.Index]; p != nil {
					in := strings.TrimSpace(p.json.String())
					if in == "" {
						in = "{}"
					}
					p.block.Input = json.RawMessage(in)
					b := p.block
					ch <- ChatEvent{Kind: EventToolUse, ToolUse: &b}
					delete(blocks, ev.Index)
				}
			case "message_delta":
				if ev.Delta.StopReason != "" {
					stop = ev.Delta.StopReason
				}
				ch <- ChatEvent{Kind: EventUsage, Usage: &Usage{OutputTokens: ev.Usage.OutputTokens}}
			case "error":
				return &APIError{Message: ev.Error.Message}
			}
			return nil
		})
		if err != nil {
			ch <- ChatEvent{Kind: EventError, Err: err}
			return
		}
		ch <- ChatEvent{Kind: EventStop, StopReason: stop}
	}()
	return ch, nil
}

type antUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

func (u antUsage) usage() Usage {
	return Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CacheReadTokens: u.CacheReadInputTokens, CacheWriteTokens: u.CacheCreationInputTokens}
}

// CountTokens implements Provider.
func (a *Anthropic) CountTokens(ctx context.Context, req ChatRequest) (int, error) {
	body := a.body(req, false)
	delete(body, "tool_choice")
	resp, err := a.post(ctx, "/v1/messages/count_tokens", body)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var out struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return out.InputTokens, nil
}
