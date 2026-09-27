// Package secrets stores credentials encrypted at rest with envelope
// encryption: each secret has its own random data key (AES-256-GCM). An
// instance secret's data key is encrypted with the instance key-encryption
// key (KEK) from KMDN_SECRET_KEY; an org secret's data key is encrypted with
// the org's key, itself wrapped by the KEK, and the org is bound into the
// ciphertext. Rotating the KEK only re-wraps data keys and org keys;
// destroying an org key makes every secret of the org unreadable.
package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/kmdn-app/kmdn/internal/ids"
	"github.com/kmdn-app/kmdn/internal/store"
)

// ErrDecrypt means the stored value could not be decrypted with this KEK.
var ErrDecrypt = errors.New("secrets: cannot decrypt (wrong secret_key?)")

// Store reads and writes encrypted secrets.
type Store struct {
	db  *store.DB
	kek []byte
}

// New returns a Store using a 32-byte KEK.
func New(db *store.DB, kek []byte) (*Store, error) {
	if len(kek) != 32 {
		return nil, fmt.Errorf("secrets: key must be 32 bytes, got %d", len(kek))
	}
	return &Store{db: db, kek: kek}, nil
}

func seal(key, plaintext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, aad), nil
}

func open(key, sealed, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, ErrDecrypt
	}
	pt, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], aad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// Put stores a new instance secret and returns its id. kind documents what
// it is (e.g. "smtp_password") and is bound into the ciphertext.
func (s *Store) Put(ctx context.Context, q store.Querier, kind string, plaintext []byte) (string, error) {
	return s.PutOrg(ctx, q, "", kind, plaintext)
}

// PutOrg stores a new secret of an org (forge tokens, webhook secrets, hook
// URLs) under the org's key; an empty orgID stores an instance secret.
func (s *Store) PutOrg(ctx context.Context, q store.Querier, orgID, kind string, plaintext []byte) (string, error) {
	id := ids.New(ids.Secret)
	wrapKey := s.kek
	if orgID != "" {
		k, err := s.orgKey(ctx, q, orgID, true)
		if err != nil {
			return "", err
		}
		wrapKey = k
	}
	ct, dk, err := encrypt(wrapKey, aadFor(id, kind, orgID), plaintext)
	if err != nil {
		return "", err
	}
	_, err = store.Exec(ctx, q, `INSERT INTO secrets (id, kind, ciphertext, data_key, created_at, org_id) VALUES (?, ?, ?, ?, ?, ?)`,
		id, kind, ct, dk, store.Millis(time.Now()), nullable(orgID))
	return id, err
}

// Update replaces the plaintext of an existing secret (new data key).
func (s *Store) Update(ctx context.Context, q store.Querier, id string, plaintext []byte) error {
	var kind, orgID string
	if err := store.QueryRow(ctx, q, `SELECT kind, COALESCE(org_id, '') FROM secrets WHERE id = ?`, id).Scan(&kind, &orgID); err != nil {
		return store.NotFound(err)
	}
	wrapKey, err := s.wrapKey(ctx, q, orgID)
	if err != nil {
		return err
	}
	ct, dk, err := encrypt(wrapKey, aadFor(id, kind, orgID), plaintext)
	if err != nil {
		return err
	}
	_, err = store.Exec(ctx, q, `UPDATE secrets SET ciphertext = ?, data_key = ?, rotated_at = ? WHERE id = ?`,
		ct, dk, store.Millis(time.Now()), id)
	return err
}

// aadFor binds a secret to its id, kind and org: a row moved to another org
// (or made an instance secret) no longer decrypts.
func aadFor(id, kind, orgID string) []byte {
	if orgID == "" {
		return []byte(id + "\x00" + kind)
	}
	return []byte(id + "\x00" + kind + "\x00" + orgID)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func encrypt(wrapKey, aad, plaintext []byte) (ct, wrapped []byte, err error) {
	dk := make([]byte, 32)
	if _, err := rand.Read(dk); err != nil {
		return nil, nil, err
	}
	if ct, err = seal(dk, plaintext, aad); err != nil {
		return nil, nil, err
	}
	if wrapped, err = seal(wrapKey, dk, aad); err != nil {
		return nil, nil, err
	}
	return ct, wrapped, nil
}

// Get decrypts a secret.
func (s *Store) Get(ctx context.Context, q store.Querier, id string) ([]byte, error) {
	var kind, orgID string
	var ct, wrapped []byte
	if err := store.QueryRow(ctx, q, `SELECT kind, ciphertext, data_key, COALESCE(org_id, '') FROM secrets WHERE id = ?`, id).Scan(&kind, &ct, &wrapped, &orgID); err != nil {
		return nil, store.NotFound(err)
	}
	wrapKey, err := s.wrapKey(ctx, q, orgID)
	if err != nil {
		return nil, err
	}
	aad := aadFor(id, kind, orgID)
	dk, err := open(wrapKey, wrapped, aad)
	if err != nil {
		return nil, err
	}
	return open(dk, ct, aad)
}

// ErrNoOrgKey means the org's key was destroyed: its secrets are gone.
var ErrNoOrgKey = errors.New("secrets: the organization's key was destroyed")

// wrapKey is the key that wraps data keys: the KEK for instance secrets,
// the org key otherwise.
func (s *Store) wrapKey(ctx context.Context, q store.Querier, orgID string) ([]byte, error) {
	if orgID == "" {
		return s.kek, nil
	}
	return s.orgKey(ctx, q, orgID, false)
}

func orgAAD(orgID string) []byte { return []byte("org\x00" + orgID) }

// orgKey unwraps the org's key, creating one first when create is set.
func (s *Store) orgKey(ctx context.Context, q store.Querier, orgID string, create bool) ([]byte, error) {
	var wrapped []byte
	err := store.QueryRow(ctx, q, `SELECT wrapped_key FROM org_keys WHERE org_id = ?`, orgID).Scan(&wrapped)
	if errors.Is(err, sql.ErrNoRows) && create {
		if err := s.createOrgKey(ctx, q, orgID); err != nil {
			return nil, err
		}
		// Another writer may have created it first: read what won.
		err = store.QueryRow(ctx, q, `SELECT wrapped_key FROM org_keys WHERE org_id = ?`, orgID).Scan(&wrapped)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoOrgKey
	}
	if err != nil {
		return nil, err
	}
	return open(s.kek, wrapped, orgAAD(orgID))
}

func (s *Store) createOrgKey(ctx context.Context, q store.Querier, orgID string) error {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return err
	}
	w, err := seal(s.kek, k, orgAAD(orgID))
	if err != nil {
		return err
	}
	_, err = store.Exec(ctx, q, `INSERT INTO org_keys (org_id, wrapped_key, created_at) VALUES (?, ?, ?) ON CONFLICT (org_id) DO NOTHING`,
		orgID, w, store.Millis(time.Now()))
	return err
}

// DestroyOrgKey deletes an org's key and its secrets (crypto-shredding: a
// backup taken before still holds only ciphertext the key no longer opens).
func (s *Store) DestroyOrgKey(ctx context.Context, q store.Querier, orgID string) error {
	if _, err := store.Exec(ctx, q, `DELETE FROM secrets WHERE org_id = ?`, orgID); err != nil {
		return err
	}
	_, err := store.Exec(ctx, q, `DELETE FROM org_keys WHERE org_id = ?`, orgID)
	return err
}

// RotateOrgKey gives an org a new key and re-wraps its secrets' data keys.
func (s *Store) RotateOrgKey(ctx context.Context, orgID string) (int, error) {
	n := 0
	err := s.db.InTx(ctx, func(tx *store.Tx) error {
		old, err := s.orgKey(ctx, tx, orgID, false)
		if err != nil {
			return err
		}
		next := make([]byte, 32)
		if _, err := rand.Read(next); err != nil {
			return err
		}
		all, err := rowsOf(ctx, tx, `SELECT id, kind, data_key FROM secrets WHERE org_id = ?`, orgID)
		if err != nil {
			return err
		}
		now := store.Millis(time.Now())
		for _, r := range all {
			aad := aadFor(r.id, r.kind, orgID)
			dk, err := open(old, r.wrapped, aad)
			if err != nil {
				return fmt.Errorf("secret %s: %w", r.id, err)
			}
			rewrapped, err := seal(next, dk, aad)
			if err != nil {
				return err
			}
			if _, err := store.Exec(ctx, tx, `UPDATE secrets SET data_key = ?, rotated_at = ? WHERE id = ?`, rewrapped, now, r.id); err != nil {
				return err
			}
			n++
		}
		w, err := seal(s.kek, next, orgAAD(orgID))
		if err != nil {
			return err
		}
		_, err = store.Exec(ctx, tx, `UPDATE org_keys SET wrapped_key = ?, rotated_at = ? WHERE org_id = ?`, w, now, orgID)
		return err
	})
	return n, err
}

type secretRow struct {
	id, kind string
	wrapped  []byte
}

func rowsOf(ctx context.Context, q store.Querier, query string, args ...any) ([]secretRow, error) {
	rows, err := store.Query(ctx, q, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []secretRow
	for rows.Next() {
		var r secretRow
		if err := rows.Scan(&r.id, &r.kind, &r.wrapped); err != nil {
			return nil, err
		}
		all = append(all, r)
	}
	return all, rows.Err()
}

// Delete removes a secret.
func (s *Store) Delete(ctx context.Context, q store.Querier, id string) error {
	_, err := store.Exec(ctx, q, `DELETE FROM secrets WHERE id = ?`, id)
	return err
}

// RotateKEK re-wraps every instance secret's data key and every org key
// from the current KEK to newKEK in one transaction. On success the Store
// uses newKEK.
func (s *Store) RotateKEK(ctx context.Context, newKEK []byte) (int, error) {
	if len(newKEK) != 32 {
		return 0, fmt.Errorf("secrets: new key must be 32 bytes")
	}
	n := 0
	err := s.db.InTx(ctx, func(tx *store.Tx) error {
		all, err := rowsOf(ctx, tx, `SELECT id, kind, data_key FROM secrets WHERE org_id IS NULL`)
		if err != nil {
			return err
		}
		now := store.Millis(time.Now())
		keys, err := rowsOf(ctx, tx, `SELECT org_id, '', wrapped_key FROM org_keys`)
		if err != nil {
			return err
		}
		for _, k := range keys {
			key, err := open(s.kek, k.wrapped, orgAAD(k.id))
			if err != nil {
				return fmt.Errorf("org key %s: %w", k.id, err)
			}
			w, err := seal(newKEK, key, orgAAD(k.id))
			if err != nil {
				return err
			}
			if _, err := store.Exec(ctx, tx, `UPDATE org_keys SET wrapped_key = ?, kek_version = kek_version + 1, rotated_at = ? WHERE org_id = ?`, w, now, k.id); err != nil {
				return err
			}
			n++
		}
		for _, r := range all {
			aad := aadFor(r.id, r.kind, "")
			dk, err := open(s.kek, r.wrapped, aad)
			if err != nil {
				return fmt.Errorf("secret %s: %w", r.id, err)
			}
			rewrapped, err := seal(newKEK, dk, aad)
			if err != nil {
				return err
			}
			if _, err := store.Exec(ctx, tx, `UPDATE secrets SET data_key = ?, kek_version = kek_version + 1, rotated_at = ? WHERE id = ?`, rewrapped, now, r.id); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	s.kek = append([]byte(nil), newKEK...)
	return n, nil
}
