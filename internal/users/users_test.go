package users

import (
	"context"
	"errors"
	"testing"

	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/storetest"
)

func TestNormalizeEmail(t *testing.T) {
	for in, want := range map[string]string{
		" Maya@Northwind.DEV ": "maya@northwind.dev",
		"not-an-email":         "",
		"Maya <maya@x.dev>":    "",
		"a@b":                  "a@b",
	} {
		if got := NormalizeEmail(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestCreateAndLookup(t *testing.T) {
	db := storetest.Open(t)
	ctx := context.Background()
	u, err := Create(ctx, db, "maya@northwind.dev", "Maya Chen", true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ByEmail(ctx, db, "maya@northwind.dev")
	if err != nil || got.ID != u.ID || !got.IsInstanceAdmin || got.Status != Active {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := Create(ctx, db, "maya@northwind.dev", "Dup", false); !store.IsUniqueViolation(err) {
		t.Fatalf("expected unique violation: %v", err)
	}
	if _, err := ByID(ctx, db, "usr_nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if n, _ := CountAdmins(ctx, db); n != 1 {
		t.Fatalf("admins %d", n)
	}
}
