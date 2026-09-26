// Package secrets stores credentials encrypted at rest with envelope
// encryption: each secret has its own random data key (AES-256-GCM), and the
// data key is encrypted with the instance key-encryption key (KEK) from
// KMDN_SECRET_KEY. Rotating the KEK only re-wraps data keys.
package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
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

// Put stores a new secret and returns its id. kind documents what it is
// (e.g. "github_app_private_key") and is bound into the ciphertext.
func (s *Store) Put(ctx context.Context, q store.Querier, kind string, plaintext []byte) (string, error) {
	id := ids.New(ids.Secret)
	ct, dk, err := s.encrypt(id, kind, plaintext)
	if err != nil {
		return "", err
	}
	_, err = store.Exec(ctx, q, `INSERT INTO secrets (id, kind, ciphertext, data_key, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, kind, ct, dk, store.Millis(time.Now()))
	return id, err
}

// Update replaces the plaintext of an existing secret (new data key).
func (s *Store) Update(ctx context.Context, q store.Querier, id string, plaintext []byte) error {
	var kind string
	if err := store.QueryRow(ctx, q, `SELECT kind FROM secrets WHERE id = ?`, id).Scan(&kind); err != nil {
		return store.NotFound(err)
	}
	ct, dk, err := s.encrypt(id, kind, plaintext)
	if err != nil {
		return err
	}
	_, err = store.Exec(ctx, q, `UPDATE secrets SET ciphertext = ?, data_key = ?, rotated_at = ? WHERE id = ?`,
		ct, dk, store.Millis(time.Now()), id)
	return err
}

func (s *Store) encrypt(id, kind string, plaintext []byte) (ct, wrapped []byte, err error) {
	dk := make([]byte, 32)
	if _, err := rand.Read(dk); err != nil {
		return nil, nil, err
	}
	aad := []byte(id + "\x00" + kind)
	if ct, err = seal(dk, plaintext, aad); err != nil {
		return nil, nil, err
	}
	if wrapped, err = seal(s.kek, dk, aad); err != nil {
		return nil, nil, err
	}
	return ct, wrapped, nil
}

// Get decrypts a secret.
func (s *Store) Get(ctx context.Context, q store.Querier, id string) ([]byte, error) {
	var kind string
	var ct, wrapped []byte
	if err := store.QueryRow(ctx, q, `SELECT kind, ciphertext, data_key FROM secrets WHERE id = ?`, id).Scan(&kind, &ct, &wrapped); err != nil {
		return nil, store.NotFound(err)
	}
	aad := []byte(id + "\x00" + kind)
	dk, err := open(s.kek, wrapped, aad)
	if err != nil {
		return nil, err
	}
	return open(dk, ct, aad)
}

// Delete removes a secret.
func (s *Store) Delete(ctx context.Context, q store.Querier, id string) error {
	_, err := store.Exec(ctx, q, `DELETE FROM secrets WHERE id = ?`, id)
	return err
}

// RotateKEK re-wraps every data key from the current KEK to newKEK in one
// transaction. On success the Store uses newKEK.
func (s *Store) RotateKEK(ctx context.Context, newKEK []byte) (int, error) {
	if len(newKEK) != 32 {
		return 0, fmt.Errorf("secrets: new key must be 32 bytes")
	}
	n := 0
	err := s.db.InTx(ctx, func(tx *store.Tx) error {
		rows, err := store.Query(ctx, tx, `SELECT id, kind, data_key FROM secrets`)
		if err != nil {
			return err
		}
		type rec struct {
			id, kind string
			wrapped  []byte
		}
		var all []rec
		for rows.Next() {
			var r rec
			if err := rows.Scan(&r.id, &r.kind, &r.wrapped); err != nil {
				rows.Close()
				return err
			}
			all = append(all, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		now := store.Millis(time.Now())
		for _, r := range all {
			aad := []byte(r.id + "\x00" + r.kind)
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
