// Package web embeds the built single-page app (web/dist, copied here by
// `make web`) and serves it with a history-API fallback to index.html.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// FS returns the embedded SPA files.
func FS() fs.FS {
	sub, _ := fs.Sub(dist, "dist")
	return sub
}

// Built reports whether a real SPA build is embedded.
func Built(f fs.FS) bool {
	_, err := fs.Stat(f, "index.html")
	return err == nil
}

const notBuilt = `<!doctype html><meta charset="utf-8"><title>kmdn</title>
<body style="font:15px system-ui;margin:4rem auto;max-width:36rem;padding:0 1rem">
<h1>The web app isn't built into this binary</h1>
<p>Run <code>make build</code>, or <code>make dev</code> for the Vite dev server.
The API is available under <code>/api/v1</code>.</p></body>`

// Handler serves static assets from f. Unknown paths fall back to index.html so
// client-side routes work on reload (page routes end in .md, so extensions are
// no signal); only missing files under /assets/ return 404. Hashed assets
// under /assets/ are cached for a year; everything else is revalidated.
func Handler(f fs.FS) http.Handler {
	if !Built(f) {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(notBuilt))
		})
	}
	files := http.FileServerFS(f)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(f, p); err != nil {
			if strings.HasPrefix(p, "assets/") {
				http.NotFound(w, r)
				return
			}
			serveIndex(w, r, f)
			return
		}
		if p == "index.html" {
			serveIndex(w, r, f)
			return
		}
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, f fs.FS) {
	b, err := fs.ReadFile(f, "index.html")
	if err != nil {
		http.Error(w, "index.html missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}
