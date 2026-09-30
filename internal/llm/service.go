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

	"github.com/kmdn-app/kmdn/internal/audit"
	"github.com/kmdn-app/kmdn/internal/events"
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

// DefaultBackgroundShare is Service.BackgroundShare's default.
const DefaultBackgroundShare = 0.8

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
	// OrgAllows reports whether an org has AI features on (its settings, its
	// plan); nil allows every org.
	OrgAllows func(ctx context.Context, orgID string) bool
	// BackgroundShare is the part of an org's monthly budget that work
	// nobody asked for right now (runs without a user: consistency checks,
	// change summaries, embeddings) may use; the rest is kept for people's
	// own requests. 0 means DefaultBackgroundShare.
	BackgroundShare float64
	// OrgBudget is an org's monthly token budget (0: none of its own).
	OrgBudget func(ctx context.Context, orgID string) int
	// Events receives the tokens each run used, per org.
	Events events.Sink
	// HTTP overrides the providers' client (tests).
	HTTP *http.Client
	Log  *slog.Logger
	// Override replaces the configured provider (tests).
	Override Provider
	// EmbedOverride replaces the embeddings endpoint (tests).
	EmbedOverride Embedder
	// Env is the provider fixed by the server config (env.go).
	Env *Env
}

// Settings loads the effective configuration: the console's, with the
// server config's provider on top (zero value when unset).
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	st, err := s.stored(ctx)
	return s.overlay(st), err
}

// stored loads the console's settings as saved.
func (s *Service) stored(ctx context.Context) (Settings, error) {
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

// EnabledForRepo reports whether AI features can run for a repository: a
// provider is set up and the repo's org has them on.
func (s *Service) EnabledForRepo(ctx context.Context, repoID string) bool {
	if !s.Enabled(ctx) {
		return false
	}
	if s.OrgAllows == nil {
		return true
	}
	var orgID string
	if err := store.QueryRow(ctx, s.DB, `SELECT org_id FROM repos WHERE id = ?`, repoID).Scan(&orgID); err != nil {
		return false
	}
	return s.OrgAllows(ctx, orgID)
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
	if err != nil || st.Provider == "" {
		return false
	}
	if s.managed() {
		return st.Check == nil || st.Check.OK // checked at startup (CheckEnv)
	}
	return st.Check != nil && st.Check.OK
}

func (s *Service) build(ctx context.Context, st Settings, key string) (Provider, error) {
	if s.Override != nil {
		return s.Override, nil
	}
	if key == "" && s.managed() {
		key = s.Env.APIKey
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
	return s.usedBy(ctx, since, userID, "")
}

// usedBy sums tokens since a time, for a user and/or an org when given.
func (s *Service) usedBy(ctx context.Context, since time.Time, userID, orgID string) (int, error) {
	q := `SELECT COALESCE(SUM(input_tokens + output_tokens + cache_read_tokens + cache_write_tokens), 0) FROM assistant_runs WHERE started_at >= ?`
	args := []any{store.Millis(since)}
	if userID != "" {
		q += ` AND user_id = ?`
		args = append(args, userID)
	}
	if orgID != "" {
		q += ` AND org_id = ?`
		args = append(args, orgID)
	}
	var n int
	err := store.QueryRow(ctx, s.DB, q, args...).Scan(&n)
	return n, err
}

// CheckBudget refuses a run when the user's daily budget, the monthly budget
// of the repo's org, or the instance's monthly budget is used up. Runs
// without a user (background work) stop earlier, at BackgroundShare of the
// org's budget, so the assistant stays available to people until the end.
func (s *Service) CheckBudget(ctx context.Context, userID, repoID string) error {
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
	if s.OrgBudget != nil && repoID != "" {
		var orgID string
		if err := store.QueryRow(ctx, s.DB, `SELECT org_id FROM repos WHERE id = ?`, repoID).Scan(&orgID); err == nil {
			if limit := s.OrgBudget(ctx, orgID); limit > 0 {
				n, err := s.usedBy(ctx, startOfMonth(now), "", orgID)
				if err != nil {
					return err
				}
				if n >= limit {
					return &ErrBudget{"This organization has used its AI budget for the month. It resets on the 1st (UTC)."}
				}
				if userID == "" && float64(n) >= float64(limit)*s.backgroundShare() {
					return &ErrBudget{"Background AI work (consistency checks, change summaries) is paused: this organization has used most of its AI budget for the month. It resumes on the 1st (UTC)."}
				}
			}
		}
	}
	return nil
}

func (s *Service) backgroundShare() float64 {
	if s.BackgroundShare > 0 && s.BackgroundShare <= 1 {
		return s.BackgroundShare
	}
	return DefaultBackgroundShare
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
	_, err := store.Exec(ctx, s.DB, `INSERT INTO assistant_runs (id, user_id, repo_id, revision_id, task, provider, model, status, started_at, org_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'running', ?, (SELECT org_id FROM repos WHERE id = ?))`,
		r.ID, nullable(r.UserID), nullable(r.RepoID), nullable(r.RevisionID), r.Task, r.Provider, r.Model, store.Millis(time.Now()), r.RepoID)
	return r.ID, err
}

// FinishRun records what a run used and how it ended.
func (s *Service) FinishRun(ctx context.Context, id string, u Usage, tools []string, status, errText string) error {
	if tools == nil {
		tools = []string{}
	}
	tj, _ := json.Marshal(tools)
	if _, err := store.Exec(ctx, s.DB, `UPDATE assistant_runs SET input_tokens = ?, output_tokens = ?, cache_read_tokens = ?, cache_write_tokens = ?, tools = ?, status = ?, error = ?, finished_at = ? WHERE id = ?`,
		u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens, string(tj), status, errText, store.Millis(time.Now()), id); err != nil {
		return err
	}
	// Assistant conversations are audited (tools called, tokens); summaries,
	// judgments and embeddings are only metered.
	var task, userID, repoID, revID, model, orgID string
	if err := store.QueryRow(ctx, s.DB, `SELECT task, COALESCE(user_id, ''), COALESCE(repo_id, ''), COALESCE(revision_id, ''), model, COALESCE(org_id, '') FROM assistant_runs WHERE id = ?`, id).
		Scan(&task, &userID, &repoID, &revID, &model, &orgID); err != nil {
		return err
	}
	if orgID != "" && u.Total() > 0 {
		s.Events.Emit(ctx, events.Event{Type: events.AIUsage, OrgID: orgID, UserID: userID, Data: map[string]any{"tokens": u.Total(), "task": task, "model": model, "run_id": id,
			"input_tokens": u.InputTokens, "output_tokens": u.OutputTokens, "cache_read_tokens": u.CacheReadTokens, "cache_write_tokens": u.CacheWriteTokens, "background": userID == ""}})
	}
	if task != TaskChat {
		return nil
	}
	return audit.Write(ctx, s.DB, audit.Entry{ActorType: audit.ActorAssistant, ActorID: userID, Action: "assistant.run", TargetType: "assistant_run", TargetID: id, RepoID: repoID,
		Data: map[string]any{"tools": tools, "tokens": u.Total(), "model": model, "status": status, "revision_id": revID}})
}

// Complete is a metered one-shot call for a task (titles, summaries).
func (s *Service) Complete(ctx context.Context, task string, r Run, req ChatRequest) (Result, error) {
	if err := s.CheckBudget(ctx, r.UserID, r.RepoID); err != nil {
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
	res, err := Collect(ctx, p, ChatRequest{Model: model, MaxTokens: 2000, Tools: []Tool{checkTool}, ToolChoice: checkTool.Name,
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
