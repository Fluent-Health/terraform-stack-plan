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

	default:
		return s
	}
}
