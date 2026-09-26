package settings

import (
	"context"
	"testing"

	"github.com/kmdn-app/kmdn/internal/storetest"
)

func TestRoundTripAndUpsert(t *testing.T) {
	db := storetest.Open(t)
	ctx := context.Background()
	var v struct{ Host string }
	if err := Get(ctx, db, "smtp", &v); !IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
	if err := Set(ctx, db, "smtp", map[string]string{"Host": "a"}); err != nil {
		t.Fatal(err)
	}
	if err := Set(ctx, db, "smtp", map[string]string{"Host": "b"}); err != nil {
		t.Fatal(err)
	}
	if err := Get(ctx, db, "smtp", &v); err != nil || v.Host != "b" {
		t.Fatalf("%+v %v", v, err)
	}
	if err := Delete(ctx, db, "smtp"); err != nil {
		t.Fatal(err)
	}
	if err := Get(ctx, db, "smtp", &v); !IsNotFound(err) {
		t.Fatal("delete failed")
	}
}
