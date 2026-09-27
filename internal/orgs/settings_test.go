package orgs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/kmdn-app/kmdn/internal/storetest"
)

func TestSettingsStore(t *testing.T) {
	ctx := context.Background()
	db := storetest.Open(t)
	s := &SettingsStore{DB: db}
	st, locked, err := s.Get(ctx, DefaultID)
	if err != nil || !st.Assistant || len(locked) != 0 {
		t.Fatalf("defaults: %+v %v %v", st, locked, err)
	}
	st, err = s.Update(ctx, db, DefaultID, map[string]json.RawMessage{"assistant": json.RawMessage("false")})
	if err != nil || st.Assistant {
		t.Fatalf("turn off: %+v %v", st, err)
	}
	if _, err := s.Update(ctx, db, DefaultID, map[string]json.RawMessage{"nope": json.RawMessage("1")}); err == nil {
		t.Fatal("unknown field accepted")
	}
	if _, err := s.Update(ctx, db, DefaultID, map[string]json.RawMessage{"assistant": json.RawMessage(`"yes"`)}); err == nil {
		t.Fatal("wrong type accepted")
	}
	// A managed field wins over the saved value and can't be changed.
	s.Managed = func(context.Context, Org) map[string]any { return map[string]any{"assistant": true} }
	st, locked, _ = s.Get(ctx, DefaultID)
	if !st.Assistant || len(locked) != 1 || locked[0] != "assistant" {
		t.Fatalf("managed: %+v %v", st, locked)
	}
	if _, err := s.Update(ctx, db, DefaultID, map[string]json.RawMessage{"assistant": json.RawMessage("false")}); !errors.Is(err, ErrLocked) {
		t.Fatalf("changing a managed field: %v", err)
	}
}
