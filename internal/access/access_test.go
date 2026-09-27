package access

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kmdn-app/kmdn/internal/groups"
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
	hr, _ := groups.Create(ctx, db, "People team", "")
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
	ids, _ := RepoIDs(ctx, db, tom)
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

func TestParse(t *testing.T) {
	if _, err := Parse("owner"); err == nil {
		t.Fatal("unknown role accepted")
	}
	if r, _ := Parse("maintainer"); !r.AtLeast(Contributor) || r.AtLeast(Admin) {
		t.Fatal("ordering")
	}
}
