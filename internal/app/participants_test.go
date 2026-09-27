package app

import (
	"context"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/repos"
	"github.com/kmdn-app/kmdn/internal/revisions"
	"github.com/kmdn-app/kmdn/internal/users"
)

// Everyone who takes part in a revision shows up in it and can review;
// admins always can.
func TestParticipantsAndReviewers(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true) // instance admin
	signIn(t, a, admin, maya)
	repoID := connectLocal(t, a, admin, map[string]string{"docs/index.md": "# Handbook\n\nStart here now.\n"})
	mk := func(email, name string, role access.Role) (users.User, *tc) {
		u, _ := users.Create(ctx, a.DB, email, name, false)
		_ = access.Grant(ctx, a.DB, repoID, "user", u.ID, role)
		c := &tc{t: t, base: admin.base, c: newClient()}
		signIn(t, a, c, u)
		return u, c
	}
	sam, samC := mk("sam@northwind.dev", "Sam", access.Contributor)
	tom, _ := mk("tom@northwind.dev", "Tom", access.Maintainer)
	luis, luisC := mk("luis@northwind.dev", "Luis", access.Contributor)
	vic, _ := mk("vic@northwind.dev", "Vic", access.Contributor)
	ada, _ := mk("ada@northwind.dev", "Ada", access.Admin) // repo admin

	_, rev := samC.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Start", "path": "docs/index.md"})
	revID := rev["id"].(string)
	rv, _ := revisions.Get(ctx, a.DB, revID)
	repo, _ := repos.Get(ctx, a.DB, repoID)
	// Tom edits without being invited (maintainers can); Luis comments.
	if err := a.Collab.Apply(ctx, repo, rv, revisions.Caller{User: tom, Role: access.Maintainer}, "docs/index.md", "# Handbook\n\nStart here, now.\n", "human"); err != nil {
		t.Fatal(err)
	}
	a.Collab.Flush(ctx)
	if code, th := luisC.do("POST", "/revisions/"+revID+"/threads", map[string]any{"path": "docs/index.md", "anchor": map[string]any{"quote": "now"}, "body": "Is this clear?"}); code != 201 {
		t.Fatalf("comment: %d %v", code, th)
	}
	// Maya (instance admin) and Ada (repo admin) edit it too, as invited editors.
	samC.do("PUT", "/revisions/"+revID+"/members/"+maya.ID, nil)
	samC.do("PUT", "/revisions/"+revID+"/members/"+ada.ID, nil)

	_, v := samC.do("GET", "/revisions/"+revID, nil)
	got := toJSON(v["participants"])
	if !strings.Contains(got, `"name":"Tom"`) || !strings.Contains(got, `"did":"edited"`) || !strings.Contains(got, `"name":"Luis"`) || !strings.Contains(got, `"did":"commented"`) || strings.Contains(got, sam.ID) || strings.Contains(got, vic.ID) {
		t.Fatalf("participants: %s", got)
	}

	// Candidates: maintainers, admins (even as editors) and participants;
	// not Vic, who took no part, nor Sam, the author.
	_, sug := samC.do("GET", "/revisions/"+revID+"/reviewer-suggestions", nil)
	cands := toJSON(sug)
	for _, u := range []users.User{tom, luis, maya, ada} {
		if !strings.Contains(cands, u.ID) {
			t.Fatalf("%s should be a candidate: %s", u.Name, cands)
		}
	}
	if strings.Contains(cands, vic.ID) || strings.Contains(cands, sam.ID) {
		t.Fatalf("candidates: %s", cands)
	}
	if code, _ := samC.do("POST", "/revisions/"+revID+"/submit", map[string]any{"reviewers": []string{vic.ID}}); code != 422 {
		t.Fatalf("Vic took no part: %d", code)
	}
	code, r := samC.do("POST", "/revisions/"+revID+"/submit", map[string]any{"reviewers": []string{luis.ID, maya.ID, ada.ID}})
	if code != 200 || r["state"] != "in_review" || len(r["reviewers"].([]any)) != 3 {
		t.Fatalf("submit to a participant and two admins who edit it: %d %v", code, r)
	}
}
