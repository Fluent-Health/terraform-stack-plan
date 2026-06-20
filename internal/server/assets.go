package server

import (
	_ "embed"
	"net/http"
)

//go:embed assets/app.css
var appCSS []byte

//go:embed assets/report.css
var reportCSS []byte

//go:embed assets/term.js
var termJS []byte

// handleAsset serves the embedded, pre-built stylesheet. Public and immutable —
// the CSS is content-stable per build, so a long cache is safe.
func (a *App) handleAsset(w http.ResponseWriter, r *http.Request) {
	switch r.PathValue("file") {
	case "app.css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(appCSS)
	case "report.css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(reportCSS)
	case "term.js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write(termJS)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}
