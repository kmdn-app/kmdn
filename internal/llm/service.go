package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/secrets"
	"github.com/kmdn-app/kmdn/internal/settings"
	"github.com/kmdn-app/kmdn/internal/store"
)

// Tasks, each routed to its own model.
const (
	TaskChat          = "chat"
	TaskReviewSummary = "review_summary"
	TaskShortText     = "short_text"
)

// Tasks lists the routed tasks.
var Tasks = []string{TaskChat, TaskReviewSummary, TaskShortText}

// Providers.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
)

// DefaultModels per provider and task.
var DefaultModels = map[string]map[string]string{
	ProviderAnthropic: {TaskChat: "claude-sonnet-5", TaskReviewSummary: "claude-opus-5-5", TaskShortText: "claude-haiku-4-5-20251001"},
	ProviderOpenAI:    {},
}

// DefaultUserDailyTokens is the per-user daily budget.
const DefaultUserDailyTokens = 500_000

// Settings is the instance's AI configuration (the API keys are in the secrets store).
type Settings struct {
	Provider string            `json:"provider"` // "" when off
	BaseURL  string            `json:"base_url,omitempty"`
	KeyRef   string            `json:"key_ref,omitempty"`
	Models   map[string]string `json:"models"`
	// Embeddings: an OpenAI-compatible embeddings endpoint (consistency checks).
	Embeddings struct {
		BaseURL string `json:"base_url,omitempty"`
		KeyRef  string `json:"key_ref,omitempty"`
		Model   string `json:"model,omitempty"`
	} `json:"embeddings"`
	UserDailyTokens       int    `json:"user_daily_tokens"`       // 0: unlimited
	InstanceMonthlyTokens int    `json:"instance_monthly_tokens"` // 0: unlimited
	Check                 *Check `json:"check,omitempty"`
	// Consistency scans (docs/specs/08-assistant.md#repo-scan).
	Consistency struct {
		ScanEveryDays int `json:"scan_every_days"` // 0: only on "Run now"
		ScanMaxCalls  int `json:"scan_max_calls"`  // LLM judgments per scan
	} `json:"consistency"`
}

// Consistency scan defaults.
const (
	DefaultScanEveryDays = 7
	DefaultScanMaxCalls  = 500
)

// Check is the result of the capability check run on save.
type Check struct {
	OK      bool      `json:"ok"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

// Model returns the model for a task (the provider's default when unset).
func (st Settings) Model(task string) string {
	if m := strings.TrimSpace(st.Models[task]); m != "" {
		return m
	}
	if m := DefaultModels[st.Provider][task]; m != "" {
		return m
	}
	return strings.TrimSpace(st.Models[TaskChat])
}

const settingsKey = "ai"

// Service configures providers, routes tasks to models and meters usage.
type Service struct {
	DB      *store.DB
	Secrets *secrets.Store
	// Disabled turns the assistant off whatever the settings (assistant.enabled: false).
	Disabled bool
	// HTTP overrides the providers' client (tests).
	HTTP *http.Client
	Log  *slog.Logger
	// Override replaces the configured provider (tests).
	Override Provider
	// EmbedOverride replaces the embeddings endpoint (tests).
	EmbedOverride Embedder
}

// Settings loads the configuration (zero value when unset).
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	st := Settings{Models: map[string]string{}, UserDailyTokens: DefaultUserDailyTokens}
	st.Consistency.ScanEveryDays, st.Consistency.ScanMaxCalls = DefaultScanEveryDays, DefaultScanMaxCalls
	err := settings.Get(ctx, s.DB, settingsKey, &st)
	if err != nil && !settings.IsNotFound(err) {
		return st, err
	}
	if st.Models == nil {
		st.Models = map[string]string{}
	}
	return st, nil
}

// Enabled reports whether a provider is set up and passed its check.
func (s *Service) Enabled(ctx context.Context) bool {
	if s.Disabled {
		return false
	}
	if s.Override != nil {
		return true
	}
	st, err := s.Settings(ctx)
	return err == nil && st.Provider != "" && st.Check != nil && st.Check.OK
}

func (s *Service) build(ctx context.Context, st Settings, key string) (Provider, error) {
	if s.Override != nil {
		return s.Override, nil
	}
	if key == "" && st.KeyRef != "" {
		b, err := s.Secrets.Get(ctx, s.DB, st.KeyRef)
		if err != nil {
			return nil, err
		}
		key = string(b)
	}
	switch st.Provider {
	case ProviderAnthropic:
		return &Anthropic{BaseURL: st.BaseURL, Key: key, HTTP: s.HTTP}, nil
	case ProviderOpenAI:
		if st.BaseURL == "" {
			return nil, errors.New("llm: an OpenAI-compatible provider needs a base URL")
		}
		return &OpenAI{BaseURL: st.BaseURL, Key: key, HTTP: s.HTTP}, nil
	}
	return nil, ErrNotConfigured
}

// For returns the provider and model for a task.
func (s *Service) For(ctx context.Context, task string) (Provider, string, error) {
	if s.Disabled {
		return nil, "", ErrNotConfigured
	}
	st, err := s.Settings(ctx)
	if err != nil {
		return nil, "", err
	}
	if s.Override == nil && st.Provider == "" {
		return nil, "", ErrNotConfigured
	}
	p, err := s.build(ctx, st, "")
	if err != nil {
		return nil, "", err
	}
	model := st.Model(task)
	if model == "" && s.Override == nil {
		return nil, "", fmt.Errorf("llm: no model set for %s", task)
	}
	return p, model, nil
}

// ErrBudget means a token budget is used up.
type ErrBudget struct{ Msg string }

func (e *ErrBudget) Error() string { return e.Msg }

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
func startOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func (s *Service) used(ctx context.Context, since time.Time, userID string) (int, error) {
	q := `SELECT COALESCE(SUM(input_tokens + output_tokens + cache_read_tokens + cache_write_tokens), 0) FROM assistant_runs WHERE started_at >= ?`
	args := []any{store.Millis(since)}
	if userID != "" {
		q += ` AND user_id = ?`
		args = append(args, userID)
	}
	var n int
	err := store.QueryRow(ctx, s.DB, q, args...).Scan(&n)
	return n, err
}

// CheckBudget refuses a run when the user's daily or the instance's monthly budget is used up.
func (s *Service) CheckBudget(ctx context.Context, userID string) error {
	st, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if st.UserDailyTokens > 0 && userID != "" {
		n, err := s.used(ctx, startOfDay(now), userID)
		if err != nil {
			return err
		}
		if n >= st.UserDailyTokens {
			return &ErrBudget{"You've used today's assistant budget. It resets at midnight UTC."}
		}
	}
	if st.InstanceMonthlyTokens > 0 {
		n, err := s.used(ctx, startOfMonth(now), "")
		if err != nil {
			return err
		}
		if n >= st.InstanceMonthlyTokens {
			return &ErrBudget{"This instance has used its assistant budget for the month."}
		}
	}
	return nil
}

// Run is a metered use of a model.
type Run struct {
	ID         string
	UserID     string
	RepoID     string
	RevisionID string
	Task       string
	Provider   string
	Model      string
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// StartRun records a run starting.
func (s *Service) StartRun(ctx context.Context, r Run) (string, error) {
	if r.ID == "" {
		r.ID = ids.New("run")
	}
	_, err := store.Exec(ctx, s.DB, `INSERT INTO assistant_runs (id, user_id, repo_id, revision_id, task, provider, model, status, started_at) VALUES (?, ?, ?, ?, ?, ?, ?, 'running', ?)`,
		r.ID, nullable(r.UserID), nullable(r.RepoID), nullable(r.RevisionID), r.Task, r.Provider, r.Model, store.Millis(time.Now()))
	return r.ID, err
}

// FinishRun records what a run used and how it ended.
func (s *Service) FinishRun(ctx context.Context, id string, u Usage, tools []string, status, errText string) error {
	if tools == nil {
		tools = []string{}
	}
	tj, _ := json.Marshal(tools)
	_, err := store.Exec(ctx, s.DB, `UPDATE assistant_runs SET input_tokens = ?, output_tokens = ?, cache_read_tokens = ?, cache_write_tokens = ?, tools = ?, status = ?, error = ?, finished_at = ? WHERE id = ?`,
		u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens, string(tj), status, errText, store.Millis(time.Now()), id)
	return err
}

// Complete is a metered one-shot call for a task (titles, summaries).
func (s *Service) Complete(ctx context.Context, task string, r Run, req ChatRequest) (Result, error) {
	if err := s.CheckBudget(ctx, r.UserID); err != nil {
		return Result{}, err
	}
	p, model, err := s.For(ctx, task)
	if err != nil {
		return Result{}, err
	}
	req.Model, r.Task, r.Provider, r.Model = model, task, p.Name(), model
	id, err := s.StartRun(ctx, r)
	if err != nil {
		return Result{}, err
	}
	res, err := Collect(ctx, p, req, nil)
	status, msg := "done", ""
	if err != nil {
		status, msg = "error", err.Error()
	}
	_ = s.FinishRun(context.WithoutCancel(ctx), id, res.Usage, nil, status, msg)
	return res, err
}

var checkTool = Tool{Name: "report_status", Description: "Report that you can call tools.", Schema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean","description":"Always true."}},"required":["ok"]}`)}

// RunCheck verifies a configuration can stream and call tools (the
// assistant needs both), with the chat model.
func (s *Service) RunCheck(ctx context.Context, st Settings, key string) Check {
	c := Check{At: time.Now()}
	p, err := s.build(ctx, st, key)
	if err != nil {
		c.Message = err.Error()
		return c
	}
	model := st.Model(TaskChat)
	if model == "" {
		c.Message = "Set the chat model."
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	res, err := Collect(ctx, p, ChatRequest{Model: model, MaxTokens: 200, Tools: []Tool{checkTool}, ToolChoice: checkTool.Name,
		Messages: []Message{Text(RoleUser, "Call the report_status tool with ok set to true.")}}, nil)
	id, _ := s.StartRun(context.WithoutCancel(ctx), Run{Task: "capability_check", Provider: p.Name(), Model: model})
	_ = s.FinishRun(context.WithoutCancel(ctx), id, res.Usage, nil, map[bool]string{true: "error", false: "done"}[err != nil], errString(err))
	if err != nil {
		c.Message = err.Error()
		return c
	}
	for _, b := range res.Content {
		if b.Type == BlockToolUse && b.Name == checkTool.Name {
			c.OK, c.Message = true, fmt.Sprintf("%s answered and called a tool.", model)
			if st.Embeddings.BaseURL != "" && st.Embeddings.Model != "" {
				if err := s.checkEmbeddings(ctx, st, ""); err != nil {
					c.Message += " Embeddings failed: " + err.Error()
				} else {
					c.Message += fmt.Sprintf(" %s returned embeddings.", st.Embeddings.Model)
				}
			}
			return c
		}
	}
	c.Message = fmt.Sprintf("%s answered but didn't call a tool: the assistant needs tool calling.", model)
	return c
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
