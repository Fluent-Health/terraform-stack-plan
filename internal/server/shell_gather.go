package server

import (
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
)

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

// loadStream reconstructs the ChangeSet for (pr, env): load the latest snapshot,
// then replay any events past the snapshot version through Evolve. Returns the
// folded state and the stream's current version. An empty stream yields a
// NotClassified gate at version 0.
func (sh *Shell) loadStream(pr int, env string) (reconcile.ChangeSet, int, error) {
	streamID := execStreamID(pr, env)
	state := reconcile.ChangeSet{PR: pr, Environment: env, Gate: reconcile.NotClassified{}}

	snapState, snapVer, ok, err := sh.app.eventStore.LoadSnapshot(streamID)
	if err != nil {
		return state, 0, err
	}
	if ok {
		cs, derr := reconcile.UnmarshalSnapshot(snapState)
		if derr != nil {
			return state, 0, derr
		}
		cs.PR, cs.Environment = pr, env
		state = cs
	}

	stored, version, err := sh.app.eventStore.Load(streamID)
	if err != nil {
		return state, 0, err
	}
	for i, se := range stored {
		if i+1 <= snapVer { // events are 1-based; skip those already in the snapshot
			continue
		}
		ev, derr := reconcile.UnmarshalEvent(se.Type, se.Data)
		if derr != nil {
			return state, 0, derr
		}
		state = reconcile.Evolve(state, ev)
	}
	return state, version, nil
}
