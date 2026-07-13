package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/config"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

type executionResponse struct {
	store.Execution
	// VerifyExecutionID is the latest verify execution for the same
	// (pr, environment), "" when none. Additive (2026-07); snake_case unlike
	// its frozen PascalCase siblings.
	VerifyExecutionID string                `json:"verify_execution_id"`
	Graph             events.Graph          `json:"graph"`
	Gates             []store.GateTarget    `json:"gates"`
	ProgressPct       int                   `json:"ProgressPct"`
	ProgressLabel     string                `json:"ProgressLabel"`
	ChangeReasons     []events.ChangeReason `json:"change_reasons"`
}

func (a *App) handleGetExecution(w http.ResponseWriter, _ *http.Request, id string) {
	e, err := store.GetExecution(a.db, id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	g, err := store.LoadGraph(a.db, id)
	if err != nil {
		http.Error(w, "load graph failed", http.StatusInternalServerError)
		return
	}
	gates, err := store.TargetsFor(a.db, e.PR, e.Environment)
	if err != nil {
		gates = []store.GateTarget{}
	}
	verifyExec, _ := store.LatestVerifyExecutionID(a.db, e.PR, e.Environment)

	done, initialized, total := countStacks(g.Stacks)
	var weights []config.PhaseWeight
	if a.cfg.Progress != nil {
		if isApplyContext(e.StatusContext) {
			weights = a.cfg.Progress.Apply
		} else {
			weights = a.cfg.Progress.Plan
		}
	}
	_, fallbackLabel, fallbackPct := progress(weights, events.Phase(e.Phase), done, initialized, total)

	progressLabel := fallbackLabel
	if e.ProgressLabel.Valid {
		progressLabel = e.ProgressLabel.String
	}
	progressPct := fallbackPct
	if e.ProgressPct.Valid {
		progressPct = int(e.ProgressPct.Int64)
	}

	var changeReasons []events.ChangeReason
	if e.ChangeReasons != "" {
		_ = json.Unmarshal([]byte(e.ChangeReasons), &changeReasons)
	}
	if changeReasons == nil {
		changeReasons = []events.ChangeReason{}
	}

	res := executionResponse{
		Execution:         e,
		VerifyExecutionID: verifyExec,
		Graph:             g,
		Gates:             gates,
		ProgressPct:       progressPct,
		ProgressLabel:     progressLabel,
		ChangeReasons:     changeReasons,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

func (a *App) handleGetExecutionEvents(w http.ResponseWriter, r *http.Request) {
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
		case msg := <-ch:
			if strings.HasPrefix(msg, "superseded:") {
				newID := strings.TrimPrefix(msg, "superseded:")
				fmt.Fprintf(w, "event: superseded\ndata: %s\n\n", newID)
			} else {
				writeSSE(w, "changed")
			}
			flusher.Flush()
		case <-tick.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func countStacks(stacks []events.StackState) (done, initialized, total int) {
	DONE := map[string]bool{"planned": true, "safe": true, "nochange": true, "failed": true, "aborted": true, "gated": true, "moving": true}
	for _, s := range stacks {
		total++
		if s.Status == events.StatusInitialized || s.Status == events.StatusPlanned || s.Status == events.StatusSafe || s.Status == events.StatusNochange || s.Status == events.StatusMoving || s.Status == events.StatusFailed || s.Status == events.StatusAborted || s.Status == events.StatusGated {
			initialized++
		}
		if DONE[string(s.Status)] {
			done++
		}
	}
	return
}
