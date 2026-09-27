package app

import (
	"context"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/llm"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestAssistantRevisionEdits(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	repoID := connectLocal(t, a, admin, map[string]string{
		"docs/policy.md": "# Equipment policy\n\n## Laptops\n\nEveryone gets a new laptop every three years.\n",
		"docs/old.md":    "# Old page\n\nNot needed.\n",
	})
	mk := func(email, name string, role access.Role) (users.User, *tc) {
		u, _ := users.Create(ctx, a.DB, email, name, false)
		_ = access.Grant(ctx, a.DB, repoID, "user", u.ID, role)
		c := &tc{t: t, base: admin.base, c: newClient()}
		signIn(t, a, c, u)
		return u, c
	}
	sam, samC := mk("sam@northwind.dev", "Sam", access.Contributor)
	_, luisC := mk("luis@northwind.dev", "Luis", access.Viewer)
	a.Collab.Options.QuietPeriod = 0
	model := &scripted{}
	a.LLM.Override = model

	_, rev := samC.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Laptop refresh", "path": "docs/policy.md"})
	revID := rev["id"].(string)
	code, th := samC.do("GET", "/revisions/"+revID+"/assistant", nil)
	if code != 200 || th["can_prompt"] != true {
		t.Fatalf("revision thread: %d %v", code, th)
	}
	threadID := th["thread"].(map[string]any)["id"].(string)

	model.push(
		func(llm.ChatRequest) []llm.ChatEvent { return call("r1", "read_file", `{"path":"docs/policy.md"}`) },
		func(req llm.ChatRequest) []llm.ChatEvent {
			return call("e0", "edit_file", `{"path":"docs/policy.md","edits":[{"find":"five years","replace":"x"}]}`)
		},
		func(req llm.ChatRequest) []llm.ChatEvent {
			last := req.Messages[len(req.Messages)-1].Content[0]
			if !last.IsError || !strings.Contains(last.Content, "doesn't contain") {
				t.Errorf("bad edit result: %+v", last)
			}
			return call("e1", "edit_file", `{"path":"docs/policy.md","edits":[{"find":"every three years","replace":"every four years"}]}`)
		},
		func(llm.ChatRequest) []llm.ChatEvent {
			return call("c1", "create_file", `{"path":"docs/faq.md","markdown":"# FAQ\n\nAsk IT.\n"}`)
		},
		func(llm.ChatRequest) []llm.ChatEvent { return call("d1", "delete_file", `{"path":"docs/old.md"}`) },
		func(req llm.ChatRequest) []llm.ChatEvent {
			if !strings.Contains(req.System[len(req.System)-1].Text, "revision #") {
				t.Errorf("no revision context: %v", req.System)
			}
			return say("Changed the refresh to four years [[docs/policy.md#laptops]], drafted an FAQ, and proposed deleting the old page.")
		},
	)
	if code, _ := samC.do("POST", "/assistant/threads/"+threadID+"/messages", map[string]any{"text": "Make it four years, add an FAQ, drop the old page."}); code != 202 {
		t.Fatalf("prompt: %d", code)
	}
	a.Assistant.Wait()

	// The edit and the new page are suggestions, credited to Sam via the assistant.
	_, list := samC.do("GET", "/revisions/"+revID+"/suggestions", nil)
	items := list["items"].([]any)
	byPath := map[string]map[string]any{}
	for _, it := range items {
		m := it.(map[string]any)
		byPath[m["path"].(string)] = m
	}
	if p := byPath["docs/policy.md"]; p == nil || p["inserted"] != "four" || p["deleted"] != "three" || p["author"] != sam.ID {
		t.Fatalf("policy suggestion: %v", items)
	}
	if f := byPath["docs/faq.md"]; f == nil || !strings.Contains(f["inserted"].(string), "Ask IT.") {
		t.Fatalf("faq suggestion: %v", items)
	}
	a.Collab.FlushRevision(ctx, revID)
	if f, _ := revisions.FileAt(ctx, a.DB, revID, "docs/policy.md"); !strings.Contains(f.ContentMD, "three years") {
		t.Fatalf("pending suggestion applied: %q", f.ContentMD)
	}
	_, state, _ := a.Collab.StateOf(ctx, revID, "docs/policy.md")
	doc, _ := a.Engine.YReadDoc(ctx, state)
	if !strings.Contains(string(doc), `"assistant":true`) {
		t.Fatalf("not marked as the assistant's: %s", doc)
	}
	var kinds int
	_ = store.QueryRow(ctx, a.DB, `SELECT COUNT(*) FROM ydoc_clients c JOIN ydocs d ON d.id = c.ydoc_id WHERE d.revision_id = ? AND c.kind = 'assistant'`, revID).Scan(&kinds)
	if kinds == 0 {
		t.Fatal("no assistant client (Assisted-by)")
	}

	// Everyone who can see the revision follows the thread; only editors prompt.
	_, lv := luisC.do("GET", "/revisions/"+revID+"/assistant", nil)
	if lv["can_prompt"] != false || len(lv["messages"].([]any)) < 3 {
		t.Fatalf("viewer: %v", lv)
	}
	var del string
	var parts []string
	for _, m := range lv["messages"].([]any) {
		for _, p := range m.(map[string]any)["parts"].([]any) {
			pp := p.(map[string]any)
			parts = append(parts, pp["type"].(string))
			if pp["type"] == "file_op" {
				del = pp["call_id"].(string)
			}
		}
	}
	if !strings.Contains(strings.Join(parts, ","), "edit,edit,file_op") || del == "" {
		t.Fatalf("parts: %v", parts)
	}
	if code, _ := luisC.do("POST", "/assistant/threads/"+threadID+"/proposals/"+del+"/accept", nil); code != 403 {
		t.Fatalf("viewer confirms: %d", code)
	}
	if code, _ := samC.do("POST", "/assistant/threads/"+threadID+"/proposals/"+del+"/accept", nil); code != 200 {
		t.Fatalf("confirm delete: %d", code)
	}
	if f, err := revisions.FileAt(ctx, a.DB, revID, "docs/old.md"); err != nil || f.Op != revisions.OpDelete {
		t.Fatalf("deleted: %+v %v", f, err)
	}
	if code, _ := samC.do("POST", "/assistant/threads/"+threadID+"/proposals/"+del+"/accept", nil); code != 409 {
		t.Fatalf("confirm twice: %d", code)
	}
}
