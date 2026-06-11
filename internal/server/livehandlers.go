package server

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// dagSVG renders the execution's group-level dependency DAG (default depth 2).
func (a *App) dagSVG(g events.Graph) []byte {
	depth := a.cfg.GroupDepth
	if depth == 0 {
		depth = 2
	}
	return renderGroupSVG(g, depth)
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
	report := failuresSection(g, e.LogURL) + e.ReportMarkdown
	var panel string
	if targets, terr := store.TargetsFor(a.db, e.PR, e.Environment); terr == nil {
		panel = approvalPanel(targets)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(a.livePage(liveView{
		Exec:        e.ID,
		Repo:        e.Repo,
		Environment: e.Environment,
		Report:      report,
		Phase:       events.Phase(e.Phase),
		Stacks:      g.Stacks,
		SVG:         string(a.dagSVG(g)),
		Panel:       panel,
	})))
}

// stackView is the data the per-stack detail template renders.
type stackView struct {
	Exec, Repo, Environment, Stack string
	Plan, LogExcerpt               string
	VerifyExec                     string // latest verify run id for this PR/env ("" if none)
}

// stackPage renders the per-stack detail page. All fields are escaped text.
func (a *App) stackPage(v stackView) string {
	var buf bytes.Buffer
	_ = a.tmpl.ExecuteTemplate(&buf, "stack.gohtml", v)
	return buf.String()
}

// handleStackDetail serves the per-stack Log/Plan/Verify tabs. Public, behind the
// unguessable execution id.
func (a *App) handleStackDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stack := r.PathValue("stack")
	e, err := store.GetExecution(a.db, id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	_, plan, _, _ := store.GetStackOutput(a.db, id, stack, "plan")
	_, logExcerpt, _, _ := store.GetStackOutput(a.db, id, stack, "log")
	verifyExec, _ := store.LatestVerifyExecutionID(a.db, e.PR, e.Environment)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(a.stackPage(stackView{
		Exec: id, Repo: e.Repo, Environment: e.Environment, Stack: stack,
		Plan: plan, LogExcerpt: logExcerpt,
		VerifyExec: verifyExec,
	})))
}
