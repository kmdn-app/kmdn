package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kmdn-app/kmdn/internal/users"
)

// bearer adds an agent key to every request.
type bearer struct {
	token string
	base  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

func mcpSession(t *testing.T, base, token string) *sdk.ClientSession {
	t.Helper()
	c := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := c.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint: base + "/mcp", HTTPClient: &http.Client{Transport: bearer{token, http.DefaultTransport}}, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// callJSON calls a tool and decodes its structured output.
func callJSON(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) (map[string]any, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		var msg string
		for _, c := range res.Content {
			if tc, ok := c.(*sdk.TextContent); ok {
				msg += tc.Text
			}
		}
		return map[string]any{"error": msg}, true
	}
	b, _ := json.Marshal(res.StructuredContent)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return out, false
}

func TestMCPServer(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	handbook, remote := connectLocalRemote(t, a, admin, map[string]string{
		"docs/travel.md":     "---\ntitle: Travel\nowner: people-ops\n---\n# Travel\n\nHow we travel.\n\n## Working abroad\n\nEmployees can work abroad for up to 30 working days per year.\n\n### Paperwork\n\nTell HR a week before.\n\n## Expenses\n\nSee [expenses](expenses.md).\n",
		"docs/expenses.md":   "# Expenses\n\nFile receipts within 30 days. Back to [travel](travel.md#working-abroad).\n",
		"docs/team/index.md": "# Team\n\nWho we are.\n",
	})
	_ = connectLocal(t, a, admin, map[string]string{"docs/secret.md": "# Secret\n\nAcquisition plans.\n"})
	writeFile(t, remote, "docs/travel.md", "---\ntitle: Travel\nowner: people-ops\n---\n# Travel\n\nHow we travel.\n\n## Working abroad\n\nEmployees can work abroad for up to 20 working days per year.\n\n### Paperwork\n\nTell HR a week before.\n\n## Expenses\n\nSee [expenses](expenses.md).\n")
	gitIn(t, remote, "commit", "--quiet", "-am", "Twenty days abroad\n\nCo-authored-by: Sam Lee <sam@northwind.dev>")
	if err := a.Repos.Sync(ctx, handbook); err != nil {
		t.Fatal(err)
	}
	runJobs(t, a)

	// Only admins manage keys; the token is shown once, with a client config.
	sam, _ := users.Create(ctx, a.DB, "sam@northwind.dev", "Sam", false)
	samC := &tc{t: t, base: admin.base, c: newClient()}
	signIn(t, a, samC, sam)
	if code, _ := samC.do("POST", "/admin/agent-keys", map[string]any{"name": "x", "all_repos": true}); code != 403 {
		t.Fatalf("non-admin creates a key: %d", code)
	}
	if code, _ := admin.do("POST", "/admin/agent-keys", map[string]any{"name": "Support bot"}); code != 422 {
		t.Fatalf("key without scope: %d", code)
	}
	code, created := admin.do("POST", "/admin/agent-keys", map[string]any{"name": "Support bot", "repo_ids": []string{handbook}, "expires_in_days": 90})
	if code != 201 {
		t.Fatalf("create key: %d %v", code, created)
	}
	token := created["token"].(string)
	keyID := created["key"].(map[string]any)["id"].(string)
	if !strings.HasPrefix(token, "kmdn_ak_"+keyID+"_") || !strings.Contains(created["config"].(string), `"Authorization": "Bearer `+token) || created["key"].(map[string]any)["expires_at"] == nil {
		t.Fatalf("created: %v", created)
	}
	_, list := admin.do("GET", "/admin/agent-keys", nil)
	if strings.Contains(toJSON(list), token[len("kmdn_ak_")+17:]) {
		t.Fatal("the secret is listed")
	}

	// Without a valid key: 401 with a clear message.
	for _, tok := range []string{"", "kmdn_ak_nope", "kmdn_ak_" + keyID + "_wrong"} {
		req, _ := http.NewRequest("POST", admin.base+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		req.Header.Set("Content-Type", "application/json")
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 401 || res.Header.Get("WWW-Authenticate") == "" {
			t.Fatalf("token %q: %d", tok, res.StatusCode)
		}
	}

	cs := mcpSession(t, admin.base, token)
	tl, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tl.Tools {
		names = append(names, tool.Name)
		if !tool.Annotations.ReadOnlyHint || tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Errorf("%s annotations: %+v", tool.Name, tool.Annotations)
		}
	}
	if got := strings.Join(names, ","); got != "get_history,get_links,get_outline,list_repos,list_tree,read_doc,read_doc_at,search" {
		t.Fatalf("tools: %s", got)
	}

	// The key sees its repo only.
	out, _ := callJSON(t, cs, "list_repos", nil)
	if rs := out["repos"].([]any); len(rs) != 1 || rs[0].(map[string]any)["id"] != handbook {
		t.Fatalf("repos: %v", out)
	}
	slug := out["repos"].([]any)[0].(map[string]any)["name"].(string)

	out, _ = callJSON(t, cs, "search", map[string]any{"query": "abroad"})
	hits := out["hits"].([]any)
	if len(hits) == 0 || hits[0].(map[string]any)["path"] != "docs/travel.md" || !strings.Contains(hits[0].(map[string]any)["url"].(string), "/docs/travel.md") {
		t.Fatalf("search: %v", out)
	}
	if out, _ := callJSON(t, cs, "search", map[string]any{"query": "abroad", "path_prefix": "docs/team"}); len(out["hits"].([]any)) != 0 {
		t.Fatalf("path_prefix: %v", out)
	}
	if out, isErr := callJSON(t, cs, "search", map[string]any{"query": "acquisition", "repo": "local/secret"}); !isErr {
		t.Fatalf("other repo searchable: %v", out)
	}

	out, _ = callJSON(t, cs, "list_tree", map[string]any{"repo": slug, "path": "docs", "depth": 1})
	if s := toJSON(out); !strings.Contains(s, "docs/travel.md") || !strings.Contains(s, `"docs/team"`) || strings.Contains(s, "docs/team/index.md") {
		t.Fatalf("tree: %s", s)
	}

	out, _ = callJSON(t, cs, "read_doc", map[string]any{"repo": slug, "path": "docs/travel.md"})
	if out["frontmatter"].(map[string]any)["owner"] != "people-ops" || strings.Contains(out["content"].(string), "owner:") || !strings.Contains(out["content"].(string), "20 working days") || out["updated_at"] == nil {
		t.Fatalf("read_doc: %v", out)
	}
	outline := out["outline"].([]any)
	if len(outline) != 1 || len(outline[0].(map[string]any)["children"].([]any)) != 2 {
		t.Fatalf("outline: %v", toJSON(outline))
	}
	out, _ = callJSON(t, cs, "read_doc", map[string]any{"repo": slug, "path": "docs/travel.md", "heading": "working-abroad", "format": "text"})
	if c := out["content"].(string); !strings.Contains(c, "Tell HR") || strings.Contains(c, "Expenses") || strings.Contains(c, "##") || !strings.HasSuffix(out["url"].(string), "#working-abroad") {
		t.Fatalf("section: %v", out)
	}
	if out, isErr := callJSON(t, cs, "read_doc", map[string]any{"repo": slug, "path": "../secret.md"}); !isErr {
		t.Fatalf("escape: %v", out)
	}

	out, _ = callJSON(t, cs, "get_history", map[string]any{"repo": slug, "path": "docs/travel.md"})
	vs := out["versions"].([]any)
	if len(vs) != 2 || vs[0].(map[string]any)["title"] != "Twenty days abroad" || !strings.HasSuffix(toJSON(vs[0].(map[string]any)["authors"]), `,"Sam Lee"]`) || strings.Contains(toJSON(out), "@") {
		t.Fatalf("history: %v", out)
	}
	old := vs[1].(map[string]any)["sha"].(string)
	out, _ = callJSON(t, cs, "read_doc_at", map[string]any{"repo": slug, "path": "docs/travel.md", "sha": old[:8]})
	if !strings.Contains(out["content"].(string), "30 working days") {
		t.Fatalf("read_doc_at: %v", out)
	}
	if out, isErr := callJSON(t, cs, "read_doc_at", map[string]any{"repo": slug, "path": "docs/expenses.md", "sha": vs[0].(map[string]any)["sha"]}); !isErr {
		t.Fatalf("sha from another page's history: %v", out)
	}

	out, _ = callJSON(t, cs, "get_links", map[string]any{"repo": slug, "path": "docs/expenses.md"})
	if s := toJSON(out); !strings.Contains(s, `"to_path":"docs/travel.md"`) || !strings.Contains(s, `"from_path":"docs/travel.md"`) {
		t.Fatalf("links: %s", s)
	}

	// Resources: listed per key, read by URI.
	rl, err := cs.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rl.Resources) != 3 || !strings.HasPrefix(rl.Resources[0].URI, "kmdn://"+slug+"/docs/") {
		t.Fatalf("resources: %+v", rl.Resources)
	}
	rr, err := cs.ReadResource(ctx, &sdk.ReadResourceParams{URI: "kmdn://" + slug + "/docs/expenses.md"})
	if err != nil || !strings.Contains(rr.Contents[0].Text, "File receipts") {
		t.Fatalf("read resource: %v %v", rr, err)
	}
	if _, err := cs.ReadResource(ctx, &sdk.ReadResourceParams{URI: "kmdn://local/secret/docs/secret.md"}); err == nil {
		t.Fatal("read a resource outside the key's scope")
	}

	p, err := cs.GetPrompt(ctx, &sdk.GetPromptParams{Name: "answer_from_docs", Arguments: map[string]string{"question": "How long can I work abroad?"}})
	if err != nil || !strings.Contains(p.Messages[0].Content.(*sdk.TextContent).Text, "How long can I work abroad?") {
		t.Fatalf("prompt: %v %v", p, err)
	}

	// Calls are audited and counted.
	_, calls := admin.do("GET", "/admin/agent-keys/"+keyID+"/calls", nil)
	if s := toJSON(calls); !strings.Contains(s, "mcp.read_doc_at") || !strings.Contains(s, `"query":"abroad"`) {
		t.Fatalf("calls: %s", s)
	}
	_, list = admin.do("GET", "/admin/agent-keys", nil)
	k := list["items"].([]any)[0].(map[string]any)
	usage := k["usage"].([]any)
	if usage[len(usage)-1].(float64) < 10 || k["last_used_at"] == nil {
		t.Fatalf("usage: %v", k)
	}

	// Revocation is immediate.
	if code, _ := admin.do("POST", "/admin/agent-keys/"+keyID+"/revoke", nil); code != 204 {
		t.Fatalf("revoke: %d", code)
	}
	if _, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "list_repos"}); err == nil || !strings.Contains(err.Error(), "Unauthorized") {
		t.Fatalf("revoked key works: %v", err)
	}
}
