// Package llm talks to language models: a provider-agnostic chat interface
// with streaming and tool use, implemented for Anthropic's Messages API and
// OpenAI-compatible endpoints (docs/specs/08-assistant.md#providers).
package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/kmdn-app/kmdn/internal/telemetry"
)

// Roles.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Block kinds.
const (
	BlockText       = "text"
	BlockToolUse    = "tool_use"
	BlockToolResult = "tool_result"
)

// Block is a piece of a message: text, a tool call, or a tool's result.
type Block struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

// Message is one turn.
type Message struct {
	Role    string  `json:"role"`
	Content []Block `json:"content"`
}

// Text makes a text message.
func Text(role, text string) Message {
	return Message{Role: role, Content: []Block{{Type: BlockText, Text: text}}}
}

// System is part of the system prompt; Cache marks a prompt-cache breakpoint
// after it (providers without caching ignore it).
type System struct {
	Text  string
	Cache bool
}

// Tool is a function the model may call, with a JSON Schema for its input.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"input_schema"`
}

// ChatRequest is one model call.
type ChatRequest struct {
	Model     string
	System    []System
	Messages  []Message
	Tools     []Tool
	MaxTokens int
	// ToolChoice forces a tool ("" lets the model choose).
	ToolChoice string
}

// Usage is what a call cost.
type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

// Add accumulates u2.
func (u *Usage) Add(u2 Usage) {
	u.InputTokens += u2.InputTokens
	u.OutputTokens += u2.OutputTokens
	u.CacheReadTokens += u2.CacheReadTokens
	u.CacheWriteTokens += u2.CacheWriteTokens
}

// Total counts every token (budgets).
func (u Usage) Total() int {
	return u.InputTokens + u.OutputTokens + u.CacheReadTokens + u.CacheWriteTokens
}

// Event kinds, in stream order: text deltas and tool calls, then usage and stop.
const (
	EventText    = "text"
	EventToolUse = "tool_use"
	EventUsage   = "usage"
	EventStop    = "stop"
	EventError   = "error"
)

// Stop reasons.
const (
	StopEndTurn   = "end_turn"
	StopToolUse   = "tool_use"
	StopMaxTokens = "max_tokens"
)

// ChatEvent is streamed from a call. The channel closes after EventStop or EventError.
type ChatEvent struct {
	Kind       string
	Text       string
	ToolUse    *Block
	Usage      *Usage
	StopReason string
	Err        error
}

// Provider is a model backend.
type Provider interface {
	Name() string
	Stream(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error)
	CountTokens(ctx context.Context, req ChatRequest) (int, error)
}

// Result is a call collected in full.
type Result struct {
	Content    []Block
	Usage      Usage
	StopReason string
}

// TextOf joins the text blocks.
func (r Result) TextOf() string {
	var b strings.Builder
	for _, c := range r.Content {
		if c.Type == BlockText {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

// Collect runs a call and gathers its events; onText (optional) sees deltas.
// Each call is a span and feeds the token and latency metrics.
func Collect(ctx context.Context, p Provider, req ChatRequest, onText func(string)) (res Result, err error) {
	ctx, span := telemetry.Tracer().Start(ctx, "llm "+p.Name(), trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("gen_ai.system", p.Name()), attribute.String("gen_ai.request.model", req.Model)))
	start := time.Now()
	defer func() {
		telemetry.LLMDuration.WithLabelValues(p.Name(), req.Model, telemetry.Outcome(err)).Observe(time.Since(start).Seconds())
		for typ, n := range map[string]int{"input": res.Usage.InputTokens, "output": res.Usage.OutputTokens, "cache_read": res.Usage.CacheReadTokens, "cache_write": res.Usage.CacheWriteTokens} {
			if n > 0 {
				telemetry.LLMTokens.WithLabelValues(p.Name(), req.Model, typ).Add(float64(n))
			}
		}
		span.SetAttributes(attribute.Int("gen_ai.usage.input_tokens", res.Usage.InputTokens), attribute.Int("gen_ai.usage.output_tokens", res.Usage.OutputTokens))
		telemetry.End(span, err)
	}()
	return collect(ctx, p, req, onText)
}

func collect(ctx context.Context, p Provider, req ChatRequest, onText func(string)) (Result, error) {
	ch, err := p.Stream(ctx, req)
	if err != nil {
		return Result{}, err
	}
	var r Result
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			r.Content = append(r.Content, Block{Type: BlockText, Text: text.String()})
			text.Reset()
		}
	}
	for ev := range ch {
		switch ev.Kind {
		case EventText:
			text.WriteString(ev.Text)
			if onText != nil {
				onText(ev.Text)
			}
		case EventToolUse:
			flush()
			r.Content = append(r.Content, *ev.ToolUse)
		case EventUsage:
			r.Usage.Add(*ev.Usage)
		case EventStop:
			r.StopReason = ev.StopReason
		case EventError:
			flush()
			return r, ev.Err
		}
	}
	flush()
	return r, nil
}

// APIError is an error response from a provider.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return "llm: " + e.Message }

// ErrNotConfigured means no provider is set up.
var ErrNotConfigured = errors.New("llm: no AI provider is configured")

func apiError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := strings.TrimSpace(string(b))
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
		msg = e.Error.Message
	}
	if msg == "" {
		msg = resp.Status
	}
	return &APIError{Status: resp.StatusCode, Message: msg}
}

// sse reads server-sent events, calling fn with each event's name and data.
func sse(r io.Reader, fn func(event, data string) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	var event string
	var data strings.Builder
	dispatch := func() error {
		if data.Len() == 0 {
			event = ""
			return nil
		}
		err := fn(event, strings.TrimSuffix(data.String(), "\n"))
		event = ""
		data.Reset()
		return err
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if err := dispatch(); err != nil {
				return err
			}
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			data.WriteByte('\n')
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return dispatch()
}

// estimate is a rough token count when a provider can't count (~4 characters a token).
func estimate(req ChatRequest) int {
	n := 0
	for _, s := range req.System {
		n += len(s.Text)
	}
	for _, m := range req.Messages {
		for _, b := range m.Content {
			n += len(b.Text) + len(b.Input) + len(b.Content)
		}
	}
	for _, t := range req.Tools {
		n += len(t.Description) + len(t.Schema)
	}
	return n/4 + 1
}
