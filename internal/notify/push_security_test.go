package notify

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/kmdn-app/kmdn/internal/outbound"
)

func TestPushRefusesPrivateEndpoint(t *testing.T) {
	var reached atomic.Int32
	endpoint := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer endpoint.Close()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	private, public, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	sub := Subscription{Endpoint: endpoint.URL,
		P256dh: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
		Auth:   base64.RawURLEncoding.EncodeToString(make([]byte, 16))}
	p := &Pusher{Subject: "mailto:test@example.org"}
	_, err = p.sender()(context.Background(), sub, []byte(`{"title":"Test"}`), VAPID{Public: public, Private: private})
	if !errors.Is(err, outbound.ErrPrivate) || reached.Load() != 0 {
		t.Fatalf("private push was not blocked: %v, requests %d", err, reached.Load())
	}
}
