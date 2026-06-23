package reconcile

import "github.com/Fluent-Health/terraform-stack-plan/internal/events"

// Evolve applies a single domain Event to a ChangeSet, returning the new state.
// It is total (never panics on any state/event combination), pure (no I/O), and
// deterministic. It is the fold half of the Decider pattern: Decide emits Events,
// Evolve reduces them into state.
func Evolve(cs ChangeSet, e Event) ChangeSet {
	switch ev := e.(type) {

	// --- execution facts ---

	case ExecutionStarted:
		cs.Exec = ev.Exec
		if cs.Gate == nil {
			cs.Gate = NotClassified{}
		}
		return cs

	case PhaseChanged:
		cs.Exec.Phase = ev.Phase
		return cs

	case StackStatusChanged:
		for i := range cs.Exec.Stacks {
			if cs.Exec.Stacks[i].Path == ev.Stack {
				cs.Exec.Stacks[i].RunStatus = ev.Status
				cs.Exec.Stacks[i].Detail = ev.Detail
			}
		}
		return cs

	case ExecutionFailed:
		for i := range cs.Exec.Stacks {
			switch cs.Exec.Stacks[i].RunStatus {
			case events.StatusPending, events.StatusRunning,
				events.StatusInitializing, events.StatusInitialized:
				cs.Exec.Stacks[i].RunStatus = events.StatusFailed
			}
		}
		return cs

	case StacksClassified:
		for i := range cs.Exec.Stacks {
			p := &cs.Exec.Stacks[i]
			if proj, ok := ev.Projects[p.Path]; ok {
				p.Project = proj
			}
			if cats, ok := ev.Categories[p.Path]; ok {
				p.Categories = cats
			}
		}
		return cs

	// --- gate-lifecycle facts ---

	case Classified:
		prior := priorTargets(cs.Gate)
		lease := priorLease(cs.Gate)

		// Build the new target set, carrying forward still-live prior grants
		// (gap ② anti-clobber: a re-classify never re-requests a valid grant).
		var targets []Target
		for _, g := range ev.Gates {
			key := g.Class + "|" + g.Target
			if pt, ok := prior[key]; ok && pt.Grant.Open() {
				// Carry forward a still-live grant.
				targets = append(targets, pt)
			} else {
				// Fresh target, or prior target whose grant is terminal/absent:
				// start clean so the request loop re-arms it.
				targets = append(targets, Target{Class: g.Class, Target: g.Target})
			}
		}
		cs.Gate = Pending{Targets: targets, Lease: lease}
		return cs

	case GrantObserved:
		targets := gateTargets(cs.Gate)
		if targets == nil {
			return cs
		}
		lease := priorLease(cs.Gate)
		for i := range targets {
			if targets[i].Class != ev.Class || targets[i].Target != ev.Target {
				continue
			}
			if ev.Name != "" {
				targets[i].GrantName = ev.Name
			}
			targets[i].Grant = ev.State
			if lease.Requester == "" && ev.Requester != "" && ev.State.Open() {
				lease.Requester = ev.Requester
			}
		}
		cs.Gate = withTargetsAndLease(cs.Gate, targets, lease)
		return cs

	case GrantCleared:
		targets := gateTargets(cs.Gate)
		if targets == nil {
			return cs
		}
		for i := range targets {
			if targets[i].Class == ev.Class && targets[i].Target == ev.Target {
				targets[i].Grant = ""
			}
		}
		cs.Gate = withTargets(cs.Gate, targets)
		return cs

	case GateTargetRequested:
		// No state change: the request is an effect; the target already carries
		// GrantName == "" until the backend responds.
		return cs

	case GateSatisfied:
		targets := gateTargets(cs.Gate)
		lease := priorLease(cs.Gate)
		cs.Gate = Satisfied{Targets: targets, Lease: lease}
		return cs

	case GateBlocked:
		targets := gateTargets(cs.Gate)
		lease := priorLease(cs.Gate)
		cs.Gate = Blocked{Targets: targets, Lease: lease, By: Blocker{Reason: ev.Reason, ByPR: ev.ByPR, ByEnv: ev.ByEnv}}
		return cs

	case TargetRevoked:
		// Mark the matching target as revoked (PR-closed semantics).
		// A foreign-PR revoke (Env/PR pointing to a different PR) is a no-op on
		// local target state — the foreign entry is in its own ChangeSet.
		targets := gateTargets(cs.Gate)
		if targets == nil {
			return cs
		}
		for i := range targets {
			if targets[i].Class == ev.Class && targets[i].Target == ev.Target {
				targets[i].Grant = "REVOKED"
			}
		}
		cs.Gate = withTargets(cs.Gate, targets)
		return cs

	case GatePassed:
		cs.Gate = Clean{}
		return cs

	case GateReleased:
		cs.Gate = Clean{}
		return cs

	// --- claim-ledger fact ---

	case ClaimReleased:
		// No domain-state change: claim ledger lives in the shell.
		return cs

	// --- trigger fact ---

	case PRClosedRecorded:
		// No domain-state change: presentation trigger only.
		return cs

	default:
		// Exhaustive switch; new Event variants not yet handled are a no-op.
		return cs
	}
}

// withTargets returns a new GateState with the targets replaced, preserving the
// variant (Pending/Satisfied/Blocked) and its lease.
func withTargets(g GateState, targets []Target) GateState {
	switch v := g.(type) {
	case Pending:
		v.Targets = targets
		return v
	case Satisfied:
		v.Targets = targets
		return v
	case Blocked:
		v.Targets = targets
		return v
	}
	return g
}

// withTargetsAndLease returns a new GateState with both targets and lease
// replaced, preserving the variant.
func withTargetsAndLease(g GateState, targets []Target, lease Lease) GateState {
	switch v := g.(type) {
	case Pending:
		v.Targets = targets
		v.Lease = lease
		return v
	case Satisfied:
		v.Targets = targets
		v.Lease = lease
		return v
	case Blocked:
		v.Targets = targets
		v.Lease = lease
		return v
	}
	return g
}
