package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// OpenAI's own API only allows function tools alongside reasoning through
// the Responses API (/v1/responses): Chat Completions refuses tools for
// reasoning models such as gpt-5.6-terra. kmdn uses it there, and keeps
// Chat Completions for OpenAI-compatible servers.

// responsesInput turns the conversation into Responses input items: plain
// messages, the assistant's function calls, and their outputs.
func responsesInput(req ChatRequest) []map[string]any {
	var out []map[string]any
	for _, m := range req.Messages {
		var text []string
		flush := func() {
			if len(text) > 0 {
				out = append(out, map[string]any{"role": m.Role, "content": strings.Join(text, "")})
				text = nil
			}
		}
		for _, b := range m.Content {
			switch b.Type {
			case BlockText:
				text = append(text, b.Text)
			case BlockToolUse:
				flush()
				args := string(b.Input)
				if args == "" {
					args = "{}"
				}
				out = append(out, map[string]any{"type": "function_call", "call_id": b.ID, "name": b.Name, "arguments": args})
			case BlockToolResult:
				flush()
				content := b.Content
				if b.IsError {
					content = "Error: " + content
				}
				out = append(out, map[string]any{"type": "function_call_output", "call_id": b.ToolUseID, "output": content})
			}
		}
		flush()
	}
	return out
}

func (o *OpenAI) streamResponses(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error) {
	body := map[string]any{"model": req.Model, "input": responsesInput(req), "stream": true, "store": false}
	var sys []string
	for _, s := range req.System {
		sys = append(sys, s.Text)
	}
	if len(sys) > 0 {
		body["instructions"] = strings.Join(sys, "\n\n")
	}
	if req.MaxTokens > 0 {
		// Reasoning counts against the cap: leave it room.
		body["max_output_tokens"] = req.MaxTokens + reasoningHeadroom
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, len(req.Tools))
		for i, t := range req.Tools {
			tools[i] = map[string]any{"type": "function", "name": t.Name, "description": t.Description, "parameters": t.Schema}
		}
		body["tools"] = tools
		if req.ToolChoice != "" {
			body["tool_choice"] = map[string]any{"type": "function", "name": req.ToolChoice}
		}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(o.BaseURL, "/")+"/responses", bytes.NewReader(b))
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
		type item struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}
		type response struct {
			Status            string `json:"status"`
			IncompleteDetails *struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Usage *struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
				InputDetails struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"input_tokens_details"`
			} `json:"usage"`
		}
		var ev struct {
			Type     string    `json:"type"`
			Delta    string    `json:"delta"`
			Item     *item     `json:"item"`
			Response *response `json:"response"`
			Message  string    `json:"message"`
		}
		calls, stop, done := 0, StopEndTurn, false
		err := sse(resp.Body, func(event, data string) error {
			if data == "[DONE]" {
				return nil
			}
			ev.Delta, ev.Item, ev.Response, ev.Message = "", nil, nil, ""
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				return fmt.Errorf("llm: bad event: %w", err)
			}
			if ev.Type == "" {
				ev.Type = event
			}
			switch ev.Type {
			case "response.output_text.delta":
				if ev.Delta != "" {
					ch <- ChatEvent{Kind: EventText, Text: ev.Delta}
				}
			case "response.output_item.done":
				// Function calls arrive whole here; argument deltas aren't needed.
				if it := ev.Item; it != nil && it.Type == "function_call" {
					in := strings.TrimSpace(it.Arguments)
					if in == "" {
						in = "{}"
					}
					if !json.Valid([]byte(in)) {
						return fmt.Errorf("llm: the model sent invalid arguments for %s", it.Name)
					}
					id := it.CallID
					if id == "" {
						id = fmt.Sprintf("call_%d", calls)
					}
					calls++
					ch <- ChatEvent{Kind: EventToolUse, ToolUse: &Block{Type: BlockToolUse, ID: id, Name: it.Name, Input: json.RawMessage(in)}}
				}
			case "response.completed", "response.incomplete":
				done = true
				if rs := ev.Response; rs != nil {
					if u := rs.Usage; u != nil {
						ch <- ChatEvent{Kind: EventUsage, Usage: &Usage{InputTokens: u.InputTokens - u.InputDetails.CachedTokens, OutputTokens: u.OutputTokens, CacheReadTokens: u.InputDetails.CachedTokens}}
					}
					if rs.Status == "incomplete" && rs.IncompleteDetails != nil && rs.IncompleteDetails.Reason == "max_output_tokens" {
						stop = StopMaxTokens
					}
				}
			case "response.failed":
				if rs := ev.Response; rs != nil && rs.Error != nil {
					return &APIError{Message: rs.Error.Message}
				}
				return &APIError{Message: "the response failed"}
			case "error":
				return &APIError{Message: ev.Message}
			}
			return nil
		})
		if err == nil && !done {
			err = fmt.Errorf("llm: the response stream ended early")
		}
		if err != nil {
			ch <- ChatEvent{Kind: EventError, Err: err}
			return
		}
		if calls > 0 && stop == StopEndTurn {
			stop = StopToolUse
		}
		ch <- ChatEvent{Kind: EventStop, StopReason: stop}
	}()
	return ch, nil
}
