package reconcile

import (
	"sort"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// Step is the one pure front door. Given the observed World and an incoming
// Signal it returns the new ChangeSet and the minimal Actions the shell must
// execute. Deterministic; no I/O; safe to call repeatedly.
func Step(w World, s Signal) (ChangeSet, []Action) {
	cs := w.Prior
	switch sig := s.(type) {
	case ApplySucceeded:
		return runDecider(cs, sig)
	case RunnerInit:
		return runDecider(cs, sig)
	case RunnerPhase:
		return runDecider(cs, sig)
	case RunnerUpdate:
		return runDecider(cs, sig)
	case RunnerFinalize:
		return runDecider(cs, sig)
	case GrantsObserved:
		return runDecider(cs, sig)
	case GateTick:
		return runDecider(cs, sig)
	case PRClosed:
		return runDecider(cs, sig)
	default:
		_ = sig
		return cs, nil
	}
}

// stepFinalize records the terminal plan state. Failed → fail open stacks and
// render failure. Clean (no gates) → Clean. With gates → carry forward any
// still-relevant prior target observations, prune dropped targets (gap ②,
// revoking each), and request the first grant unpinned so the lease can be
// established (subsequent gates are requested on the GrantsObserved feedback).
func stepFinalize(cs ChangeSet, f RunnerFinalize) (ChangeSet, []Action) {
	if f.Failed {
		for i := range cs.Exec.Stacks {
			switch cs.Exec.Stacks[i].RunStatus {
			case events.StatusPending, events.StatusRunning,
				events.StatusInitializing, events.StatusInitialized:
				cs.Exec.Stacks[i].RunStatus = events.StatusFailed
			}
		}
		return cs, []Action{RenderCheckRun{Terminal: true, Conclusion: "failure"}, PublishSSE{}}
	}

	// Backfill stack grouping/categories from the finalize payload.
	for i := range cs.Exec.Stacks {
		p := &cs.Exec.Stacks[i]
		if proj, ok := f.Projects[p.Path]; ok {
			p.Project = proj
		}
		if cats, ok := f.Categories[p.Path]; ok {
			p.Categories = cats
		}
	}

	prior := priorTargets(cs.Gate)
	lease := priorLease(cs.Gate)

	// The effective gate set. A plan finalize is authoritative — its set replaces
	// the prior gate (pruning dropped targets below). An apply finalize is a
	// recovery signal, NOT an authority: an under-reporting apply-time re-classify
	// must never weaken a gate the plan established (issue #103 fail-open), so
	// union the prior targets in and skip the prune. (A genuinely wiped serve has
	// no prior targets, so this is a no-op there — recovery still works.)
	gates := f.Gates
	if f.ApplyContext && len(prior) > 0 {
		gates = unionPriorTargets(f.Gates, prior)
	}

	if len(gates) == 0 {
		cs.Gate = Clean{}
		return cs, []Action{RenderCheckRun{Terminal: true, Conclusion: "success"}, PublishSSE{}}
	}

	// Build the new target set, carrying forward prior grant observations.
	want := map[string]bool{}
	var targets []Target
	for _, g := range gates {
		key := g.Class + "|" + g.Target
		want[key] = true
		if pt, ok := prior[key]; ok && pt.Grant.Open() {
			// Carry forward a still-live grant so a re-plan never re-requests a
			// valid grant (gap② anti-clobber intent).
			targets = append(targets, pt)
		} else {
			// A fresh target, or a prior target whose grant is terminal/absent
			// (DENIED/REVOKED/EXPIRED): start it clean so the request loop below
			// re-arms it. A new plan is a new request cycle — a standing
			// denial/revoke/expiry must not wedge the target with no new request.
			targets = append(targets, Target{Class: g.Class, Target: g.Target})
		}
	}

	// Prune (revoke) targets the new plan dropped — sorted for deterministic
	// output. Authoritative plan finalize only; an apply finalize never prunes
	// (its set already unions the prior targets, so nothing is "dropped").
	var actions []Action
	if !f.ApplyContext {
		prunedKeys := make([]string, 0, len(prior))
		for key := range prior {
			prunedKeys = append(prunedKeys, key)
		}
		sort.Strings(prunedKeys)
		for _, key := range prunedKeys {
			pt := prior[key]
			if !want[key] && pt.GrantName != "" {
				actions = append(actions, RevokeGrant{
					Class: pt.Class, Target: pt.Target, PR: cs.PR, Environment: cs.Environment,
				})
			}
		}
	}

	cs.Gate = Pending{Targets: targets, Lease: lease}

	// Request the first target that has no grant yet, unpinned if no lease.
	for _, t := range targets {
		if t.GrantName == "" {
			actions = append(actions, RequestGrant{Class: t.Class, Target: t.Target, Requester: lease.Requester})
			break
		}
	}
	actions = append(actions, RenderCheckRun{}, PublishSSE{})
	return cs, actions
}

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
	priorKeys := make([]string, 0, len(prior))
	for key := range prior {
		priorKeys = append(priorKeys, key)
	}
	sort.Strings(priorKeys)
	for _, key := range priorKeys {
		if !seen[key] {
			pt := prior[key]
			out = append(out, events.GateTarget{Class: pt.Class, Target: pt.Target})
		}
	}
	return out
}

// priorTargets indexes a gate's targets by "class|target".
func priorTargets(g GateState) map[string]Target {
	out := map[string]Target{}
	for _, t := range gateTargets(g) {
		out[t.Class+"|"+t.Target] = t
	}
	return out
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

// stepApplySucceeded releases everything the finished apply held — the
// merge-lock stack claim and the changeset's grants — and marks the gate
// terminally Clean (privilege no longer needed).
func stepApplySucceeded(cs ChangeSet) (ChangeSet, []Action) {
	// NotClassified means the PR never planned/merged, so no apply ran and no
	// claim/grant was ever acquired — nothing to release.
	if _, ok := cs.Gate.(NotClassified); ok {
		return cs, nil
	}
	// The apply for this (pr,env) is done: release the merge-lock claim it held.
	// This is the level-once "apply finished" transition — driven by the
	// runner's apply-end GateRevoke, which (unlike Finalize) is never sent during
	// the mid-run classify pass, so the claim is held until the apply truly ends.
	actions := []Action{ReleaseClaim{PR: cs.PR, Environment: cs.Environment}}
	// Plus the grants the gate held (gated apply only).
	switch g := cs.Gate.(type) {
	case Pending:
		actions = append(actions, revokeAll(cs, g.Targets)...)
	case Satisfied:
		actions = append(actions, revokeAll(cs, g.Targets)...)
	case Blocked:
		actions = append(actions, revokeAll(cs, g.Targets)...)
	}
	// No RenderCheckRun/PublishSSE here: post-apply the runner drives its own
	// apply check run; this transition only releases the claim + grants.
	cs.Gate = Clean{}
	return cs, actions
}

// revokeAll emits a RevokeGrant for every target that still has a grant.
func revokeAll(cs ChangeSet, targets []Target) []Action {
	var actions []Action
	for _, t := range targets {
		if t.GrantName == "" {
			continue
		}
		actions = append(actions, RevokeGrant{
			Class: t.Class, Target: t.Target, PR: cs.PR, Environment: cs.Environment,
		})
	}
	return actions
}

// stepPRClosed revokes every open grant for the closed PR and moves the gate to
// a terminal Blocked{revoked} so the persisted state matches the backend (gap
// ④) and no later apply check can read it as satisfied (Bug #1).
func stepPRClosed(cs ChangeSet) (ChangeSet, []Action) {
	targets := gateTargets(cs.Gate)
	if targets == nil {
		return cs, nil
	}
	actions := revokeAll(cs, targets)
	if len(actions) == 0 {
		return cs, nil
	}
	for i := range targets {
		targets[i].Grant = approval.StateRevoked
	}
	cs.Gate = Blocked{Targets: targets, Lease: priorLease(cs.Gate), By: Blocker{Reason: ReasonRevoked}}
	actions = append(actions, PublishSSE{})
	return cs, actions
}
