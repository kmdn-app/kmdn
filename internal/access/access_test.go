package access

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kmdn-app/kmdn/internal/groups"
	"github.com/kmdn-app/kmdn/internal/orgs"
	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/storetest"
	"github.com/kmdn-app/kmdn/internal/users"
)

func seedRepo(t *testing.T, db *store.DB, id string) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.Exec(ctx, db, `INSERT INTO forge_hosts (id, kind, display_name, created_at) VALUES ('fh_1', 'git', 'Git', ?) ON CONFLICT (id) DO NOTHING`, store.Millis(time.Now())); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Exec(ctx, db, `INSERT INTO repos (id, forge_host_id, owner, name, display_name, target_branch, created_at) VALUES (?, 'fh_1', 'northwind', ?, ?, 'main', ?)`, id, id, id, store.Millis(time.Now())); err != nil {
		t.Fatal(err)
	}
}

func TestEffectiveRoles(t *testing.T) {
	db := storetest.Open(t)
	ctx := context.Background()
	seedRepo(t, db, "rep_a")
	seedRepo(t, db, "rep_b")
	maya, _ := users.Create(ctx, db, "maya@x.dev", "Maya", true)
	tom, _ := users.Create(ctx, db, "tom@x.dev", "Tom", false)
	luis, _ := users.Create(ctx, db, "luis@x.dev", "Luis", false)
	hr, _ := groups.Create(ctx, db, orgs.DefaultID, "People team", "")
	_ = groups.AddMember(ctx, db, hr.ID, tom.ID)
	_ = Grant(ctx, db, "rep_a", GroupPrincipal, hr.ID, Contributor)
	_ = Grant(ctx, db, "rep_a", UserPrincipal, tom.ID, Viewer)
	_ = Grant(ctx, db, "rep_a", UserPrincipal, luis.ID, Viewer)

	cases := []struct {
		u    users.User
		repo string
		want Role
	}{{maya, "rep_a", Admin}, {maya, "rep_b", Admin}, {tom, "rep_a", Contributor}, {tom, "rep_b", None}, {luis, "rep_a", Viewer}}
	for _, c := range cases {
		got, err := Effective(ctx, db, c.u, c.repo)
		if err != nil || got != c.want {
			t.Errorf("%s on %s: %q want %q (%v)", c.u.Name, c.repo, got, c.want, err)
		}
	}
	if _, err := Require(ctx, db, luis, "rep_a", Contributor); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer passed contributor check: %v", err)
	}
	ids, _ := RepoIDs(ctx, db, tom, orgs.DefaultID)
	if len(ids) != 1 || ids[0] != "rep_a" {
		t.Fatalf("repo ids: %v", ids)
	}
	members, err := Members(ctx, db, "rep_a")
	if err != nil || len(members) != 3 || members[2].Type != GroupPrincipal || members[2].Members != 1 {
		t.Fatalf("members: %+v %v", members, err)
	}
	// Deactivated users lose access.
	_, _ = store.Exec(ctx, db, `UPDATE users SET status = 'deactivated' WHERE id = ?`, tom.ID)
	tom.Status = users.Deactivated
	if r, _ := Effective(ctx, db, tom, "rep_a"); r != None {
		t.Fatalf("deactivated user role %q", r)
	}
	_ = groups.Delete(ctx, db, hr.ID)
	if m, _ := Members(ctx, db, "rep_a"); len(m) != 2 {
		t.Fatalf("group grant not removed with group: %+v", m)
	}
}

// Roles only count inside the repo's org: grants to outsiders are ignored,
// org admins are Admin on the org's repos and nowhere else.
func TestOrgBoundaries(t *testing.T) {
	db := storetest.Open(t)
	ctx := context.Background()
	seedRepo(t, db, "rep_a") // default org
	if err := orgs.SetMode(ctx, db, orgs.Multi); err != nil {
		t.Fatal(err)
	}
	alice, _ := users.Create(ctx, db, "alice@acme.dev", "Alice", false)
	tom, _ := users.Create(ctx, db, "tom@x.dev", "Tom", false)
	acme, err := orgs.Create(ctx, db, "acme", "Acme", alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	seedRepo(t, db, "rep_c")
	if _, err := store.Exec(ctx, db, `UPDATE repos SET org_id = ? WHERE id = 'rep_c'`, acme.ID); err != nil {
		t.Fatal(err)
	}
	_ = orgs.AddMember(ctx, db, orgs.DefaultID, tom.ID, orgs.Member, "")
	_ = Grant(ctx, db, "rep_c", UserPrincipal, tom.ID, Maintainer) // tom isn't in acme
	_ = Grant(ctx, db, "rep_a", UserPrincipal, tom.ID, Viewer)

	cases := []struct {
		u    users.User
		repo string
		want Role
	}{{alice, "rep_c", Admin}, {alice, "rep_a", None}, {tom, "rep_c", None}, {tom, "rep_a", Viewer}, {tom, "rep_missing", None}}
	for _, c := range cases {
		if got, err := Effective(ctx, db, c.u, c.repo); err != nil || got != c.want {
			t.Errorf("%s on %s: %q want %q (%v)", c.u.Name, c.repo, got, c.want, err)
		}
	}
	if ids, _ := RepoIDs(ctx, db, alice, acme.ID); len(ids) != 1 || ids[0] != "rep_c" {
		t.Fatalf("alice in acme: %v", ids)
	}
	if ids, _ := RepoIDs(ctx, db, tom, acme.ID); len(ids) != 0 {
		t.Fatalf("tom sees acme repos: %v", ids)
	}
	if ids, _ := RepoIDs(ctx, db, alice, orgs.DefaultID); len(ids) != 0 {
		t.Fatalf("alice sees default repos: %v", ids)
	}
	// Once tom joins acme, his grant counts.
	_ = orgs.AddMember(ctx, db, acme.ID, tom.ID, orgs.Member, alice.ID)
	if got, _ := Effective(ctx, db, tom, "rep_c"); got != Maintainer {
		t.Fatalf("tom in acme: %q", got)
	}
}

func TestParse(t *testing.T) {
	if _, err := Parse("owner"); err == nil {
		t.Fatal("unknown role accepted")
	}
	if r, _ := Parse("maintainer"); !r.AtLeast(Contributor) || r.AtLeast(Admin) {
		t.Fatal("ordering")
	}
}
