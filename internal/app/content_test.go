package app

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kmdn-app/kmdn/internal/access"
	"github.com/kmdn-app/kmdn/internal/auth"
	"github.com/kmdn-app/kmdn/internal/config"
	"github.com/kmdn-app/kmdn/internal/users"
)

// With a content origin, the app origin never serves user bytes: raw
// requests are checked, then redirected to a signed URL on the content host,
// which serves them without cookies.
func TestContentOrigin(t *testing.T) {
	a, admin := newApp(t, func(c *config.Config) { c.Server.ContentBaseURL = "http://content.localhost" })
	ctx := context.Background()
	maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
	signIn(t, a, admin, maya)
	img := pngBytes(t)
	repoID := connectLocal(t, a, admin, map[string]string{
		"docs/index.md":          "# Home\n",
		"docs/images/logo.svg":   `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		"docs/images/sketch.png": string(img),
	})
	noFollow := &http.Client{Jar: admin.c.Jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	redirect := func(p string) *url.URL {
		t.Helper()
		res, err := noFollow.Get(admin.base + "/api/v1" + p)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		loc, _ := url.Parse(res.Header.Get("Location"))
		if res.StatusCode != http.StatusFound || loc.Host != "content.localhost" || !strings.HasPrefix(loc.Path, "/c/") {
			t.Fatalf("%s: %d %q", p, res.StatusCode, res.Header.Get("Location"))
		}
		return loc
	}
	// The content host is served by the same handler, told apart by Host.
	fetch := func(u *url.URL, cookie bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", u.RequestURI(), nil)
		req.Host = u.Host
		if cookie {
			req.Header.Set("Cookie", auth.SessionCookie+"=anything")
		}
		rec := httptest.NewRecorder()
		a.Server.Handler().ServeHTTP(rec, req)
		return rec
	}

	svg := redirect("/repos/" + repoID + "/raw/docs/images/logo.svg")
	rec := fetch(svg, true)
	h := rec.Header()
	if rec.Code != 200 || h.Get("Content-Type") != "image/svg+xml" || h.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(h.Get("Content-Security-Policy"), "sandbox") || strings.Contains(h.Get("Content-Security-Policy"), "allow-scripts") {
		t.Fatalf("svg on content host: %d %v", rec.Code, h)
	}
	if len(h.Values("Set-Cookie")) != 0 || h.Get("Cache-Control") == "" {
		t.Fatalf("content host set cookies or no caching: %v", h)
	}
	// Signed URLs name a commit, so they stay valid after the file changes.
	if !strings.Contains(svg.String(), "/logo.svg") {
		t.Fatalf("readable name: %s", svg)
	}

	// A tampered or foreign signature is refused, and the content host serves
	// nothing but signed URLs.
	bad := *svg
	bad.Path = strings.Replace(bad.Path, "/c/repo/", "/c/upload/", 1)
	if rec := fetch(&bad, false); rec.Code != http.StatusForbidden {
		t.Fatalf("token for another kind: %d", rec.Code)
	}
	for _, p := range []string{"/", "/api/v1/me", "/api/v1/repos/" + repoID + "/raw/docs/images/logo.svg", "/healthz"} {
		if rec := fetch(&url.URL{Host: "content.localhost", Path: p}, true); rec.Code != http.StatusNotFound {
			t.Fatalf("content host %s: %d", p, rec.Code)
		}
	}

	// Access is still checked on the app origin before signing.
	vic, _ := users.Create(ctx, a.DB, "vic@northwind.dev", "Vic", false)
	vicC := &tc{t: t, base: admin.base, c: newClient()}
	signIn(t, a, vicC, vic)
	if res, _ := vicC.c.Get(admin.base + "/api/v1/repos/" + repoID + "/raw/docs/images/logo.svg"); res.StatusCode != 404 {
		t.Fatalf("outsider raw: %d", res.StatusCode)
	}
	_ = access.Grant(ctx, a.DB, repoID, "user", vic.ID, access.Viewer)

	// Revision uploads and base files go the same way.
	_, rev := admin.do("POST", "/repos/"+repoID+"/revisions", map[string]any{"title": "Photos"})
	revID := rev["id"].(string)
	if code, up := admin.upload("/revisions/"+revID+"/assets", "docs/index.md", "desk.png", img); code != 201 {
		t.Fatalf("upload: %d %v", code, up)
	}
	for _, p := range []string{"docs/images/desk.png", "docs/images/sketch.png"} {
		u := redirect("/revisions/" + revID + "/raw/" + p)
		if rec := fetch(u, false); rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), img) || rec.Header().Get("Content-Type") != "image/png" {
			t.Fatalf("revision %s on content host: %d %q", p, rec.Code, rec.Header().Get("Content-Type"))
		}
	}

	// The app's CSP lets pages load images from the content origin.
	res, _ := admin.c.Get(admin.base + "/api/v1/me")
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if !strings.Contains(res.Header.Get("Content-Security-Policy"), "img-src 'self' data: blob: http://content.localhost;") {
		t.Fatalf("app CSP: %q", res.Header.Get("Content-Security-Policy"))
	}
}

// Strict mode refuses to start without a content origin.
func TestStrictNeedsContentOrigin(t *testing.T) {
	cfg := config.Defaults()
	cfg.Policy.Strict = true
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "content_base_url") {
		t.Fatalf("validate: %v", err)
	}
	cfg.Server.ContentBaseURL = cfg.Server.BaseURL
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "different host") {
		t.Fatalf("same host: %v", err)
	}
}

// Over https the cookies take the __Host- prefix; a cookie set before the
// prefix existed is moved to it, unless another host serves user content.
func TestHostCookies(t *testing.T) {
	for _, withContent := range []bool{false, true} {
		a, _ := newApp(t, func(c *config.Config) {
			c.Server.BaseURL = "https://docs.northwind.dev"
			if withContent {
				c.Server.ContentBaseURL = "https://content.northwind.dev"
			}
		})
		ctx := context.Background()
		maya, _ := users.Create(ctx, a.DB, "maya@northwind.dev", "Maya", true)
		token, _, err := a.Auth.CreateSession(ctx, a.DB, maya.ID, "127.0.0.1", "test")
		if err != nil {
			t.Fatal(err)
		}
		me := func(cookie string) *httptest.ResponseRecorder {
			req := httptest.NewRequest("GET", "https://docs.northwind.dev/api/v1/me", nil)
			req.Header.Set("Cookie", cookie)
			rec := httptest.NewRecorder()
			a.Server.Handler().ServeHTTP(rec, req)
			return rec
		}
		if rec := me(auth.SecurePrefix + auth.SessionCookie + "=" + token); rec.Code != 200 {
			t.Fatalf("prefixed cookie: %d", rec.Code)
		}
		rec := me(auth.SessionCookie + "=" + token)
		set := strings.Join(rec.Header().Values("Set-Cookie"), "\n")
		switch {
		case !withContent && (rec.Code != 200 || !strings.Contains(set, "__Host-kmdn_session="+token) || !strings.Contains(set, "__Host-kmdn_csrf=")):
			t.Fatalf("legacy cookie not moved: %d %s", rec.Code, set)
		case withContent && rec.Code != 401:
			t.Fatalf("legacy cookie accepted next to a content origin: %d", rec.Code)
		}
	}
}
