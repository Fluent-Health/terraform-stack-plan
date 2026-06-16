package server

import (
	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// gather loads the scoped World for (pr, env). For now the World carries only
// the prior ChangeSet's gate; signal-specific external observations (ListGrants,
// PRClosed) are attached by the caller before Step (later tasks). Execution
// stacks are loaded by the runner-event path when needed.
func (sh *Shell) gather(pr int, env string) (reconcile.World, error) {
	raw, err := store.LoadChangeSet(sh.app.db, pr, env)
	if err != nil {
		return reconcile.World{}, err
	}
	return reconcile.World{Prior: mapRawGate(raw)}, nil
}

// mapRawGate maps the persisted RawChangeSet into a reconcile.ChangeSet,
// reconstructing the GateState sum type from the flat rows.
func mapRawGate(raw store.RawChangeSet) reconcile.ChangeSet {
	cs := reconcile.ChangeSet{PR: raw.PR, Environment: raw.Environment}
	if !raw.Classified {
		cs.Gate = reconcile.NotClassified{}
		return cs
	}
	if len(raw.Targets) == 0 {
		cs.Gate = reconcile.Clean{}
		return cs
	}
	var targets []reconcile.Target
	lease := reconcile.Lease{}
	allActive := true
	anyTerminal := false
	for _, t := range raw.Targets {
		gs := approval.GrantState(t.State)
		targets = append(targets, reconcile.Target{Class: t.Class, Target: t.Target, GrantName: t.GrantName, Grant: gs})
		if t.Requester != "" && lease.Requester == "" {
			lease.Requester = t.Requester
		}
		if gs != approval.StateActive {
			allActive = false
		}
		// EXPIRED is terminal for reconstruction too, so a reloaded expired gate
		// shows Blocked rather than Pending. ReasonDenied is the reload default for
		// every terminal state (Denied/Revoked/Expired); the precise terminal cause
		// is refined on the next GateTick, and no caller branches on it before then.
		if gs == approval.StateDenied || gs == approval.StateRevoked || gs == approval.StateExpired {
			anyTerminal = true
		}
	}
	switch {
	case anyTerminal:
		cs.Gate = reconcile.Blocked{Targets: targets, Lease: lease, By: reconcile.Blocker{Reason: reconcile.ReasonDenied}}
	case allActive:
		cs.Gate = reconcile.Satisfied{Targets: targets, Lease: lease}
	default:
		cs.Gate = reconcile.Pending{Targets: targets, Lease: lease}
	}
	return cs
}
