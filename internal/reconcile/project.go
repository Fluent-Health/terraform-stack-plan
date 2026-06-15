package reconcile

import "github.com/Fluent-Health/terraform-stack-plan/internal/events"

// DisplayStatus is the derived per-stack status shown in the UI/check run. The
// runner-told status wins when terminal (failed/moving); otherwise the gating
// overlay (gated/safe) is derived from the ChangeSet's GateState. This is the
// single source of the gated/safe distinction — it is never stored separately.
func DisplayStatus(s Stack, g GateState) events.Status {
	switch s.RunStatus {
	case events.StatusFailed, events.StatusMoving:
		return s.RunStatus
	}
	if !stackGated(s, g) {
		return s.RunStatus
	}
	switch g.(type) {
	case Satisfied, Clean:
		return events.StatusSafe
	default: // Pending, Blocked
		return events.StatusGated
	}
}

// stackGated reports whether stack s is governed by a gate target (matched by
// its grouping/target key).
func stackGated(s Stack, g GateState) bool {
	for _, t := range gateTargets(g) {
		if t.Target == s.Project {
			return true
		}
	}
	return false
}

// ApplyAllowed is the fail-closed apply-gate verdict: only Clean or Satisfied
// permit apply; NotClassified, Pending, and Blocked deny it.
func ApplyAllowed(g GateState) bool {
	switch g.(type) {
	case Clean, Satisfied:
		return true
	}
	return false
}

// Requester returns the leased requester identity for the gate ("" if none).
func Requester(g GateState) string { return priorLease(g).Requester }
