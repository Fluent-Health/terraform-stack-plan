package reconcile

import (
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
		return stepApplySucceeded(cs)
	case RunnerInit:
		cs.Exec = sig.Exec
		if cs.Gate == nil {
			cs.Gate = NotClassified{}
		}
		return cs, []Action{RenderCheckRun{}, PublishSSE{}}
	case RunnerPhase:
		cs.Exec.Phase = sig.Phase
		return cs, []Action{RenderCheckRun{}, PublishSSE{}}
	case RunnerUpdate:
		for i := range cs.Exec.Stacks {
			if cs.Exec.Stacks[i].Path == sig.Stack {
				cs.Exec.Stacks[i].RunStatus = sig.Status
				cs.Exec.Stacks[i].Detail = sig.Detail
			}
		}
		return cs, []Action{RenderCheckRun{}, PublishSSE{}}
	case RunnerFinalize:
		return stepFinalize(cs, sig)
	case GrantsObserved:
		return stepObserve(cs, sig.Grants)
	case GateTick:
		return stepObserve(cs, sig.Grants)
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
			case events.StatusPending, events.StatusRunning:
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

	if len(f.Gates) == 0 {
		cs.Gate = Clean{}
		return cs, []Action{RenderCheckRun{Terminal: true, Conclusion: "success"}, PublishSSE{}}
	}

	prior := priorTargets(cs.Gate)
	lease := priorLease(cs.Gate)

	// Build the new target set, carrying forward prior grant observations.
	want := map[string]bool{}
	var targets []Target
	for _, g := range f.Gates {
		key := g.Class + "|" + g.Target
		want[key] = true
		if pt, ok := prior[key]; ok {
			targets = append(targets, pt)
		} else {
			targets = append(targets, Target{Class: g.Class, Target: g.Target})
		}
	}

	// Prune (revoke) targets the new plan dropped.
	var actions []Action
	for key, pt := range prior {
		if !want[key] && pt.GrantName != "" {
			actions = append(actions, RevokeGrant{
				Class: pt.Class, Target: pt.Target, PR: cs.PR, Environment: cs.Environment,
			})
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

// stepApplySucceeded revokes the changeset's grants post-apply and marks the
// gate terminally Clean (privilege no longer needed).
func stepApplySucceeded(cs ChangeSet) (ChangeSet, []Action) {
	var actions []Action
	switch g := cs.Gate.(type) {
	case Pending:
		actions = revokeAll(cs, g.Targets)
	case Satisfied:
		actions = revokeAll(cs, g.Targets)
	case Blocked:
		actions = revokeAll(cs, g.Targets)
	default:
		return cs, nil
	}
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

// stepObserve folds grant observations into the gate's targets, then recomputes
// the gate variant: pins the lease from the first leased grant and requests any
// still-ungranted targets (fixpoint), promotes to Satisfied when every target is
// ACTIVE, downgrades a previously-active target whose grant is gone (gap ①),
// and surfaces DENIED/EXPIRED as Blocked (gap ③). Slot collisions are handled
// in Task 8. No-op for non-gated states.
func stepObserve(cs ChangeSet, obs []ObservedGrant) (ChangeSet, []Action) {
	targets := gateTargets(cs.Gate)
	lease := priorLease(cs.Gate)
	if targets == nil {
		return cs, nil
	}
	prevWasActive := isAllActive(targets)

	byKey := map[string]ObservedGrant{}
	for _, o := range obs {
		byKey[o.Class+"|"+o.Target] = o
	}

	var actions []Action
	for i := range targets {
		o, ok := byKey[targets[i].Class+"|"+targets[i].Target]
		if !ok {
			continue
		}
		// A slot collision is resolved in Task 8; ignore here.
		if o.Collision != nil {
			continue
		}
		if o.Name != "" {
			targets[i].GrantName = o.Name
		}
		targets[i].Grant = o.State
		if lease.Requester == "" && o.Requester != "" {
			lease.Requester = o.Requester
		}
	}

	// Denied/Expired/Revoked → Blocked terminal (gap ③).
	if r, blocked := firstTerminalBlock(targets); blocked {
		cs.Gate = Blocked{Targets: targets, Lease: lease, By: Blocker{Reason: r}}
		return cs, append(actions, RenderCheckRun{Terminal: true, Conclusion: "action_required"}, PublishSSE{})
	}

	// Request any target still lacking a grant (pinned to the lease).
	requested := false
	for _, t := range targets {
		if t.GrantName == "" {
			actions = append(actions, RequestGrant{Class: t.Class, Target: t.Target, Requester: lease.Requester})
			requested = true
		}
	}
	if requested {
		cs.Gate = Pending{Targets: targets, Lease: lease}
		return cs, append(actions, RenderCheckRun{}, PublishSSE{})
	}

	if isAllActive(targets) {
		cs.Gate = Satisfied{Targets: targets, Lease: lease}
		return cs, append(actions, RenderCheckRun{Terminal: true, Conclusion: "success"}, PublishSSE{})
	}

	// gap ①: was satisfied, a grant is now gone (not terminal-denied) → downgrade.
	if prevWasActive {
		cs.Gate = Blocked{Targets: targets, Lease: lease, By: Blocker{Reason: ReasonExpired}}
		return cs, append(actions, RenderCheckRun{Terminal: true, Conclusion: "action_required"}, PublishSSE{})
	}

	cs.Gate = Pending{Targets: targets, Lease: lease}
	return cs, append(actions, RenderCheckRun{}, PublishSSE{})
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
