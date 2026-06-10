package server

import (
	"encoding/json"
	"net/http"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func (a *App) handleInit(w http.ResponseWriter, r *http.Request) {
	var in events.Init
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		badRequest(w, err)
		return
	}
	if err := store.UpsertInit(a.db, in); err != nil {
		http.Error(w, "store init", http.StatusInternalServerError)
		return
	}
	base := a.baseURL(r)
	if isGate(in.Context, in.Environment) && a.cfg.UseChecks {
		if err := a.ensureCheckRun(r.Context(), in.ID, in.Repo, in.SHA, in.Environment, liveURL(base, in.ID)); err != nil {
			http.Error(w, "create check run", http.StatusBadGateway)
			return
		}
	}
	a.drive(r.Context(), in.ID, base, false)
	w.WriteHeader(http.StatusOK)
}

func (a *App) handlePhase(w http.ResponseWriter, r *http.Request) {
	var p events.PhaseEvent
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		badRequest(w, err)
		return
	}
	if err := store.UpsertPhase(a.db, p); err != nil {
		http.Error(w, "store phase", http.StatusInternalServerError)
		return
	}
	e, err := store.GetExecution(a.db, p.ID)
	if err != nil {
		http.Error(w, "read execution", http.StatusInternalServerError)
		return
	}
	base := a.baseURL(r)
	if isGate(e.StatusContext, e.Environment) && a.cfg.UseChecks {
		if err := a.ensureCheckRun(r.Context(), e.ID, e.Repo, e.SHA, e.Environment, liveURL(base, e.ID)); err != nil {
			http.Error(w, "create check run", http.StatusBadGateway)
			return
		}
	}
	a.drive(r.Context(), p.ID, base, false)
	w.WriteHeader(http.StatusOK)
}

func (a *App) handleUpdate(w http.ResponseWriter, r *http.Request) {
	var u events.Update
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		badRequest(w, err)
		return
	}
	if err := store.UpdateStack(a.db, u.ID, u.Stack, u.Status, u.Detail); err != nil {
		http.Error(w, "update stack", http.StatusInternalServerError)
		return
	}
	a.drive(r.Context(), u.ID, a.baseURL(r), false)
	w.WriteHeader(http.StatusOK)
}

// REMOVE in Task 6: temporary finalize stub so the package compiles.
func (a *App) handleFinalize(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}
