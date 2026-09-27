package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/fxamacker/cbor/v2"

	"github.com/kmdn-app/kmdn/internal/users"
)

// softKey is a minimal software authenticator (none attestation, ES256).
type softKey struct {
	key    *ecdsa.PrivateKey
	id     []byte
	user   []byte
	origin string
	rpID   string
	count  uint32
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func newSoftKey(t *testing.T, origin, rpID string) *softKey {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return &softKey{key: k, id: id, origin: origin, rpID: rpID}
}

func (s *softKey) authData(flags byte, attested []byte) []byte {
	h := sha256.Sum256([]byte(s.rpID))
	b := append(h[:], flags)
	b = binary.BigEndian.AppendUint32(b, s.count)
	return append(b, attested...)
}

func clientData(typ, challenge, origin string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": origin, "crossOrigin": false})
	return b
}

// create answers navigator.credentials.create().
func (s *softKey) create(t *testing.T, challenge string) map[string]any {
	x, y := s.key.X.FillBytes(make([]byte, 32)), s.key.Y.FillBytes(make([]byte, 32))
	cose, _ := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	att := make([]byte, 16) // AAGUID
	att = binary.BigEndian.AppendUint16(att, uint16(len(s.id)))
	att = append(append(att, s.id...), cose...)
	ao, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": s.authData(0x01|0x04|0x08|0x40, att)})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"id": b64(s.id), "rawId": b64(s.id), "type": "public-key",
		"response": map[string]any{"clientDataJSON": b64(clientData("webauthn.create", challenge, s.origin)), "attestationObject": b64(ao)}}
}

// get answers navigator.credentials.get() for a discoverable credential.
func (s *softKey) get(t *testing.T, challenge string) map[string]any {
	ad := s.authData(0x01|0x04|0x08, nil)
	cd := clientData("webauthn.get", challenge, s.origin)
	ch := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), ch[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, s.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"id": b64(s.id), "rawId": b64(s.id), "type": "public-key",
		"response": map[string]any{"clientDataJSON": b64(cd), "authenticatorData": b64(ad), "signature": b64(sig), "userHandle": b64(s.user)}}
}

func challengeOf(t *testing.T, opts map[string]any) (ceremony, challenge string) {
	t.Helper()
	pk := opts["options"].(map[string]any)["publicKey"].(map[string]any)
	return opts["ceremony"].(string), pk["challenge"].(string)
}

func TestPasskeys(t *testing.T) {
	a, admin := newApp(t, nil)
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	key := newSoftKey(t, "http://localhost:8080", "localhost")
	key.user = []byte(maya.ID)

	// Add a passkey from the profile.
	code, opts := admin.do("POST", "/me/passkeys/register/options", map[string]any{"name": "MacBook"})
	if code != 200 {
		t.Fatalf("register options: %d %v", code, opts)
	}
	pk := opts["options"].(map[string]any)["publicKey"].(map[string]any)
	if pk["rp"].(map[string]any)["id"] != "localhost" || pk["authenticatorSelection"].(map[string]any)["residentKey"] != "required" {
		t.Fatalf("options: %v", pk)
	}
	cer, ch := challengeOf(t, opts)
	if code, body := admin.do("POST", "/me/passkeys/register/verify", map[string]any{"ceremony": cer, "credential": key.create(t, ch)}); code != 201 || body["name"] != "MacBook" {
		t.Fatalf("register: %d %v", code, body)
	}
	if code, _ := admin.do("POST", "/me/passkeys/register/verify", map[string]any{"ceremony": cer, "credential": key.create(t, ch)}); code != 400 {
		t.Fatalf("ceremony reused: %d", code)
	}
	_, list := admin.do("GET", "/me/passkeys", nil)
	items := list["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["synced"] != true {
		t.Fatalf("passkeys: %v", list)
	}
	pkID := items[0].(map[string]any)["id"].(string)
	// The same authenticator can't be added twice (it's excluded).
	_, opts = admin.do("POST", "/me/passkeys/register/options", map[string]any{})
	if ex := opts["options"].(map[string]any)["publicKey"].(map[string]any)["excludeCredentials"].([]any); len(ex) != 1 {
		t.Fatalf("exclusions: %v", ex)
	}

	// Sign in with it on another browser, no email needed.
	anon := &tc{t: t, base: admin.base, c: newClient()}
	signInWithKey := func(k *softKey) (int, map[string]any) {
		_, o := anon.do("POST", "/auth/passkey/options", map[string]any{})
		cer, ch := challengeOf(t, o)
		return anon.do("POST", "/auth/passkey/verify", map[string]any{"ceremony": cer, "credential": k.get(t, ch)})
	}
	key.count = 5
	if code, body := signInWithKey(key); code != 200 {
		t.Fatalf("passkey sign-in: %d %v", code, body)
	}
	if code, me := anon.do("GET", "/me", nil); code != 200 || me["id"] != maya.ID {
		t.Fatalf("not signed in: %d %v", code, me)
	}
	_, list = admin.do("GET", "/me/passkeys", nil)
	if list["items"].([]any)[0].(map[string]any)["last_used_at"] == nil {
		t.Fatal("last use not recorded")
	}

	// A counter going backwards means a cloned key.
	key.count = 3
	if code, _ := signInWithKey(key); code != 401 {
		t.Fatalf("cloned key accepted: %d", code)
	}
	// Another site's origin, and unknown keys, are refused.
	key.count = 9
	phish := *key
	phish.origin = "https://kmdn.example.evil"
	if code, _ := signInWithKey(&phish); code != 401 {
		t.Fatalf("wrong origin accepted: %d", code)
	}
	stranger := newSoftKey(t, "http://localhost:8080", "localhost")
	stranger.user = []byte(maya.ID)
	if code, _ := signInWithKey(stranger); code != 401 {
		t.Fatalf("unknown key accepted: %d", code)
	}

	// Rename, then remove: it can't sign in anymore.
	if code, _ := admin.do("PATCH", "/me/passkeys/"+pkID, map[string]any{"name": "Work laptop"}); code != 204 {
		t.Fatalf("rename: %d", code)
	}
	if code, _ := admin.do("DELETE", "/me/passkeys/"+pkID, nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	key.count = 20
	if code, _ := signInWithKey(key); code != 401 {
		t.Fatalf("removed key accepted: %d", code)
	}
}
