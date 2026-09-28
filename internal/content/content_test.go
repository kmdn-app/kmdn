package content

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSignedURLs(t *testing.T) {
	o, err := New("https://content.example.com/", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	o.now = func() time.Time { return now }
	o.Register("repo", func(_ context.Context, q url.Values) (Blob, error) {
		if q.Get("path") != "a/b.png" {
			return Blob{}, ErrNotFound
		}
		return Blob{Type: "image/png", Bytes: []byte("png"), Immutable: true}, nil
	})
	get := func(u string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		o.Handler().ServeHTTP(rec, httptest.NewRequest("GET", strings.TrimPrefix(u, "https://content.example.com"), nil))
		return rec
	}
	u := o.URL("repo", url.Values{"path": {"a/b.png"}}, "a/b.png")
	if !strings.HasPrefix(u, "https://content.example.com/c/repo/") || !strings.HasSuffix(u, "/b.png") {
		t.Fatalf("url: %s", u)
	}
	// Stable within a window, so browsers can cache it.
	if o.URL("repo", url.Values{"path": {"a/b.png"}}, "a/b.png") != u {
		t.Fatal("URL changed within a window")
	}
	if rec := get(u); rec.Code != 200 || rec.Body.String() != "png" || !strings.HasPrefix(rec.Header().Get("Cache-Control"), "private, max-age=") {
		t.Fatalf("serve: %d %v", rec.Code, rec.Header())
	}
	// Changing the signed params breaks the signature.
	forged := strings.Replace(u, "/c/repo/", "/c/repo/x", 1)
	if rec := get(forged); rec.Code != http.StatusForbidden {
		t.Fatalf("forged: %d", rec.Code)
	}
	// Expired after at most two windows.
	now = now.Add(2 * Window)
	if rec := get(u); rec.Code != http.StatusForbidden {
		t.Fatalf("expired: %d", rec.Code)
	}
	if off, _ := New("", nil); off.Enabled() {
		t.Fatal("empty base should disable")
	}
}
