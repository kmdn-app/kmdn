package orgs

import (
	"context"
	"errors"
	"testing"

	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/storetest"
	"github.com/kmdn-app/kmdn/internal/users"
)

func TestDefaultOrgExists(t *testing.T) {
	ctx := context.Background()
	db := storetest.Open(t)
	o, err := ByID(ctx, db, DefaultID)
	if err != nil {
		t.Fatal(err)
	}
	if o.Slug != "default" || o.Name != "Default" || o.Status != Active {
		t.Fatalf("default org = %+v", o)
	}
}

func TestSlugs(t *testing.T) {
	for _, s := range []string{"ab", "northwind", "north-wind", "a1-b2", "x2345678901234567890123456789012345678"} {
		if err := ValidSlug(s); err != nil {
			t.Errorf("ValidSlug(%q) = %v", s, err)
		}
	}
	for _, s := range []string{"", "a", "-ab", "ab-", "a--b", "Ab", "a_b", "admin", "api", "x23456789012345678901234567890123456789012"} {
		if ValidSlug(s) == nil {
			t.Errorf("ValidSlug(%q) accepted", s)
		}
	}
	for in, want := range map[string]string{"Northwind Docs": "northwind-docs", "  HR / People ": "hr-people", "A": "org-a", "Admin": "org-admin", "Café 42": "caf-42"} {
		got := Slugify(in)
		if got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
		if err := ValidSlug(got); err != nil {
			t.Errorf("Slugify(%q) = %q is invalid: %v", in, got, err)
		}
	}
}

func TestCreateAndMembers(t *testing.T) {
	ctx := context.Background()
	db := storetest.Open(t)
	alice, _ := users.Create(ctx, db, "alice@example.com", "Alice", false)
	bob, _ := users.Create(ctx, db, "bob@example.com", "Bob", false)

	o, err := Create(ctx, db, "acme", "Acme", alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, db, "acme", "Other", bob.ID); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("duplicate slug: %v", err)
	}
	var se *ErrSlug
	if _, err := Create(ctx, db, "settings", "Settings", bob.ID); !errors.As(err, &se) {
		t.Fatalf("reserved slug: %v", err)
	}

	if err := SetMode(ctx, db, Multi); err != nil {
		t.Fatal(err)
	}
	role := func(org string, u users.User) string {
		t.Helper()
		r, err := Role(ctx, db, org, u)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := role(o.ID, alice); r != Owner {
		t.Fatalf("alice = %q", r)
	}
	if r := role(o.ID, bob); r != "" {
		t.Fatalf("bob before joining = %q", r)
	}
	if err := AddMember(ctx, db, o.ID, bob.ID, Member, alice.ID); err != nil {
		t.Fatal(err)
	}
	if err := AddMember(ctx, db, o.ID, bob.ID, Admin, alice.ID); err != nil { // upsert
		t.Fatal(err)
	}
	if r := role(o.ID, bob); r != Admin {
		t.Fatalf("bob = %q", r)
	}
	if n, _ := CountOwners(ctx, db, o.ID); n != 1 {
		t.Fatalf("owners = %d", n)
	}
	if err := SetMemberStatus(ctx, db, o.ID, bob.ID, Deactivated); err != nil {
		t.Fatal(err)
	}
	if r := role(o.ID, bob); r != "" {
		t.Fatalf("deactivated bob = %q", r)
	}
	ms, err := Members(ctx, db, o.ID)
	if err != nil || len(ms) != 2 || ms[0].UserID != alice.ID || ms[1].InvitedBy != alice.ID {
		t.Fatalf("members = %+v, %v", ms, err)
	}

	mine, err := ForUser(ctx, db, alice)
	if err != nil || len(mine) != 1 || mine[0].ID != o.ID {
		t.Fatalf("alice's orgs = %+v, %v", mine, err)
	}
	if err := RemoveMember(ctx, db, o.ID, bob.ID); err != nil {
		t.Fatal(err)
	}
	if err := RemoveMember(ctx, db, o.ID, bob.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("removing twice: %v", err)
	}
}

func TestSingleMode(t *testing.T) {
	ctx := context.Background()
	db := storetest.Open(t)
	admin, _ := users.Create(ctx, db, "admin@example.com", "Admin", true)
	carol, _ := users.Create(ctx, db, "carol@example.com", "Carol", false)
	other, err := Create(ctx, db, "other", "Other", "")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		u    users.User
		org  string
		want string
	}{
		{admin, DefaultID, Owner},
		{carol, DefaultID, Member},
		{carol, other.ID, ""}, // single mode only knows the default org
	}
	for _, c := range cases {
		if r, _ := Role(ctx, db, c.org, c.u); r != c.want {
			t.Errorf("%s in %s = %q, want %q", c.u.Email, c.org, r, c.want)
		}
	}
	// Going back to single mode is refused while several orgs exist.
	if err := SetMode(ctx, db, Multi); err != nil {
		t.Fatal(err)
	}
	if err := SetMode(ctx, db, Single); !errors.Is(err, ErrModeChange) {
		t.Fatalf("back to single with two orgs: %v", err)
	}
	// Multi mode needs a row.
	if r, _ := Role(ctx, db, DefaultID, carol); r != "" {
		t.Errorf("carol in multi mode without a row = %q", r)
	}
	if err := SetStatus(ctx, db, other.ID, Deleting); err != nil {
		t.Fatal(err)
	}
	if err := SetMode(ctx, db, Single); err != nil {
		t.Fatal(err)
	}
	// A deactivated row still removes access in single mode.
	if err := AddMember(ctx, db, DefaultID, carol.ID, Member, ""); err != nil {
		t.Fatal(err)
	}
	if err := SetMemberStatus(ctx, db, DefaultID, carol.ID, Deactivated); err != nil {
		t.Fatal(err)
	}
	if r, _ := Role(ctx, db, DefaultID, carol); r != "" {
		t.Errorf("deactivated carol = %q", r)
	}
	// Deactivated accounts have no role anywhere.
	admin.Status = users.Deactivated
	if r, _ := Role(ctx, db, DefaultID, admin); r != "" {
		t.Errorf("deactivated admin = %q", r)
	}
	orgs, err := ForUser(ctx, db, carol)
	if err != nil || len(orgs) != 1 || orgs[0].ID != DefaultID {
		t.Fatalf("single-mode orgs = %+v, %v", orgs, err)
	}
}

func TestSlugAndStatus(t *testing.T) {
	ctx := context.Background()
	db := storetest.Open(t)
	a, _ := Create(ctx, db, "alpha", "Alpha", "")
	if _, err := Create(ctx, db, "beta", "Beta", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetSlug(ctx, db, a.ID, "beta"); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("taken slug: %v", err)
	}
	if err := SetSlug(ctx, db, a.ID, "alpha-2"); err != nil {
		t.Fatal(err)
	}
	if err := Rename(ctx, db, a.ID, " Alpha Two "); err != nil {
		t.Fatal(err)
	}
	if err := SetStatus(ctx, db, a.ID, Deleting); err != nil {
		t.Fatal(err)
	}
	got, _ := BySlug(ctx, db, "alpha-2")
	if got.Name != "Alpha Two" || got.Status != Deleting {
		t.Fatalf("org = %+v", got)
	}
	all, _ := List(ctx, db)
	for _, o := range all {
		if o.ID == a.ID {
			t.Fatal("orgs being deleted are listed")
		}
	}
}
