package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestHandlerFallback(t *testing.T) {
	f := fstest.MapFS{
		"index.html":      {Data: []byte("<html>app</html>")},
		"assets/app-1.js": {Data: []byte("js")},
		"favicon.svg":     {Data: []byte("<svg/>")},
	}
	h := Handler(f)
	cases := []struct {
		path, want string
		code       int
		cache      string
	}{
		{"/", "<html>app</html>", 200, "no-cache"},
		{"/northwind/handbook/docs/a.md", "<html>app</html>", 200, "no-cache"}, // page route
		{"/northwind/handbook/revisions", "<html>app</html>", 200, "no-cache"},
		{"/assets/app-1.js", "js", 200, "public, max-age=31536000, immutable"},
		{"/assets/missing.js", "", 404, ""},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if rec.Code != c.code {
			t.Errorf("%s: code %d want %d", c.path, rec.Code, c.code)
		}
		if c.want != "" && rec.Body.String() != c.want {
			t.Errorf("%s: body %q", c.path, rec.Body.String())
		}
		if c.cache != "" && rec.Header().Get("Cache-Control") != c.cache {
			t.Errorf("%s: cache %q", c.path, rec.Header().Get("Cache-Control"))
		}
	}
}

func TestNotBuilt(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(fstest.MapFS{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 200 || Built(fstest.MapFS{}) {
		t.Fatal("expected placeholder page")
	}
}
