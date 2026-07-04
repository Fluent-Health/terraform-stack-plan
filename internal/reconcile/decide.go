package reconcile

import (
	"sort"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// Decide computes the past-tense domain facts that result from applying Signal
// s to the prior state. All business logic lives here; Evolve only folds.
func Decide(state ChangeSet, s Signal) []Event {
	switch sig := s.(type) {
	case RunnerInit:
		return []Event{ExecutionStarted{Exec: sig.Exec}}
	case RunnerPhase:
		return []Event{PhaseChanged{Phase: sig.Phase}}
	case RunnerUpdate:
		return []Event{StackStatusChanged{Stack: sig.Stack, Status: sig.Status, Detail: sig.Detail}}
	case RunnerFinalize:
		return decideFinalize(state, sig)
	case GrantsObserved:
		return decideObserve(state, sig.Grants, false)
	case GateTick:
		return decideObserve(state, sig.Grants, true)
	case RunRequested:
		return decideRunRequested(state, sig)
	case RunStartResult:
		return decideRunStartResult(state, sig)
	case ApplySucceeded:
		if _, ok := state.Gate.(NotClassified); ok {
			return nil
		}
		evs := []Event{ClaimReleased{PR: state.PR, Environment: state.Environment}}
		for _, t := range gateTargets(state.Gate) {
			if t.GrantName == "" {
				continue
			}
			evs = append(evs, TargetRevoked{Class: t.Class, Target: t.Target, PR: state.PR, Env: state.Environment})
		}
		return append(evs, GateReleased{})
	case PRClosed:
		targets := gateTargets(state.Gate)
		var revokes []Event
		for _, t := range targets {
			if t.GrantName == "" {
				continue
			}
			revokes = append(revokes, TargetRevoked{Class: t.Class, Target: t.Target, PR: state.PR, Env: state.Environment})
		}
		if len(revokes) == 0 {
			return nil
		}
		return append(revokes, PRClosedRecorded{}, GateBlocked{Reason: ReasonRevoked})
	default:
		return nil
	}
}

// decideFinalize implements the three RunnerFinalize outcomes:
//   - failed: emit ExecutionFailed (Evolve fails-open stacks).
//   - clean (effective gate empty): emit StacksClassified + GatePassed.
//   - gated: emit StacksClassified + Classified{effective} + TargetRevoked for
//     each pruned dropped target (plan-authoritative only) + GateTargetRequested
//     for the first target lacking a carried-forward live grant, OR GateSatisfied
//     when every target is already ACTIVE (carried-forward all-ACTIVE path).
//
// decideFinalize computes the gate/exec events for a RunnerFinalize signal.
func decideFinalize(state ChangeSet, f RunnerFinalize) []Event {
	if f.Failed {
		return []Event{ExecutionFailed{}}
	}

	evs := []Event{StacksClassified{Projects: f.Projects, Categories: f.Categories}}

	prior := priorTargets(state.Gate)

	// Effective gate set: plan-authoritative replaces; apply-context unions.
	gates := f.Gates
	if f.ApplyContext && len(prior) > 0 {
		gates = unionPriorTargets(f.Gates, prior)
	}

	if len(gates) == 0 {
		return append(evs, GatePassed{})
	}
	evs = append(evs, Classified{Gates: gates})

	// Prune (revoke) dropped targets — authoritative plan finalize only.
	// Apply-context finalizes never prune (step.go:108-122).
	if !f.ApplyContext {
		want := make(map[string]bool, len(gates))
		for _, g := range gates {
			want[g.Class+"|"+g.Target] = true
		}
		for _, key := range sortedKeys(prior) {
			pt := prior[key]
			if !want[key] && pt.GrantName != "" {
				evs = append(evs, TargetRevoked{
					Class: pt.Class, Target: pt.Target,
					PR: state.PR, Env: state.Environment,
				})
			}
		}
	}

	// Request the first target with no carried-forward grant.
	// Mirror Evolve(Classified)'s carry-forward: if prior[key].Grant.Open()
	// the target will be carried forward (GrantName retained); otherwise it
	// starts fresh and must be requested (step.go:127-131, 88-101).
	lease := priorLease(state.Gate)
	anyRequested := false
	for _, g := range gates {
		key := g.Class + "|" + g.Target
		if pt, ok := prior[key]; ok && pt.Grant.Open() {
			continue // carried forward — no request needed
		}
		evs = append(evs, GateTargetRequested{
			Class:     g.Class,
			Target:    g.Target,
			Requester: lease.Requester,
		})
		anyRequested = true
		break
	}

	// Carried-forward all-ACTIVE path: if every target was carried forward with
	// an ACTIVE grant, the gate is already satisfied. Emit GateSatisfied so the
	// check run closes immediately. Without this, PendingGates excludes
	// fully-ACTIVE gates and the ReconcileLoop never heals the stuck in_progress
	// check run when a superseding build re-finalizes a plan whose grant is still
	// live from a prior build of the same PR/env.
	if !anyRequested {
		allActive := true
		for _, g := range gates {
			key := g.Class + "|" + g.Target
			if pt, ok := prior[key]; !ok || pt.Grant != approval.StateActive {
				allActive = false
				break
			}
		}
		if allActive {
			evs = append(evs, GateSatisfied{})
		}
	}

	return evs
}

// sortedKeys returns the keys of m in sorted order (deterministic output).
func sortedKeys(m map[string]Target) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// decideObserve folds grant observations into facts, then classifies the gate.
// It mirrors stepObserve (step.go) exactly but emits past-tense Events instead of
// mutating state + returning actions: a GrantObserved per matched target (plus a
// GrantCleared per dropped target on a full re-list), then exactly one outcome
// fact — GateBlocked (terminal denial/revoke or gap① downgrade), one
// GateTargetRequested per re-armable target, GateSatisfied, or NONE (the settled
// awaiting-approval fallthrough, which React renders from the post-fold Pending
// state). Slot collisions short-circuit via decideCollision. No-op (nil) for
// non-gated states.
//
// fullRelist (GateTick) treats the observation set as authoritative: a target the
// backend no longer reports has lost its grant (GrantCleared → gap①). Partial
// feedback (GrantsObserved) leaves unmentioned targets untouched.
func decideObserve(state ChangeSet, obs []ObservedGrant, fullRelist bool) []Event {
	targets := gateTargets(state.Gate)
	if targets == nil {
		return nil // non-gated state: no-op, no render (step.go:249-251).
	}
	lease := priorLease(state.Gate)
	prevWasActive := isAllActive(targets) // captured BEFORE folding (gap① guard).

	// Resolve any slot collision first; it determines the whole gate outcome.
	for _, o := range obs {
		if o.Collision == nil {
			continue
		}
		return decideCollision(state, lease, o)
	}

	// Dedup multiple observations of the same (class,target) down to the best one
	// (foldBetter), so a stale terminal grant from an earlier retry can't clobber a
	// live one and wedge the gate via firstTerminalBlock.
	byKey := map[string]ObservedGrant{}
	for _, o := range obs {
		k := o.Class + "|" + o.Target
		if cur, ok := byKey[k]; !ok || foldBetter(o, cur, lease) {
			byKey[k] = o
		}
	}

	// Emit the fold facts AND build a local post-fold target view to classify on.
	// (The local fold mirrors Evolve(GrantObserved/GrantCleared) + the lease pin.)
	var evs []Event
	local := make([]Target, len(targets))
	copy(local, targets)
	for i := range local {
		o, ok := byKey[local[i].Class+"|"+local[i].Target]
		if !ok {
			if fullRelist {
				// A target the backend no longer reports has lost its grant — clear
				// it so a previously-active gate downgrades (gap①).
				evs = append(evs, GrantCleared{Class: local[i].Class, Target: local[i].Target})
				local[i].Grant = approval.GrantState("")
			}
			continue
		}
		evs = append(evs, GrantObserved{
			Class:     o.Class,
			Target:    o.Target,
			Name:      o.Name,
			State:     o.State,
			Requester: o.Requester,
		})
		if o.Name != "" {
			local[i].GrantName = o.Name
		}
		local[i].Grant = o.State
		if lease.Requester == "" && o.Requester != "" && o.State.Open() {
			lease.Requester = o.Requester
		}
	}

	// Denied/Revoked → Blocked terminal (gap③). EXPIRED is intentionally NOT
	// terminal here: a never-approved lapse is re-armed below; a was-active expiry
	// downgrades via prevWasActive.
	if r, blocked := firstTerminalBlock(local); blocked {
		return append(evs, GateBlocked{Reason: r})
	}

	// Re-arm EVERY target lacking an approved grant (pinned to the lease): no grant
	// yet (GrantName ""), OR a never-active lapse (EXPIRED && !prevWasActive). A
	// was-active expiry is NOT re-armed — it downgrades to Blocked{expired} below.
	requested := false
	for _, t := range local {
		if t.GrantName == "" || (t.Grant == approval.StateExpired && !prevWasActive) {
			evs = append(evs, GateTargetRequested{Class: t.Class, Target: t.Target, Requester: lease.Requester})
			requested = true
		}
	}
	if requested {
		return evs
	}

	if isAllActive(local) {
		return append(evs, GateSatisfied{})
	}

	// gap①: was satisfied, a grant is now gone (not terminal-denied) → downgrade.
	if prevWasActive {
		return append(evs, GateBlocked{Reason: ReasonExpired})
	}

	// Awaiting-approval fallthrough: targets exist, all have grants, none ACTIVE,
	// none re-armable, not was-active → the gate STAYS Pending but renders TERMINAL
	// action_required (step.go:346-347). We emit NO outcome fact (emitting
	// GateBlocked would wrongly fold to Blocked); React projects the terminal
	// action_required from the post-fold Pending state with no outcome event in the
	// batch. The GrantObserved facts still fold the observation into state.
	return evs
}

// decideCollision implements the slot-collision policy as facts (relocated from
// resolveCollision, step.go):
//   - BySelf (same PR, another env): Blocked{slot_self}, never self-revoke (gap⑥).
//   - foreign open: Blocked{slot_foreign}, wait.
//   - foreign abandoned (closed && !merged): revoke the foreign blocker, retry our
//     request, stay Pending (Bug #2). Order: TargetRevoked → GateTargetRequested.
func decideCollision(state ChangeSet, lease Lease, o ObservedGrant) []Event {
	c := o.Collision
	if c.BySelf {
		return []Event{GateBlocked{Reason: ReasonSlotSelf, ByPR: c.ByPR, ByEnv: c.ByEnv}}
	}
	if !c.ByPRAbandoned {
		return []Event{GateBlocked{Reason: ReasonSlotForeign, ByPR: c.ByPR, ByEnv: c.ByEnv}}
	}
	// Abandoned foreign blocker: revoke it (its own PR/env), retry our request.
	return []Event{
		TargetRevoked{Class: o.Class, Target: o.Target, PR: c.ByPR, Env: c.ByEnv},
		GateTargetRequested{Class: o.Class, Target: o.Target, Requester: lease.Requester},
	}
}

// grantStateRank orders grant states for folding multiple observations of the
// same target down to one: ACTIVE wins, then open-pending states, then any
// terminal state, then absent. This makes a live grant beat the stale terminal
// grants a full re-list also returns for the same (PR, target).
func grantStateRank(s approval.GrantState) int {
	switch s {
	case approval.StateActive:
		return 4
	case approval.StateActivating:
		return 3
	case approval.StateAwaiting:
		return 2
	case "":
		return 0
	default: // terminal: DENIED / REVOKED / EXPIRED
		return 1
	}
}

// foldBetter reports whether observation a should win over b when both describe
// the same (class,target) in a re-list. Higher grant-state rank wins; on a tie
// the grant matching the pinned lease wins (requester continuity), then the
// lexicographically greater Name wins. The Name tiebreak makes the fold a total,
// backend-order-independent order so the chosen grant (and the requester it pins)
// is deterministic regardless of PAM's unspecified re-list order. Name is the
// backend-assigned grant id (unique per (class,target,PR,env)), so equal Names
// don't arise in practice; if they ever did, the fold would degrade to slice order.
func foldBetter(a, b ObservedGrant, lease Lease) bool {
	if ra, rb := grantStateRank(a.State), grantStateRank(b.State); ra != rb {
		return ra > rb
	}
	aMatch := lease.Requester != "" && a.Requester == lease.Requester
	bMatch := lease.Requester != "" && b.Requester == lease.Requester
	if aMatch != bMatch {
		return aMatch
	}
	return a.Name > b.Name
}

// isAllActive reports whether every target has an ACTIVE grant.
func isAllActive(targets []Target) bool {
	if len(targets) == 0 {
		return false
	}
	for _, t := range targets {
		if t.Grant != approval.StateActive {
			return false
		}
	}
	return true
}

// firstTerminalBlock returns the block reason for the first target observed in a
// terminal-denied state, if any.
func firstTerminalBlock(targets []Target) (BlockReason, bool) {
	for _, t := range targets {
		switch t.Grant {
		case approval.StateDenied:
			return ReasonDenied, true
		case approval.StateRevoked:
			return ReasonRevoked, true
		}
	}
	return "", false
}

// gateTargets returns the targets carried by any stateful gate variant.
func gateTargets(g GateState) []Target {
	switch v := g.(type) {
	case Pending:
		return v.Targets
	case Satisfied:
		return v.Targets
	case Blocked:
		return v.Targets
	}
	return nil
}

// priorTargets indexes a gate's targets by "class|target".
func priorTargets(g GateState) map[string]Target {
	out := map[string]Target{}
	for _, t := range gateTargets(g) {
		out[t.Class+"|"+t.Target] = t
	}
	return out
}

// priorLease returns the lease carried by any stateful gate variant.
func priorLease(g GateState) Lease {
	switch v := g.(type) {
	case Pending:
		return v.Lease
	case Satisfied:
		return v.Lease
	case Blocked:
		return v.Lease
	}
	return Lease{}
}

// Requester returns the leased requester identity for the gate ("" if none).
func Requester(g GateState) string { return priorLease(g).Requester }

// unionPriorTargets returns the finalize gates plus any prior target not already
// named, so an apply-context finalize can only ADD to (never remove from) the
// plan-established gate. Prior-only targets append in sorted key order so the
// result is deterministic.
func unionPriorTargets(gates []events.GateTarget, prior map[string]Target) []events.GateTarget {
	seen := make(map[string]bool, len(gates))
	for _, g := range gates {
		seen[g.Class+"|"+g.Target] = true
	}
	out := append([]events.GateTarget(nil), gates...)
	for _, key := range sortedKeys(prior) {
		if !seen[key] {
			pt := prior[key]
			out = append(out, events.GateTarget{Class: pt.Class, Target: pt.Target})
		}
	}
	return out
}
