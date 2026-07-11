package server

import (
	"net/http"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// dagSVG renders the execution's dependency DAG. For graphs with ≤ 40 stacks
// the per-stack graph (renderSVG) is used so the user can see individual nodes;
// larger graphs fall back to the group-level view (renderGroupSVG) for readability.
func (a *App) dagSVG(g events.Graph) []byte {
	if len(g.Stacks) <= 40 {
		return renderSVG(g)
	}
	depth := a.cfg.GroupDepth
	if depth == 0 {
		depth = 2
	}
	return renderGroupSVG(g, depth, a.groupRE)
}

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
	w.Write(a.dagSVG(g))
}

// handleLive renders the auto-refreshing execution page (diagram + approval
