package execution

import "github.com/Fluent-Health/terraform-stack-plan/internal/events"

// Evolve applies a single domain Event to a State, returning the new state. Total
// (never panics on any state/event combination), pure, deterministic — the fold
// half of the Decider pattern.
func Evolve(s State, e Event) State {
	switch ev := e.(type) {
	case Started:
		// A fresh Init replaces the whole execution state (a re-init with the same
		// id restarts the fold from the reported subgraph).
		return ev.Exec

	case PhaseChanged:
		s.Phase = ev.Phase
		s.ProgressLabel = ev.Label
		s.ProgressPct = ev.Pct
		// Identity is set non-regressively so a phase-before-init materializes the
		// row without a later bare phase bump clobbering it (mirrors old UpsertPhase).
		if s.ID == "" {
			s.ID = ev.ID
		}
		if s.PR == 0 {
			s.PR = ev.PR
		}
		if s.Repo == "" {
			s.Repo = ev.Repo
		}
		if s.SHA == "" {
			s.SHA = ev.SHA
		}
		if s.Environment == "" {
			s.Environment = ev.Environment
		}
		if s.Context == "" {
			s.Context = ev.Context
		}
		if s.LogURL == "" {
			s.LogURL = ev.LogURL
		}
		return s

	case StackStatusChanged:
		for i := range s.Stacks {
			if s.Stacks[i].Path == ev.Stack {
				s.Stacks[i].RunStatus = ev.Status
				s.Stacks[i].Detail = ev.Detail
			}
		}
		return s

	case Failed:
		s.Status = "failure"
		// A stack still mid-run at a failed finalize did not itself fail — terramate
		// aborted it (e.g. a parallel sibling 403'd). Mark it `aborted`, not `failed`,
		// so innocent / no-change stacks are not mislabeled (matches live handleFinalize).
		for i := range s.Stacks {
			switch s.Stacks[i].RunStatus {
			case events.StatusPending, events.StatusRunning,
				events.StatusInitializing, events.StatusInitialized,
				events.StatusMoving:
				s.Stacks[i].RunStatus = events.StatusAborted
			}
		}
		return s

	case Succeeded:
		s.Status = "success"
		return s

	default:
		return s
	}
}
