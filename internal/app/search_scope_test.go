package app

import (
	"context"
	"testing"

	"github.com/kmdn-app/kmdn/internal/users"
)

func TestSearchScopeChangesWithoutGitCommit(t *testing.T) {
	for _, field := range []string{"content_root", "include", "exclude"} {
		t.Run(field, func(t *testing.T) {
			a, admin := newApp(t, nil)
			ctx := context.Background()
			u, err := users.Create(ctx, a.DB, "admin@example.org", "Admin", true)
			if err != nil {
				t.Fatal(err)
			}
			signIn(t, a, admin, u)
			repo := connectLocal(t, a, admin, map[string]string{
				"docs/public/index.md": "# Searchneedle public\nVisible content.\n",
				"docs/private.md":      "# Searchneedle private\nConfidential content.\n",
			})
			code, created := admin.do("POST", "/orgs/default/admin/agent-keys", map[string]any{"name": "Scope test", "repo_ids": []string{repo}})
			if code != 201 {
				t.Fatalf("key: %d %v", code, created)
			}
			cs := mcpSession(t, admin.base, created["token"].(string))
			check := func(want int) {
				t.Helper()
				code, body := admin.do("GET", "/repos/"+repo+"/search?q=Searchneedle", nil)
				if code != 200 || len(body["items"].([]any)) != want {
					t.Fatalf("REST search: %d %v, want %d hits", code, body, want)
				}
				body, failed := callJSON(t, cs, "search", map[string]any{"repo": repo, "query": "Searchneedle"})
				if failed || len(body["hits"].([]any)) != want {
					t.Fatalf("MCP search: %v, want %d hits", body, want)
				}
			}
			check(2)
			var narrow, broad any
			switch field {
			case "content_root":
				narrow, broad = "docs/public", "docs"
			case "include":
				narrow, broad = []string{"public/**"}, []string{}
			case "exclude":
				narrow, broad = []string{"private.md"}, []string{}
			}
			if code, body := admin.do("PATCH", "/repos/"+repo, map[string]any{field: narrow}); code != 200 {
				t.Fatalf("narrow: %d %v", code, body)
			}
			check(1) // The queued rebuild has not run yet.
			if err := a.Repos.Sync(ctx, repo); err != nil {
				t.Fatal(err)
			}
			runJobs(t, a)
			check(1)
			if code, body := admin.do("PATCH", "/repos/"+repo, map[string]any{field: broad}); code != 200 {
				t.Fatalf("broaden: %d %v", code, body)
			}
			runJobs(t, a)
			check(2)
		})
	}
}
