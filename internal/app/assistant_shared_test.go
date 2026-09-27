package app

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/assistant"
	"github.com/kmdn-app/kmdn/internal/llm"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

// Two editors in one revision thread: a prompt sent while the other's run is
// in a tool call is seen by everyone, answered after that run, and doesn't
// break the tool call/result pairing the provider needs.
func TestAssistantSharedThreadMidRunPrompt(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	repoID := connectLocal(t, a, admin, map[string]string{"docs/policy.md": "# Equipment policy\n\n## Laptops\n\nEveryone gets a new laptop every three years.\n"})
	mk := func(email, name string, role access.Role) (users.User, *tc) {
		u, _ := users.Create(ctx, a.DB, email, name, false)
		_ = access.Grant(ctx, a.DB, repoID, "user", u.ID, role)
		c := &tc{t: t, base: admin.base, c: newClient()}
		signIn(t, a, c, u)
		return u, c
	}
	sam, samC := mk("sam@northwind.dev", "Sam", access.Contributor)
	luis, luisC := mk("luis@northwind.dev", "Luis", access.Maintainer)
	model := &scripted{}
	a.LLM.Override = model

	_, rev := samC.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Laptop refresh", "path": "docs/policy.md"})
	revID := rev["id"].(string)
	_, th := samC.do("GET", "/revisions/"+revID+"/assistant", nil)
	threadID := th["thread"].(map[string]any)["id"].(string)
	if _, lv := luisC.do("GET", "/revisions/"+revID+"/assistant", nil); lv["can_prompt"] != true {
		t.Fatalf("luis can't prompt: %v", lv)
	}

	// Hold Sam's run inside its tool call: its tool_use is stored, its
	// tool_result isn't yet.
	atTool, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	publish := a.Assistant.Publish
	a.Assistant.Publish = func(scope string, ev map[string]any) {
		publish(scope, ev)
		if ev["kind"] == "tool" {
			once.Do(func() {
				close(atTool)
				<-release
			})
		}
	}
	var luisReq llm.ChatRequest
	model.push(
		func(llm.ChatRequest) []llm.ChatEvent { return call("r1", "read_file", `{"path":"docs/policy.md"}`) },
		func(llm.ChatRequest) []llm.ChatEvent { return say("It says three years.") },
		func(req llm.ChatRequest) []llm.ChatEvent {
			luisReq = req
			return say("The FAQ doesn't exist yet.")
		},
	)
	if code, _ := samC.do("POST", "/assistant/threads/"+threadID+"/messages", map[string]any{"text": "What does the policy say?"}); code != 202 {
		t.Fatalf("sam's prompt: %d", code)
	}
	select {
	case <-atTool:
	case <-time.After(10 * time.Second):
		t.Fatal("sam's run never reached its tool call")
	}
	time.Sleep(2 * time.Millisecond) // a later millisecond than the tool_use
	code, posted := luisC.do("POST", "/assistant/threads/"+threadID+"/messages", map[string]any{"text": "Is there an FAQ?", "context": map[string]any{"path": "docs/faq.md"}})
	if code != 202 || posted["queued"] != true {
		t.Fatalf("luis's prompt: %d %v", code, posted)
	}
	time.Sleep(2 * time.Millisecond)
	close(release)
	a.Assistant.Wait()

	// Stored chronologically: Luis's prompt sits inside Sam's tool call.
	stored, _ := assistant.Messages(ctx, a.DB, threadID)
	var order []string
	for _, m := range stored {
		switch {
		case m.AuthorName != "":
			order = append(order, m.AuthorName)
		case len(m.Content) > 0 && m.Content[0].Type == llm.BlockToolUse:
			order = append(order, "use")
		case len(m.Content) > 0 && m.Content[0].Type == llm.BlockToolResult:
			order = append(order, "result")
		default:
			order = append(order, "reply")
		}
	}
	if strings.Join(order, ",") != "Sam,use,Luis,result,reply,reply" {
		t.Fatalf("stored order: %v", order)
	}

	// Everyone sees both people's messages and both answers.
	_, lv := samC.do("GET", "/revisions/"+revID+"/assistant", nil)
	var seen []string
	for _, m := range lv["messages"].([]any) {
		mm := m.(map[string]any)
		for _, p := range mm["parts"].([]any) {
			if pp := p.(map[string]any); pp["type"] == "text" {
				who, _ := mm["author_name"].(string)
				if who == "" {
					who = "assistant"
				}
				seen = append(seen, who+": "+pp["text"].(string))
			}
		}
	}
	want := "Sam: What does the policy say? | Luis: Is there an FAQ? | assistant: It says three years. | assistant: The FAQ doesn't exist yet."
	if strings.Join(seen, " | ") != want {
		t.Fatalf("thread:\n got %s\nwant %s", strings.Join(seen, " | "), want)
	}

	// Luis's run answered Luis, where Luis was, with a valid history.
	if len(model.reqs) != 3 {
		t.Fatalf("model calls: %d", len(model.reqs))
	}
	if !strings.Contains(luisReq.System[1].Text, "talking with Luis") || !strings.Contains(luisReq.System[1].Text, "reading docs/faq.md") {
		t.Fatalf("luis's turn: %s", luisReq.System[1].Text)
	}
	last := luisReq.Messages[len(luisReq.Messages)-1]
	if last.Role != llm.RoleUser || last.Content[0].Text != "Luis: Is there an FAQ?" {
		t.Fatalf("luis's run doesn't end with his prompt: %+v", last)
	}
	for i, m := range luisReq.Messages {
		for _, b := range m.Content {
			if b.Type != llm.BlockToolResult {
				continue
			}
			ok := false
			if i > 0 {
				for _, pb := range luisReq.Messages[i-1].Content {
					ok = ok || (pb.Type == llm.BlockToolUse && pb.ID == b.ToolUseID)
				}
			}
			if !ok {
				t.Fatalf("tool result %s at %d doesn't follow its call: %+v", b.ToolUseID, i, luisReq.Messages)
			}
		}
	}
	var runs int
	_ = store.QueryRow(ctx, a.DB, `SELECT COUNT(*) FROM assistant_runs WHERE user_id IN (?, ?) AND task = 'chat'`, sam.ID, luis.ID).Scan(&runs)
	if runs != 2 {
		t.Fatalf("runs: %d", runs)
	}
}
