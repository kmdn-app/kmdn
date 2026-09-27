package app

import (
	"context"
	"testing"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestLinkGraph(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	repoID := connectLocal(t, a, admin, map[string]string{
		"docs/index.md":              "# Handbook\n\nSee the [guide](guides/setup-guide.md), twice: [again](guides/setup-guide), and [gone](missing.md). ![logo](logo.png)\n",
		"docs/guides/setup-guide.md": "# Setup\n\nBack [home](../index.md).\n",
		"docs/orphan.md":             "# Orphan\n\nNobody links here.\n",
		"docs/logo.png":              "png",
	})
	sam, samC := func() (users.User, *tc) {
		u, _ := users.Create(ctx, a.DB, "sam@northwind.dev", "Sam", false)
		_ = access.Grant(ctx, a.DB, repoID, "user", u.ID, access.Contributor)
		c := &tc{t: t, base: admin.base, c: newClient()}
		signIn(t, a, c, u)
		return u, c
	}()
	a.Collab.Options.QuietPeriod = 0
	type node = map[string]any
	graph := func(scope string) (map[string]node, map[string]node) {
		t.Helper()
		q := ""
		if scope != "" {
			q = "?scope=" + scope
		}
		code, g := samC.do("GET", "/repos/"+repoID+"/graph"+q, nil)
		if code != 200 {
			t.Fatalf("graph: %d %v", code, g)
		}
		nodes, edges := map[string]node{}, map[string]node{}
		for _, n := range g["nodes"].([]any) {
			nodes[n.(node)["path"].(string)] = n.(node)
		}
		for _, e := range g["edges"].([]any) {
			edges[e.(node)["from"].(string)+" → "+e.(node)["to"].(string)] = e.(node)
		}
		return nodes, edges
	}
	nodes, edges := graph("")
	if len(nodes) != 3 || nodes["docs/orphan.md"]["orphan"] != true || nodes["docs/guides/setup-guide.md"]["folder"] != "guides" || nodes["docs/guides/setup-guide.md"]["title"] != "Setup guide" {
		t.Fatalf("nodes: %v", nodes)
	}
	if e := edges["docs/index.md → docs/guides/setup-guide.md"]; e == nil || e["count"] != float64(2) {
		t.Fatalf("edges: %v", edges)
	}
	if e := edges["docs/index.md → docs/missing.md"]; e == nil || e["broken"] != true {
		t.Fatalf("broken edge: %v", edges)
	}
	if len(edges) != 3 || nodes["docs/index.md"]["in"] != float64(1) {
		t.Fatalf("edges: %v", edges)
	}

	// In a revision that links the orphan from the guide, it isn't one anymore.
	_, rev := samC.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Link it", "path": "docs/guides/setup-guide.md"})
	rv, _ := revisions.Get(ctx, a.DB, rev["id"].(string))
	repo, _ := repos.Get(ctx, a.DB, repoID)
	if err := a.Collab.Apply(ctx, repo, rv, revisions.Caller{User: sam, Role: access.Contributor}, "docs/guides/setup-guide.md", "# Setup\n\nSee the [orphan](../orphan.md).\n", "human"); err != nil {
		t.Fatal(err)
	}
	a.Collab.FlushRevision(ctx, rv.ID)
	nodes, edges = graph("revision:" + rv.ID)
	if nodes["docs/orphan.md"]["orphan"] == true || nodes["docs/guides/setup-guide.md"]["changed"] != true {
		t.Fatalf("revision nodes: %v", nodes)
	}
	if edges["docs/guides/setup-guide.md → docs/index.md"] != nil || edges["docs/guides/setup-guide.md → docs/orphan.md"] == nil {
		t.Fatalf("revision edges: %v", edges)
	}
	if code, _ := samC.do("GET", "/repos/"+repoID+"/graph?scope=revision:rev_nope", nil); code != 404 {
		t.Fatalf("unknown revision: %d", code)
	}
}
