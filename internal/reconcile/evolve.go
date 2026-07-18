package reconcile

import (
	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
)

// Evolve applies a single domain Event to a ChangeSet, returning the new state.
// It is total (never panics on any state/event combination), pure (no I/O), and
// deterministic. It is the fold half of the Decider pattern: Decide emits Events,
// Evolve reduces them into state.
func Evolve(cs ChangeSet, e Event) ChangeSet {
	switch ev := e.(type) {

	// --- run-triggering facts ---

	case RunQueued:
		return withRun(cs, Run{
			ExecutionID: ev.ExecutionID, Kind: ev.Kind, SHA: ev.SHA,
			Branch: ev.Branch, Attempt: ev.Attempt, Phase: RunPhaseQueued,
		})

	case RunStarted:
		r, ok := cs.Runs[ev.Kind]
		if !ok || r.ExecutionID != ev.ExecutionID {
			return cs // stale: a supersede raced the start
		}
		r.BuildRef = ev.BuildRef
		r.Phase = RunPhaseStarted
		return withRun(cs, r)

	case RunStartFailed:
		r, ok := cs.Runs[ev.Kind]
		if !ok || r.ExecutionID != ev.ExecutionID {
			return cs
		}
		r.Phase = RunPhaseStartFailed
		return withRun(cs, r)

	case RunCompleted:
		r, ok := cs.Runs[ev.Kind]
		if !ok || r.ExecutionID != ev.ExecutionID {
			return cs
		}
		r.Phase = RunPhaseCompleted
		return withRun(cs, r)

	case RunSuperseded:
		r, ok := cs.Runs[ev.Kind]
		if !ok || r.ExecutionID != ev.OldExecutionID {
			return cs
		}
		// Usually followed in the same batch by the RunQueued that replaces
		// this map entry; folding superseded first keeps replays total.
		r.Phase = RunPhaseSuperseded
		return withRun(cs, r)

	case RunAdopted:
		// Bind a run to an externally-created build: created already-Started (the
		// build exists), replacing any prior (superseded/failed) entry for the kind.
		return withRun(cs, Run{
			ExecutionID: ev.ExecutionID, Kind: ev.Kind, SHA: ev.SHA,
			Branch: ev.Branch, Attempt: ev.Attempt, BuildRef: ev.BuildRef,
			Phase: RunPhaseStarted,
		})

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
				targets[i].Grant = approval.GrantState("")
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
				targets[i].Grant = approval.StateRevoked
			}
		}
		cs.Gate = withTargets(cs.Gate, targets)
		return cs

	case AdminGrantReleased:
		targets := gateTargets(cs.Gate)
		if targets == nil {
			return cs
		}
		for i := range targets {
			if targets[i].Class == ev.Class && targets[i].Target == ev.Target {
				targets[i].Grant = approval.StateRevoked
			}
		}
		cs.Gate = withTargets(cs.Gate, targets)
		return cs

	case AdminExecutionCancelled:
		// Audit event only, status is aborted by accompanying RunCompleted
		return cs

	case AdminGateSatisfied:
		targets := gateTargets(cs.Gate)
		if targets == nil {
			return cs
		}
		for i := range targets {
			if targets[i].Class == ev.Class && targets[i].Target == ev.Target {
				targets[i].Grant = approval.StateActive
			}
		}
		cs.Gate = withTargets(cs.Gate, targets)
		return cs

	case AdminCheckOverridden:
		cs.CheckOverride = &CheckOverride{
			CheckName:  ev.CheckName,
			Conclusion: ev.Conclusion,
			Actor:      ev.Actor,
			Reason:     ev.Reason,
		}
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

// withRun returns cs with the run stored under its kind. The map is cloned —
// Evolve must never mutate shared state (a prior ChangeSet may still be
// referenced by the caller).
func withRun(cs ChangeSet, r Run) ChangeSet {
	runs := make(map[string]Run, len(cs.Runs)+1)
	for k, v := range cs.Runs {
		runs[k] = v
	}
	runs[r.Kind] = r
	cs.Runs = runs
	return cs
}
