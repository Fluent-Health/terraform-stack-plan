package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"

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
	report := failuresSection(g, e.LogURL, a.baseURL(r)+"/logs/"+e.ID) + e.ReportMarkdown
	var panel string
	if targets, terr := store.TargetsFor(a.db, e.PR, e.Environment); terr == nil {
		panel = approvalPanel(targets)
	}
	// Per-stack detail: the rendered plan diff (when planned) and a recent log
	// excerpt, so a row click scrolls to that stack's diff/log on the same page.
	reports := map[string]string{}
	logs := map[string]string{}
	for _, s := range g.Stacks {
		if _, md, ok, _ := store.GetStackOutput(a.db, e.ID, s.Path, "plan"); ok && md != "" {
			reports[s.Path] = md
		}
		if _, ex, ok, _ := store.GetStackOutput(a.db, e.ID, s.Path, "log"); ok && ex != "" {
			logs[s.Path] = ex
		}
	}
	verifyExec, _ := store.LatestVerifyExecutionID(a.db, e.PR, e.Environment)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(a.livePage(liveView{
		Exec:         e.ID,
		Repo:         e.Repo,
		Environment:  e.Environment,
		Report:       report,
		PR:           e.PR,
		SHA:          e.SHA,
		Context:      e.StatusContext,
		Status:       e.Status,
		Phase:        events.Phase(e.Phase),
		CreatedAt:    e.CreatedAt,
		Stacks:       g.Stacks,
		StackReports: reports,
		StackLogs:    logs,
		VerifyExec:   verifyExec,
		SVG:          string(a.dagSVG(g)),
		Panel:        panel,
	})))
}

// handleLiveEvents streams Server-Sent Events for an execution: a "changed" event
// whenever its state mutates (published from drive), plus a periodic comment
// heartbeat so idle connections survive proxies.
func (a *App) handleLiveEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := store.GetExecution(a.db, id); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	ch, unsub := a.hub.subscribe("exec:" + id)
	defer unsub()
	tick := time.NewTicker(25 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			writeSSE(w, "changed")
			flusher.Flush()
		case <-tick.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
