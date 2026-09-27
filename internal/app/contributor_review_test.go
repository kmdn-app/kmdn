package app

import (
	"context"
	"testing"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestAssignedContributorParticipantCanReview(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@example.org", "Maya", true)
	signIn(t, a, admin, maya)
	repoID, _ := connectLocalRemote(t, a, admin, map[string]string{"docs/index.md": "# Initial\n"})
	sam, _ := users.Create(ctx, a.DB, "sam@example.org", "Sam", false)
	if err := access.Grant(ctx, a.DB, repoID, "user", sam.ID, access.Contributor); err != nil {
		t.Fatal(err)
	}
	samC := &tc{t: t, base: admin.base, c: newClient()}
	signIn(t, a, samC, sam)
	_, body := admin.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Participant reviews", "path": "docs/index.md"})
	id := body["id"].(string)
	if code, b := samC.do("POST", "/revisions/"+id+"/threads", map[string]any{"path": "docs/index.md", "body": "I can check this page."}); code != 201 {
		t.Fatalf("comment %d %v", code, b)
	}
	if code, _ := samC.do("POST", "/revisions/"+id+"/approve", nil); code != 403 {
		t.Fatalf("unassigned contributor approved: %d", code)
	}
	if code, b := admin.do("POST", "/revisions/"+id+"/submit", map[string]any{"reviewers": []string{sam.ID}}); code != 200 {
		t.Fatalf("submit %d %v", code, b)
	}
	if code, b := samC.do("POST", "/revisions/"+id+"/files", map[string]any{"op": "add", "path": "docs/reviewer.md", "content": "# Reviewer correction\n"}); code != 200 {
		t.Fatalf("assigned contributor could not edit: %d %v", code, b)
	}
	if code, b := samC.do("POST", "/revisions/"+id+"/approve", nil); code != 200 || b["state"] != "approved" {
		t.Fatalf("assigned contributor could not approve: %d %v", code, b)
	}
	if code, _ := samC.do("POST", "/revisions/"+id+"/publish", map[string]any{}); code != 403 {
		t.Fatalf("contributor could publish: %d", code)
	}
	if err := access.Grant(ctx, a.DB, repoID, "user", sam.ID, access.Viewer); err != nil {
		t.Fatal(err)
	}
	if code, _ := samC.do("POST", "/revisions/"+id+"/approve", nil); code != 403 {
		t.Fatalf("downgraded reviewer could approve: %d", code)
	}
	if code, _ := samC.do("POST", "/revisions/"+id+"/files", map[string]any{"op": "add", "path": "docs/denied.md", "content": "# Denied\n"}); code != 403 {
		t.Fatalf("downgraded reviewer could edit: %d", code)
	}
}
