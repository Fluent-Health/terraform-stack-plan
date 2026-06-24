package server

import "github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"

// gather loads the scoped World for (pr, env) by replaying its event stream:
// the latest snapshot folded forward over any newer events. mapRawGate is gone —
// the GateState sum type is reconstructed losslessly by Evolve.
func (sh *Shell) gather(pr int, env string) (reconcile.World, error) {
	prior, version, err := sh.loadStream(pr, env)
	if err != nil {
		return reconcile.World{}, err
	}
	prior.PR, prior.Environment = pr, env
	return reconcile.World{Prior: prior, Version: version}, nil
}

// loadGate returns the current folded gate state for (pr, env) by replaying the
// event stream — the lossless source of truth, used by read-only callers like the
// apply gate-check. No lock needed (replay is a read).
func (sh *Shell) loadGate(pr int, env string) (reconcile.GateState, error) {
	state, _, err := sh.loadStream(pr, env)
	if err != nil {
		return nil, err
	}
	return state.Gate, nil
}

// loadStream reconstructs the ChangeSet for (pr, env) via the generic host:
// latest snapshot folded forward over any newer events via Evolve. Returns the
// folded state and the stream's current version. An empty stream yields a
// NotClassified gate at version 0.
func (sh *Shell) loadStream(pr int, env string) (reconcile.ChangeSet, int, error) {
	state, version, err := sh.app.gateDecider.Load(sh.app.eventStore, execStreamID(pr, env))
	if err != nil {
		return reconcile.ChangeSet{}, 0, err
	}
	state.PR, state.Environment = pr, env
	return state, version, nil
}
