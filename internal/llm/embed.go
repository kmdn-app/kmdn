package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/kmdn-app/kmdn/internal/telemetry"
)

// Embedder turns texts into vectors (consistency checks).
type Embedder interface {
	// Embed returns one vector per text, in order, and the tokens used.
	Embed(ctx context.Context, model string, texts []string) ([][]float32, int, error)
}

// OpenAIEmbeddings calls an OpenAI-compatible /embeddings endpoint (OpenAI,
// Ollama, vLLM, LM Studio, Voyage-compatible gateways…). Anthropic has no
// embeddings API, so this is configured separately from the chat provider.
type OpenAIEmbeddings struct {
	BaseURL string
	Key     string
	HTTP    *http.Client
}

// Embed implements Embedder.
func (o *OpenAIEmbeddings) Embed(ctx context.Context, model string, texts []string) ([][]float32, int, error) {
	b, err := json.Marshal(map[string]any{"model": model, "input": texts})
	if err != nil {
		return nil, 0, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(o.BaseURL, "/")+"/embeddings", bytes.NewReader(b))
	if err != nil {
		return nil, 0, err
	}
	r.Header.Set("Content-Type", "application/json")
	if o.Key != "" {
		r.Header.Set("Authorization", "Bearer "+o.Key)
	}
	c := o.HTTP
	if c == nil {
		c = &http.Client{Timeout: 2 * time.Minute}
	}
	resp, err := c.Do(r)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, 0, apiError(resp)
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, 0, fmt.Errorf("llm: embeddings response: %w", err)
	}
	vecs := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(vecs) {
			return nil, 0, errors.New("llm: embeddings response has an out-of-range index")
		}
		vecs[d.Index] = d.Embedding
	}
	for _, v := range vecs {
		if len(v) == 0 {
			return nil, 0, errors.New("llm: embeddings response is missing vectors")
		}
	}
	return vecs, max(out.Usage.TotalTokens, out.Usage.PromptTokens), nil
}

// embedBatch is how many texts go in one request.
const embedBatch = 64

// TaskEmbeddings meters embedding calls in assistant_runs.
const TaskEmbeddings = "embeddings"

// EmbeddingsEnabled reports whether consistency checks can run: a chat
// provider that passed its check (for judging pairs) and an embeddings model.
func (s *Service) EmbeddingsEnabled(ctx context.Context) bool {
	if !s.Enabled(ctx) {
		return false
	}
	if s.EmbedOverride != nil {
		return true
	}
	st, err := s.Settings(ctx)
	return err == nil && st.Embeddings.BaseURL != "" && st.Embeddings.Model != ""
}

func (s *Service) embedder(ctx context.Context, st Settings, key string) (Embedder, string, error) {
	if s.EmbedOverride != nil {
		return s.EmbedOverride, "test-embeddings", nil
	}
	if st.Embeddings.BaseURL == "" || st.Embeddings.Model == "" {
		return nil, "", ErrNotConfigured
	}
	if key == "" && st.Embeddings.KeyRef != "" {
		b, err := s.Secrets.Get(ctx, s.DB, st.Embeddings.KeyRef)
		if err != nil {
			return nil, "", err
		}
		key = string(b)
	}
	return &OpenAIEmbeddings{BaseURL: st.Embeddings.BaseURL, Key: key, HTTP: s.HTTP}, st.Embeddings.Model, nil
}

// EmbeddingModel is the configured embeddings model ("" when off). Vectors
// are stored per model, so changing it re-embeds.
func (s *Service) EmbeddingModel(ctx context.Context) string {
	if s.EmbedOverride != nil {
		return "test-embeddings"
	}
	st, err := s.Settings(ctx)
	if err != nil {
		return ""
	}
	return st.Embeddings.Model
}

// Embed is a metered embeddings call, batched. It respects the instance's
// monthly budget (embeddings aren't charged to a person).
func (s *Service) Embed(ctx context.Context, r Run, texts []string) (_ [][]float32, err error) {
	if s.Disabled {
		return nil, ErrNotConfigured
	}
	if err := s.CheckBudget(ctx, ""); err != nil {
		return nil, err
	}
	st, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	e, model, err := s.embedder(ctx, st, "")
	if err != nil {
		return nil, err
	}
	r.Task, r.Provider, r.Model = TaskEmbeddings, "embeddings", model
	ctx, span := telemetry.Tracer().Start(ctx, "llm embeddings", trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("gen_ai.request.model", model), attribute.Int("kmdn.embeddings.texts", len(texts))))
	start := time.Now()
	defer func() {
		telemetry.LLMDuration.WithLabelValues("embeddings", model, telemetry.Outcome(err)).Observe(time.Since(start).Seconds())
	}()
	defer func() { telemetry.End(span, err) }()
	id, err := s.StartRun(ctx, r)
	if err != nil {
		return nil, err
	}
	out := make([][]float32, 0, len(texts))
	tokens := 0
	for start := 0; start < len(texts) && err == nil; start += embedBatch {
		var vecs [][]float32
		var n int
		vecs, n, err = e.Embed(ctx, model, texts[start:min(start+embedBatch, len(texts))])
		tokens += n
		out = append(out, vecs...)
	}
	status, msg := "done", ""
	if err != nil {
		status, msg = "error", err.Error()
	}
	_ = s.FinishRun(context.WithoutCancel(ctx), id, Usage{InputTokens: tokens}, nil, status, msg)
	telemetry.LLMTokens.WithLabelValues("embeddings", model, "input").Add(float64(tokens))
	if err != nil {
		return nil, err
	}
	return out, nil
}

// checkEmbeddings embeds a word with a configuration under test.
func (s *Service) checkEmbeddings(ctx context.Context, st Settings, key string) error {
	e, model, err := s.embedder(ctx, st, key)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, _, err = e.Embed(ctx, model, []string{"kmdn"})
	return err
}
