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

func TestOrgSecrets(t *testing.T) {
	db := storetest.Open(t)
	ctx := context.Background()
	s, _ := New(db, key(1))
	for _, id := range []string{"org_a", "org_b"} {
		if _, err := store.Exec(ctx, db, `INSERT INTO orgs (id, slug, name, created_at) VALUES (?, ?, ?, 0)`, id, id[4:]+"x", id); err != nil {
			t.Fatal(err)
		}
	}
	a, err := s.PutOrg(ctx, db, "org_a", "repo_token", []byte("glpat-a"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.PutOrg(ctx, db, "org_b", "repo_token", []byte("glpat-b"))
	inst, _ := s.Put(ctx, db, "smtp_password", []byte("smtp"))
	if got, err := s.Get(ctx, db, a); err != nil || string(got) != "glpat-a" {
		t.Fatalf("org secret: %q %v", got, err)
	}
	if err := s.Update(ctx, db, a, []byte("glpat-a2")); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, db, a); string(got) != "glpat-a2" {
		t.Fatalf("after update: %q", got)
	}

	// A row moved to another org no longer decrypts.
	if _, err := store.Exec(ctx, db, `UPDATE secrets SET org_id = 'org_b' WHERE id = ?`, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, db, a); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("moved secret: %v", err)
	}
	_, _ = store.Exec(ctx, db, `UPDATE secrets SET org_id = 'org_a' WHERE id = ?`, a)

	// Rotating the org key keeps its secrets readable.
	if n, err := s.RotateOrgKey(ctx, "org_a"); err != nil || n != 1 {
		t.Fatalf("rotate org key: %d %v", n, err)
	}
	if got, _ := s.Get(ctx, db, a); string(got) != "glpat-a2" {
		t.Fatalf("after org key rotation: %q", got)
	}
	// Rotating the KEK re-wraps instance secrets and org keys.
	if n, err := s.RotateKEK(ctx, key(3)); err != nil || n != 3 {
		t.Fatalf("rotate KEK: %d %v", n, err)
	}
	for id, want := range map[string]string{a: "glpat-a2", b: "glpat-b", inst: "smtp"} {
		if got, err := s.Get(ctx, db, id); err != nil || string(got) != want {
			t.Fatalf("after KEK rotation %s: %q %v", id, got, err)
		}
	}
	// Destroying an org's key shreds its secrets, and only its own.
	if err := s.DestroyOrgKey(ctx, db, "org_a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, db, a); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("secret of a shredded org: %v", err)
	}
	// Even a copy of the ciphertext (a backup) can't be opened any more.
	if _, err := store.Exec(ctx, db, `INSERT INTO secrets (id, kind, ciphertext, data_key, created_at, org_id) SELECT 'sec_copy', kind, ciphertext, data_key, created_at, 'org_a' FROM secrets WHERE id = ?`, b); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, db, "sec_copy"); !errors.Is(err, ErrNoOrgKey) {
		t.Fatalf("restored copy after shredding: %v", err)
	}
	if got, _ := s.Get(ctx, db, b); string(got) != "glpat-b" {
		t.Fatalf("other org after shredding: %q", got)
	}
}
