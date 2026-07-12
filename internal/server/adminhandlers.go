package server

import (
	"encoding/json"
	"net/http"

	"github.com/Fluent-Health/terraform-stack-plan/internal/api"
	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func (a *App) handleAdminGrantsRelease(w http.ResponseWriter, r *http.Request) {
	var req api.AdminGrantsReleaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	actor := Actor(r)
	streamID := execStreamID(req.Pr, req.Environment)

	// Load stream current version
	_, ver, err := a.gateDecider.Load(a.eventStore, streamID)
	if err != nil {
		http.Error(w, "load changeset", http.StatusInternalServerError)
		return
	}

	evs := []reconcile.Event{
		reconcile.AdminGrantReleased{
			PR:          req.Pr,
			Environment: req.Environment,
			Class:       req.Class,
			Target:      req.Target,
			Actor:       actor,
			Reason:      req.Reason,
		},
	}

	state, _, _ := a.gateDecider.Load(a.eventStore, streamID)
	state.PR = req.Pr
	state.Environment = req.Environment
	state = a.gateDecider.Evolve(state, evs[0])

	if err := a.gateDecider.Append(a.eventStore, streamID, ver, evs, state); err != nil {
		http.Error(w, "append event", http.StatusInternalServerError)
		return
	}

	if a.Approval != nil {
		_ = a.Approval.Revoke(r.Context(), approval.Request{
			PR:          req.Pr,
			Environment: req.Environment,
			Class:       req.Class,
			Target:      req.Target,
		})
	}

	// Trigger tick to reconcile
	a.reconcileBackground(req.Pr, req.Environment)
	w.WriteHeader(http.StatusOK)
}

func (a *App) handleAdminExecutionsCancel(w http.ResponseWriter, r *http.Request) {
	var req api.AdminExecutionsCancelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	actor := Actor(r)

	exec, err := store.GetExecution(a.db, req.Id)
	if err != nil {
		http.Error(w, "get execution", http.StatusBadRequest)
		return
	}

	streamID := execStreamID(exec.PR, exec.Environment)
	_, ver, err := a.gateDecider.Load(a.eventStore, streamID)
	if err != nil {
		http.Error(w, "load changeset", http.StatusInternalServerError)
		return
	}

	evs := []reconcile.Event{
		reconcile.AdminExecutionCancelled{
			Kind:        exec.StatusContext,
			ExecutionID: req.Id,
			Actor:       actor,
			Reason:      req.Reason,
		},
		reconcile.RunCompleted{
			Kind:        exec.Phase,
			ExecutionID: req.Id,
		},
	}

	state, _, _ := a.gateDecider.Load(a.eventStore, streamID)
	state.PR = exec.PR
	state.Environment = exec.Environment
	state = a.gateDecider.Evolve(state, evs[0])
	state = a.gateDecider.Evolve(state, evs[1])

	if err := a.gateDecider.Append(a.eventStore, streamID, ver, evs, state); err != nil {
		http.Error(w, "append event", http.StatusInternalServerError)
		return
	}

	a.reconcileBackground(exec.PR, exec.Environment)
	w.WriteHeader(http.StatusOK)
}

func (a *App) handleAdminGatesSatisfy(w http.ResponseWriter, r *http.Request) {
	var req api.AdminGatesSatisfyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	actor := Actor(r)
	streamID := execStreamID(req.Pr, req.Environment)

	_, ver, err := a.gateDecider.Load(a.eventStore, streamID)
	if err != nil {
		http.Error(w, "load changeset", http.StatusInternalServerError)
		return
	}

	evs := []reconcile.Event{
		reconcile.AdminGateSatisfied{
			PR:          req.Pr,
			Environment: req.Environment,
			Class:       req.Class,
			Target:      req.Target,
			Actor:       actor,
			Reason:      req.Reason,
		},
		reconcile.GrantObserved{
			Class:     req.Class,
			Target:    req.Target,
			Name:      "admin-override",
			State:     approval.StateActive,
			Requester: actor,
		},
	}

	state, _, _ := a.gateDecider.Load(a.eventStore, streamID)
	state.PR = req.Pr
	state.Environment = req.Environment
	state = a.gateDecider.Evolve(state, evs[0])
	state = a.gateDecider.Evolve(state, evs[1])

	if err := a.gateDecider.Append(a.eventStore, streamID, ver, evs, state); err != nil {
		http.Error(w, "append event", http.StatusInternalServerError)
		return
	}

	a.reconcileBackground(req.Pr, req.Environment)
	w.WriteHeader(http.StatusOK)
}

func (a *App) handleAdminChecksOverride(w http.ResponseWriter, r *http.Request) {
	var req api.AdminChecksOverrideRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	actor := Actor(r)
	streamID := execStreamID(req.Pr, req.Environment)

	_, ver, err := a.gateDecider.Load(a.eventStore, streamID)
	if err != nil {
		http.Error(w, "load changeset", http.StatusInternalServerError)
		return
	}

	evs := []reconcile.Event{
		reconcile.AdminCheckOverridden{
			PR:         req.Pr,
			Env:        req.Environment,
			CheckName:  req.Check,
			Conclusion: req.Conclusion,
			Actor:      actor,
			Reason:     req.Reason,
		},
	}

	state, _, _ := a.gateDecider.Load(a.eventStore, streamID)
	state.PR = req.Pr
	state.Environment = req.Environment
	state = a.gateDecider.Evolve(state, evs[0])

	if err := a.gateDecider.Append(a.eventStore, streamID, ver, evs, state); err != nil {
		http.Error(w, "append event", http.StatusInternalServerError)
		return
	}

	a.reconcileBackground(req.Pr, req.Environment)
	w.WriteHeader(http.StatusOK)
}
