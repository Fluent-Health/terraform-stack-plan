package ui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// The SPA ships embedded from dist/. The repo commits only a placeholder
// index.html so `go build` stays toolchain-free; CI/release runs the Vite
// build (web/ui/) and overwrites dist/ before the Go build, so shipped
// binaries carry the real SPA.
//
//go:embed all:dist
var distFS embed.FS

// spaHandler serves the embedded SPA: exact asset paths when they exist,
// index.html for everything else (client-side routing). /api, /auth and
// /healthz are matched by more specific mux patterns and never land here.
func (a *App) spaHandler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err) // embed is compile-time; a missing dist/ cannot happen in a built binary
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if f, err := sub.Open(p); err == nil {
				f.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		index, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			http.Error(w, "ui assets missing", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
}
