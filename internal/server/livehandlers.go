package server

import (
	"net/http"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// handleImg renders the execution's dependency graph as an SVG. Public and
// cache-busted via the rev the server bumps on each state change.
func (a *App) handleImg(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(r.PathValue("name"), ".svg")
	g, err := store.LoadGraph(a.db, id)
	if err != nil || len(g.Stacks) == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=30")
	w.Write(renderSVG(g))
}

// handleLive renders the auto-refreshing execution page (diagram + approval
// panel + report). Public, behind an unguessable execution id.
func (a *App) handleLive(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	e, err := store.GetExecution(a.db, id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	g, err := store.LoadGraph(a.db, id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	report := failuresSection(g, e.LogURL) + e.ReportMarkdown
	var panel string
	if targets, terr := store.TargetsFor(a.db, e.PR, e.Environment); terr == nil {
		panel = approvalPanel(targets)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(a.livePage(e.Repo, e.Environment, report, string(renderSVG(g)), panel)))
}
