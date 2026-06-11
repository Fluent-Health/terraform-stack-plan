package server

import (
	"bytes"
	"net/http"
	"strconv"

	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// execRow is one row of the execution list pages.
type execRow struct {
	ID, Repo, Environment, Phase, Status, StatusBadge, When string
	PR                                                      int
}

// execStatusBadge maps an execution-level commit status to a DaisyUI badge class.
func execStatusBadge(status string) string {
	switch status {
	case "success":
		return "badge-success"
	case "failure":
		return "badge-error"
	case "in_progress":
		return "badge-info"
	default:
		return "badge-ghost"
	}
}

func toRows(execs []store.Execution) []execRow {
	rows := make([]execRow, 0, len(execs))
	for _, e := range execs {
		rows = append(rows, execRow{
			ID: e.ID, Repo: e.Repo, Environment: e.Environment, Phase: e.Phase,
			Status: e.Status, StatusBadge: execStatusBadge(e.Status),
			When: e.CreatedAt.Format("2006-01-02 15:04"), PR: e.PR,
		})
	}
	return rows
}

func (a *App) renderExecutions(w http.ResponseWriter, title string, execs []store.Execution) {
	var buf bytes.Buffer
	_ = a.tmpl.ExecuteTemplate(&buf, "executions.gohtml", struct {
		Title string
		Rows  []execRow
	}{Title: title, Rows: toRows(execs)})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

// handleIndex lists the most recent executions.
func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	execs, err := store.ListExecutions(a.db, 100)
	if err != nil {
		http.Error(w, "list executions", http.StatusInternalServerError)
		return
	}
	a.renderExecutions(w, "Executions", execs)
}

// handlePRTimeline lists a PR's executions, newest first.
func (a *App) handlePRTimeline(w http.ResponseWriter, r *http.Request) {
	pr, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		http.Error(w, "bad pr", http.StatusBadRequest)
		return
	}
	execs, err := store.ListExecutionsForPR(a.db, pr)
	if err != nil {
		http.Error(w, "list executions", http.StatusInternalServerError)
		return
	}
	a.renderExecutions(w, "PR #"+strconv.Itoa(pr), execs)
}
