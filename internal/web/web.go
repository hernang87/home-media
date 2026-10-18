// Package web serves the phone-friendly control page embedded in the binary.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var static embed.FS

// Routes mounts the page at / and its assets under /app/ (see Deps.Extra in
// internal/api). It only serves files; the page talks to the existing /api routes.
func Routes(mux *http.ServeMux) {
	assets, err := fs.Sub(static, "static")
	if err != nil {
		panic(err) // the embedded directory always exists
	}
	files := http.FileServerFS(assets)
	// no-cache makes the browser revalidate, so a new build shows up on reload.
	noCache := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-cache")
			next.ServeHTTP(w, r)
		})
	}
	mux.Handle("GET /{$}", noCache(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, assets, "index.html")
	})))
	mux.Handle("GET /app/", noCache(http.StripPrefix("/app/", files)))
}
