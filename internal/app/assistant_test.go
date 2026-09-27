package app

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/llm"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// scripted is a fake model: each call answers with the next step.
type scripted struct {
	mu    sync.Mutex
	steps []func(req llm.ChatRequest) []llm.ChatEvent
	reqs  []llm.ChatRequest
}

func (s *scripted) Name() string { return "fake" }

func (s *scripted) CountTokens(context.Context, llm.ChatRequest) (int, error) { return 1, nil }

func (s *scripted) Stream(_ context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	var evs []llm.ChatEvent
	if len(s.steps) > 0 {
		evs = s.steps[0](req)
		s.steps = s.steps[1:]
	} else {
		evs = say("(no more steps)")
	}
	s.mu.Unlock()
	ch := make(chan llm.ChatEvent, len(evs)+2)
	for _, e := range evs {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func (s *scripted) push(steps ...func(llm.ChatRequest) []llm.ChatEvent) {
	s.mu.Lock()
	s.steps = append(s.steps, steps...)
	s.mu.Unlock()
}

func say(text string) []llm.ChatEvent {
	return []llm.ChatEvent{{Kind: llm.EventText, Text: text}, {Kind: llm.EventUsage, Usage: &llm.Usage{InputTokens: 100, OutputTokens: 20}}, {Kind: llm.EventStop, StopReason: llm.StopEndTurn}}
}

func call(id, name, input string) []llm.ChatEvent {
	return []llm.ChatEvent{{Kind: llm.EventToolUse, ToolUse: &llm.Block{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(input)}}, {Kind: llm.EventUsage, Usage: &llm.Usage{InputTokens: 80, OutputTokens: 10}}, {Kind: llm.EventStop, StopReason: llm.StopToolUse}}
}

func TestAssistantQA(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	repoID := connectLocal(t, a, admin, map[string]string{"docs/policy.md": "# Equipment policy\n\n## Laptops\n\nEveryone gets a new laptop every three years.\n"})
	sam, _ := users.Create(ctx, a.DB, "sam@northwind.dev", "Sam", false)
	_ = access.Grant(ctx, a.DB, repoID, "user", sam.ID, access.Contributor)
	samC := &tc{t: t, base: admin.base, c: newClient()}
	signIn(t, a, samC, sam)
	luis, _ := users.Create(ctx, a.DB, "luis@northwind.dev", "Luis", false)
	_ = access.Grant(ctx, a.DB, repoID, "user", luis.ID, access.Viewer)
	luisC := &tc{t: t, base: admin.base, c: newClient()}
	signIn(t, a, luisC, luis)

	if code, _ := samC.do("POST", "/repos/"+repoID+"/assistant/threads", map[string]any{"text": "hi"}); code != 409 {
		t.Fatalf("assistant off: %d", code)
	}
	model := &scripted{}
	a.LLM.Override = model
	model.push(
		func(llm.ChatRequest) []llm.ChatEvent { return call("t1", "search", `{"query":"laptop"}`) },
		func(req llm.ChatRequest) []llm.ChatEvent {
			return say("Every three years [[docs/policy.md#laptops]].")
		},
	)
	code, created := samC.do("POST", "/repos/"+repoID+"/assistant/threads", map[string]any{"text": "How often do we get laptops?", "context": map[string]any{"path": "docs/policy.md"}})
	if code != 201 {
		t.Fatalf("create: %d %v", code, created)
	}
	threadID := created["thread"].(map[string]any)["id"].(string)
	a.Assistant.Wait()

	_, th := samC.do("GET", "/assistant/threads/"+threadID, nil)
	msgs := th["messages"].([]any)
	var parts []string
	for _, m := range msgs {
		for _, p := range m.(map[string]any)["parts"].([]any) {
			pp := p.(map[string]any)
			parts = append(parts, pp["type"].(string)+":"+firstNonEmpty(pp["text"], pp["label"]))
		}
	}
	if strings.Join(parts, " | ") != "text:How often do we get laptops? | tool:Searching for “laptop” | text:Every three years [[docs/policy.md#laptops]]." {
		t.Fatalf("thread: %v", parts)
	}
	// The model saw the search results and where Sam was reading.
	second := model.reqs[1]
	last := second.Messages[len(second.Messages)-1]
	if last.Content[0].Type != llm.BlockToolResult || !strings.Contains(last.Content[0].Content, "docs/policy.md#laptops") {
		t.Fatalf("tool result: %+v", last)
	}
	if !strings.Contains(second.System[1].Text, "reading docs/policy.md") || !second.System[0].Cache || !strings.Contains(second.System[0].Text, "[[path#heading-slug]]") {
		t.Fatalf("system prompt: %+v", second.System)
	}
	var tools string
	_ = store.QueryRow(ctx, a.DB, `SELECT tools FROM assistant_runs WHERE user_id = ? AND task = 'chat'`, sam.ID).Scan(&tools)
	if tools != `["search"]` {
		t.Fatalf("run tools: %s", tools)
	}
	// Q&A threads are private.
	if code, _ := luisC.do("GET", "/assistant/threads/"+threadID, nil); code != 404 {
		t.Fatalf("someone else's thread: %d", code)
	}
	if _, list := samC.do("GET", "/repos/"+repoID+"/assistant/threads", nil); len(list["items"].([]any)) != 1 {
		t.Fatalf("list: %v", list)
	}

	// Asking for a change proposes a revision; accepting starts it and moves the conversation.
	model.push(func(llm.ChatRequest) []llm.ChatEvent {
		return call("t2", "read_file", `{"path":"docs/policy.md","from_heading":"Laptops"}`)
	}, func(req llm.ChatRequest) []llm.ChatEvent {
		r := req.Messages[len(req.Messages)-1].Content[0].Content
		if !strings.Contains(r, "## Laptops") || !strings.Contains(r, "Outline:") {
			t.Errorf("read_file result: %s", r)
		}
		return call("t3", "propose_revision", `{"title":"Laptops every four years","description":"Change the refresh cycle to four years.","files":["docs/policy.md"]}`)
	})
	samC.do("POST", "/assistant/threads/"+threadID+"/messages", map[string]any{"text": "Make it four years."})
	a.Assistant.Wait()
	_, th = samC.do("GET", "/assistant/threads/"+threadID, nil)
	var callID string
	for _, m := range th["messages"].([]any) {
		for _, p := range m.(map[string]any)["parts"].([]any) {
			if pp := p.(map[string]any); pp["type"] == "proposal" {
				callID = pp["call_id"].(string)
			}
		}
	}
	if callID == "" {
		t.Fatalf("no proposal: %v", th["messages"])
	}
	if len(model.steps) != 0 {
		t.Fatal("the run went on after proposing")
	}
	code, acc := samC.do("POST", "/assistant/threads/"+threadID+"/proposals/"+callID+"/accept", nil)
	if code != 201 || acc["revision"].(map[string]any)["title"] != "Laptops every four years" {
		t.Fatalf("accept: %d %v", code, acc)
	}
	revID := acc["revision"].(map[string]any)["id"].(string)
	if _, files := samC.do("GET", "/revisions/"+revID+"/files", nil); len(files["items"].([]any)) != 1 {
		t.Fatalf("revision files: %v", files)
	}
	shared := acc["thread"].(map[string]any)["id"].(string)
	if _, st := luisC.do("GET", "/assistant/threads/"+shared, nil); len(st["messages"].([]any)) < 4 {
		t.Fatalf("shared thread (visible to the repo): %v", st)
	}
	// Viewers read the revision thread but can't prompt.
	if code, _ := luisC.do("POST", "/assistant/threads/"+shared+"/messages", map[string]any{"text": "hello"}); code != 403 {
		t.Fatalf("viewer prompt: %d", code)
	}

	// Over budget: an explanation instead of a run.
	admin.do("PUT", "/admin/ai", map[string]any{"user_daily_tokens": 10})
	samC.do("POST", "/assistant/threads/"+threadID+"/messages", map[string]any{"text": "One more?"})
	a.Assistant.Wait()
	_, th = samC.do("GET", "/assistant/threads/"+threadID, nil)
	ms := th["messages"].([]any)
	lastText := ms[len(ms)-1].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(lastText, "budget") {
		t.Fatalf("budget: %q", lastText)
	}
}

func firstNonEmpty(vs ...any) string {
	for _, v := range vs {
		if s, _ := v.(string); s != "" {
			return s
		}
	}
	return ""
}
