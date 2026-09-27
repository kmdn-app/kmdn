// Package assistant is kmdn's server-side agent: a provider-agnostic loop
// that answers with kmdn's own services as tools and never edits published
// content (docs/specs/08-assistant.md).
package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/docengine"
	"github.com/kmdn-app/kmdn/internal/links"
	"github.com/kmdn-app/kmdn/internal/llm"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/search"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// Limits per run.
var (
	MaxToolCalls = 25
	MaxRunTime   = 10 * time.Minute
	// HistoryMessages bounds how much of a thread goes to the model.
	HistoryMessages = 40
)

// Service runs the assistant.
type Service struct {
	DB        *store.DB
	LLM       *llm.Service
	Repos     *repos.Service
	Revisions *revisions.Service
	Search    *search.Index
	Links     *links.Service
	Engine    *docengine.Engine
	Docs      Docs
	// Publish sends live events (realtime.Hub.Publish).
	Publish func(scope string, event map[string]any)
	Log     *slog.Logger

	mu      sync.Mutex
	running map[string]bool // thread ids with a worker
	wg      sync.WaitGroup
}

// Scope is the realtime scope of a thread's events.
func Scope(threadID string) string { return "assistant:" + threadID }

func (s *Service) emit(threadID string, ev map[string]any) {
	if s.Publish != nil {
		ev["type"] = "assistant"
		ev["thread"] = threadID
		s.Publish(Scope(threadID), ev)
	}
}

// Wait blocks until running workers finish (tests, shutdown).
func (s *Service) Wait() { s.wg.Wait() }

// Running reports whether a thread has a run in progress.
func (s *Service) Running(threadID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running[threadID]
}

// kick starts the thread's worker unless one is running: it answers
// unanswered messages one run at a time (a prompt sent meanwhile is queued).
func (s *Service) kick(t Thread) {
	s.mu.Lock()
	if s.running == nil {
		s.running = map[string]bool{}
	}
	if s.running[t.ID] {
		s.mu.Unlock()
		s.emit(t.ID, map[string]any{"kind": "queued"})
		return
	}
	s.running[t.ID] = true
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.running, t.ID)
			s.mu.Unlock()
		}()
		ctx := context.Background()
		for i := 0; i < 5; i++ {
			msgs, err := Messages(ctx, s.DB, t.ID)
			if err != nil || len(msgs) == 0 || !msgs[len(msgs)-1].human() {
				return
			}
			s.run(ctx, t, msgs)
		}
	}()
}

// errorMessage records a failed run as an assistant reply.
func (s *Service) errorMessage(ctx context.Context, t Thread, runID, msg string) {
	_, _ = AddMessage(ctx, s.DB, t.ID, Message{Role: llm.RoleAssistant, RunID: runID, Content: []llm.Block{{Type: llm.BlockText, Text: msg}}})
	s.emit(t.ID, map[string]any{"kind": "error", "message": msg})
}

// run answers the last message.
func (s *Service) run(ctx context.Context, t Thread, msgs []Message) {
	last := msgs[len(msgs)-1]
	u, err := users.ByID(ctx, s.DB, last.AuthorID)
	if err != nil {
		s.errorMessage(ctx, t, "", "I can't tell who asked.")
		return
	}
	repo, err := repos.Get(ctx, s.DB, t.RepoID)
	if err != nil {
		return
	}
	role, _ := access.Effective(ctx, s.DB, u, repo.ID)
	if err := s.LLM.CheckBudget(ctx, u.ID); err != nil {
		var be *llm.ErrBudget
		if errors.As(err, &be) {
			s.errorMessage(ctx, t, "", be.Msg)
			return
		}
		s.errorMessage(ctx, t, "", "The assistant can't run right now.")
		return
	}
	p, model, err := s.LLM.For(ctx, llm.TaskChat)
	if err != nil {
		s.errorMessage(ctx, t, "", "The assistant isn't set up on this instance.")
		return
	}
	runID, err := s.LLM.StartRun(ctx, llm.Run{UserID: u.ID, RepoID: repo.ID, RevisionID: t.RevisionID, Task: llm.TaskChat, Provider: p.Name(), Model: model})
	if err != nil {
		return
	}
	s.emit(t.ID, map[string]any{"kind": "running", "run": runID})
	ctx, cancel := context.WithTimeout(ctx, MaxRunTime)
	defer cancel()

	tools := append([]llm.Tool{}, readTools...)
	e := env{s: s, repo: repo, caller: revisions.Caller{User: u, Role: role}}
	system := s.system(repo, u, role, last.Context)
	if t.RevisionID == "" {
		tools = append(tools, proposeTool)
		system[len(system)-1].Text += qaNote + "\n"
	} else {
		rev, err := revisions.Get(ctx, s.DB, t.RevisionID)
		if err != nil {
			s.errorMessage(ctx, t, runID, "This revision is gone.")
			_ = s.LLM.FinishRun(ctx, runID, llm.Usage{}, nil, "error", err.Error())
			return
		}
		e.rev = &rev
		tools = append(tools, writeTools...)
		system = append(system, llm.System{Text: s.revisionContext(ctx, rev)})
	}
	req := llm.ChatRequest{Model: model, System: system, Tools: tools, MaxTokens: 8192, Messages: conversation(msgs, t.RevisionID != "")}
	var usage llm.Usage
	var called []string
	status, errText := "done", ""
	for calls := 0; ; {
		res, err := llm.Collect(ctx, p, req, func(d string) { s.emit(t.ID, map[string]any{"kind": "delta", "run": runID, "text": d}) })
		usage.Add(res.Usage)
		if err != nil {
			status, errText = "error", err.Error()
			if len(res.Content) > 0 {
				_, _ = AddMessage(ctx, s.DB, t.ID, Message{Role: llm.RoleAssistant, RunID: runID, Content: res.Content})
			}
			msg := "Something went wrong talking to the model."
			if errors.Is(err, context.DeadlineExceeded) {
				msg = "That took too long, so I stopped."
			}
			s.errorMessage(ctx, t, runID, msg)
			break
		}
		if len(res.Content) == 0 {
			res.Content = []llm.Block{{Type: llm.BlockText, Text: ""}}
		}
		if _, err := AddMessage(ctx, s.DB, t.ID, Message{Role: llm.RoleAssistant, RunID: runID, Content: res.Content}); err != nil {
			status, errText = "error", err.Error()
			break
		}
		req.Messages = append(req.Messages, llm.Message{Role: llm.RoleAssistant, Content: res.Content})
		s.emit(t.ID, map[string]any{"kind": "message", "run": runID})
		if res.StopReason != llm.StopToolUse {
			break
		}
		var results []llm.Block
		stop := false
		for _, b := range res.Content {
			if b.Type != llm.BlockToolUse {
				continue
			}
			calls++
			called = append(called, b.Name)
			s.emit(t.ID, map[string]any{"kind": "tool", "run": runID, "name": b.Name, "label": toolLabel(b.Name, b.Input)})
			out, err := "", error(nil)
			if calls > MaxToolCalls {
				out, err = "", fmt.Errorf("%w: too many tool calls in this run", errTool)
				stop = true
			} else {
				out, err = e.run(ctx, b.Name, b.Input)
			}
			rb := llm.Block{Type: llm.BlockToolResult, ToolUseID: b.ID, Content: truncate(out, MaxToolResult)}
			if err != nil {
				rb.Content, rb.IsError = err.Error(), true
			}
			results = append(results, rb)
			if b.Name == proposeTool.Name {
				stop = true
			}
		}
		if _, err := AddMessage(ctx, s.DB, t.ID, Message{Role: llm.RoleUser, RunID: runID, Content: results}); err != nil {
			status, errText = "error", err.Error()
			break
		}
		req.Messages = append(req.Messages, llm.Message{Role: llm.RoleUser, Content: results})
		if stop {
			break
		}
	}
	_ = s.LLM.FinishRun(context.WithoutCancel(ctx), runID, usage, called, status, errText)
	s.emit(t.ID, map[string]any{"kind": "done", "run": runID})
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "\n… (truncated)"
	}
	return s
}

// conversation turns stored messages into the model's history: the most
// recent ones, starting at a person's message, without tool results whose
// call was cut off.
func conversation(msgs []Message, shared bool) []llm.Message {
	if len(msgs) > HistoryMessages {
		msgs = msgs[len(msgs)-HistoryMessages:]
	}
	for len(msgs) > 0 && !msgs[0].human() {
		msgs = msgs[1:]
	}
	out := make([]llm.Message, 0, len(msgs))
	for _, m := range msgs {
		content := m.Content
		if shared && m.human() && m.AuthorName != "" && len(content) > 0 {
			// Shared threads have several people: say who's talking.
			content = append([]llm.Block{}, content...)
			content[0].Text = m.AuthorName + ": " + content[0].Text
		}
		if len(content) == 0 {
			continue
		}
		out = append(out, llm.Message{Role: m.Role, Content: content})
	}
	return out
}

// system is the system prompt: stable instructions and repo facts first
// (cached), then this turn's context.
func (s *Service) system(repo repos.Repo, u users.User, role access.Role, cx Context) []llm.System {
	root := strings.Trim(repo.ContentRoot, "/")
	if root == "" {
		root = "the repository root"
	}
	stable := fmt.Sprintf(`You are the assistant of kmdn, a collaborative editor for documentation stored as markdown in git.
You work on the repository %s. Its pages are under %s. Published content is what's on the target branch.

How to answer:
- Look things up with the tools before answering questions about the content. Don't guess.
- Cite every fact you take from the repository with [[path#heading-slug]] (or [[path]] for a whole page) right after the sentence. Use the paths and slugs the tools return.
- When the pages don't say, say so plainly.
- Be concise. Use markdown sparingly: short paragraphs and lists.
- You never change published content yourself.`, repo.Slug, root)
	return []llm.System{{Text: stable, Cache: true}, {Text: s.turn(u, role, cx)}}
}

// qaNote tells a Q&A thread how to hand changes off.
const qaNote = "When they ask for a change, call propose_revision with a title, what will change and the pages; don't write out the edit first. They confirm and continue in the revision."

func (s *Service) turn(u users.User, role access.Role, cx Context) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You're talking with %s (%s role on this repository). Today is %s.\n", u.Name, role, time.Now().UTC().Format("Monday 2 January 2006"))
	if cx.Path != "" {
		fmt.Fprintf(&b, "They're reading %s.\n", cx.Path)
	}
	if cx.Selection != "" {
		fmt.Fprintf(&b, "They selected: %q\n", truncate(cx.Selection, 2000))
	}
	return b.String()
}

// Post adds a person's message and starts (or queues) a run.
func (s *Service) Post(ctx context.Context, t Thread, u users.User, text string, cx Context) (Message, error) {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 20_000 {
		return Message{}, errBadText
	}
	m, err := AddMessage(ctx, s.DB, t.ID, Message{Role: llm.RoleUser, AuthorID: u.ID, AuthorName: u.Name, Content: []llm.Block{{Type: llm.BlockText, Text: text}}, Context: cx})
	if err != nil {
		return m, err
	}
	s.emit(t.ID, map[string]any{"kind": "message"})
	s.kick(t)
	return m, nil
}

var errBadText = errors.New("assistant: write a message (up to 20,000 characters)")

// Proposal is a propose_revision call in a thread.
type Proposal struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Files       []string `json:"files"`
}

// findProposal returns a thread's propose_revision call by id.
func findProposal(msgs []Message, callID string) (Proposal, bool) {
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolUse && b.ID == callID && b.Name == proposeTool.Name {
				var p Proposal
				if json.Unmarshal(b.Input, &p) == nil {
					return p, true
				}
			}
		}
	}
	return Proposal{}, false
}

// revisionContext describes the revision a shared thread works in.
func (s *Service) revisionContext(ctx context.Context, rev revisions.Revision) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You're working in revision #%d “%s” (%s), shared with its collaborators: several people may write here; each message says who.\n", rev.Number, rev.Title, strings.ReplaceAll(string(rev.State), "_", " "))
	if files, err := revisions.Files(ctx, s.DB, rev.ID); err == nil && len(files) > 0 {
		b.WriteString("Pages in the revision:\n")
		for _, f := range files {
			if f.FromPath != "" {
				fmt.Fprintf(&b, "- %s (%s from %s)\n", f.Path, f.Op, f.FromPath)
			} else {
				fmt.Fprintf(&b, "- %s (%s)\n", f.Path, f.Op)
			}
		}
	}
	b.WriteString(`Everything you change is a suggestion people accept or reject; nothing is applied silently.
- Read a page before editing it. Use edit_file with small, exact find/replace edits (find must match the page's markdown exactly once); use new_markdown only to rewrite a page.
- create_file adds a page; rename_file and delete_file are proposals the person confirms.
- Say briefly what you changed and where when you're done.`)
	return b.String()
}
