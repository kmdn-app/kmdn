package secrets

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/kmdn-app/kmdn/internal/store"
	"github.com/kmdn-app/kmdn/internal/storetest"
)

func key(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func TestPutGetUpdateDelete(t *testing.T) {
	db := storetest.Open(t)
	ctx := context.Background()
	s, err := New(db, key(1))
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Put(ctx, db, "smtp_password", []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := store.QueryRow(ctx, db, `SELECT ciphertext FROM secrets WHERE id = ?`, id).Scan(&raw); err != nil || bytes.Contains(raw, []byte("hunter2")) {
		t.Fatalf("plaintext stored or read failed: %v", err)
	}
	got, err := s.Get(ctx, db, id)
	if err != nil || string(got) != "hunter2" {
		t.Fatalf("get: %q %v", got, err)
	}
	if err := s.Update(ctx, db, id, []byte("correct horse")); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, db, id); string(got) != "correct horse" {
		t.Fatalf("after update: %q", got)
	}
	other, _ := New(db, key(2))
	if _, err := other.Get(ctx, db, id); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("wrong key should fail, got %v", err)
	}
	if err := s.Delete(ctx, db, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, db, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestRotateKEK(t *testing.T) {
	db := storetest.Open(t)
	ctx := context.Background()
	s, _ := New(db, key(1))
	a, _ := s.Put(ctx, db, "a", []byte("alpha"))
	b, _ := s.Put(ctx, db, "b", []byte("beta"))
	n, err := s.RotateKEK(ctx, key(9))
	if err != nil || n != 2 {
		t.Fatalf("rotate: %d %v", n, err)
	}
	fresh, _ := New(db, key(9))
	for id, want := range map[string]string{a: "alpha", b: "beta"} {
		if got, err := fresh.Get(ctx, db, id); err != nil || string(got) != want {
			t.Fatalf("after rotate %s: %q %v", id, got, err)
		}
	}
	old, _ := New(db, key(1))
	if _, err := old.Get(ctx, db, a); !errors.Is(err, ErrDecrypt) {
		t.Fatal("old key still works")
	}
}

func TestSwappedCiphertextRejected(t *testing.T) {
	db := storetest.Open(t)
	ctx := context.Background()
	s, _ := New(db, key(1))
	a, _ := s.Put(ctx, db, "k", []byte("alpha"))
	b, _ := s.Put(ctx, db, "k", []byte("beta"))
	// Copy a's ciphertext and data key onto b: AAD binds them to a's id.
	if _, err := store.Exec(ctx, db, `UPDATE secrets SET ciphertext = (SELECT ciphertext FROM secrets WHERE id = ?), data_key = (SELECT data_key FROM secrets WHERE id = ?) WHERE id = ?`, a, a, b); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, db, b); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("expected decrypt failure, got %v", err)
	}
}
