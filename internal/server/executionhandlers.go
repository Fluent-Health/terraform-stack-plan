package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

type executionResponse struct {
	store.Execution
	Graph events.Graph       `json:"graph"`
	Gates []store.GateTarget `json:"gates"`
}

func (a *App) handleGetExecution(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
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
	res := executionResponse{
		Execution: e,
		Graph:     g,
		Gates:     gates,
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
