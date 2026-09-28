// Package content serves user-controlled bytes (raw repo files, revision
// uploads) from a separate origin when server.content_base_url is set
// (15-security.md T4). The app origin checks access and redirects to a
// short-lived signed URL; the content origin has no cookies and serves only
// what a valid signature names.
package content

import (
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// Window is how long a signed URL stays stable. A URL is valid for one to
// two windows, so browsers can cache it while it lasts.
const Window = 5 * time.Minute

// Blob is what a resolver returns for a signed URL.
type Blob struct {
	Type      string
	Bytes     []byte
	Immutable bool // the URL names fixed bytes (a commit sha)
}

// Resolver loads the bytes a signed URL names. params are the ones passed to
// URL; the signature has been checked.
type Resolver func(ctx context.Context, params url.Values) (Blob, error)

// ErrNotFound makes the handler answer 404.
var ErrNotFound = errors.New("content: not found")

// Origin signs and serves content URLs. A zero Origin (no base URL) is
// disabled: callers serve bytes themselves.
type Origin struct {
	base  *url.URL
	key   []byte
	now   func() time.Time
	kinds map[string]Resolver
}

// New returns an Origin for base (empty: disabled). The signing key is
// derived from the instance secret key.
func New(base string, secretKey []byte) (*Origin, error) {
	o := &Origin{now: time.Now, kinds: map[string]Resolver{}}
	if base == "" {
		return o, nil
	}
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("content: server.content_base_url must be an absolute http(s) URL")
	}
	key, err := hkdf.Key(sha256.New, secretKey, nil, "kmdn content urls v1", 32)
	if err != nil {
		return nil, err
	}
	o.base, o.key = u, key
	return o, nil
}

// Enabled reports whether user bytes go through the content origin.
func (o *Origin) Enabled() bool { return o != nil && o.base != nil }

// Host is the content origin's host (empty when disabled).
func (o *Origin) Host() string {
	if !o.Enabled() {
		return ""
	}
	return o.base.Host
}

// BaseURL is the content origin (empty when disabled).
func (o *Origin) BaseURL() string {
	if !o.Enabled() {
		return ""
	}
	return o.base.String()
}

// Register adds the resolver for a kind of URL.
func (o *Origin) Register(kind string, r Resolver) { o.kinds[kind] = r }

// URL signs params for kind. name is the file name the URL ends with, so
// downloads and devtools show something readable; it isn't trusted.
func (o *Origin) URL(kind string, params url.Values, name string) string {
	q := url.Values{}
	for k, v := range params {
		q[k] = v
	}
	w := int64(Window / time.Second)
	q.Set("exp", strconv.FormatInt((o.now().Unix()/w+2)*w, 10))
	payload := kind + "\x00" + q.Encode()
	tok := base64.RawURLEncoding.EncodeToString([]byte(q.Encode())) + "." + base64.RawURLEncoding.EncodeToString(o.sign(payload))
	return o.base.String() + "/c/" + kind + "/" + tok + "/" + url.PathEscape(path.Base("/"+name))
}

func (o *Origin) sign(payload string) []byte {
	m := hmac.New(sha256.New, o.key)
	m.Write([]byte(payload))
	return m.Sum(nil)
}

// verify returns the params a token signs for kind, if it is valid and live.
func (o *Origin) verify(kind, tok string) (url.Values, time.Time, bool) {
	enc, sig, ok := strings.Cut(tok, ".")
	if !ok {
		return nil, time.Time{}, false
	}
	raw, err1 := base64.RawURLEncoding.DecodeString(enc)
	mac, err2 := base64.RawURLEncoding.DecodeString(sig)
	if err1 != nil || err2 != nil || !hmac.Equal(mac, o.sign(kind+"\x00"+string(raw))) {
		return nil, time.Time{}, false
	}
	q, err := url.ParseQuery(string(raw))
	if err != nil {
		return nil, time.Time{}, false
	}
	exp, err := strconv.ParseInt(q.Get("exp"), 10, 64)
	if err != nil || o.now().Unix() >= exp {
		return nil, time.Time{}, false
	}
	q.Del("exp")
	return q, time.Unix(exp, 0), true
}

// Handler serves GET /c/{kind}/{token}/{name} on the content origin, and
// nothing else. It never reads or sets cookies.
func (o *Origin) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
		h.Set("Cross-Origin-Resource-Policy", "same-site")
		h.Set("Referrer-Policy", "no-referrer")
		if r.URL.Path == "/robots.txt" {
			h.Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n"))
			return
		}
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/c/"), "/", 3)
		if (r.Method != http.MethodGet && r.Method != http.MethodHead) || !strings.HasPrefix(r.URL.Path, "/c/") || len(parts) != 3 {
			http.NotFound(w, r)
			return
		}
		res, ok := o.kinds[parts[0]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		params, exp, ok := o.verify(parts[0], parts[1])
		if !ok {
			http.Error(w, "This link has expired. Reload the page to get a new one.", http.StatusForbidden)
			return
		}
		b, err := res(r.Context(), params)
		if errors.Is(err, ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if age := int(exp.Sub(o.now()).Seconds()); b.Immutable && age > 0 {
			h.Set("Cache-Control", "private, max-age="+strconv.Itoa(age))
		} else {
			h.Set("Cache-Control", "private, no-cache")
		}
		Write(w, b.Type, b.Bytes)
	})
}

// Write sets the headers every response carrying user bytes has, on either
// origin, and writes b. Content types are fixed by the caller, never sniffed,
// and documents run in a CSP sandbox without scripts.
func Write(w http.ResponseWriter, contentType string, b []byte) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	h.Set("Content-Disposition", "inline")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	_, _ = w.Write(b)
}
