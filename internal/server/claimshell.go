package server

import (
	"errors"
	"log"
	"sync"

	"github.com/Fluent-Health/terraform-stack-plan/internal/claims"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// envStreamID is the event-stream id for an environment's apply-lock claim
// ledger. One stream per environment: claims across PRs in the same env serialize
// on this stream (and the per-env lock below), independent of the gate streams.
func envStreamID(env string) string { return "env:" + env }

// envLocks holds the per-env mutexes that serialize handleClaim for a given env.
// It is a separate map from the per-(pr,env) gate locks (shell.go) on purpose:
//
//	Lock-ordering invariant — handleClaim acquires ONLY the per-env lock, never
//	an exec (pr,env) lock. The reconcile.ReleaseClaim action runs from inside the
//	exec Handle (holding the (pr,env) lock) and calls handleClaim (env lock), so
//	the nesting is strictly exec → env, one-directional, and deadlock-free.
type envLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (e *envLocks) lockFor(env string) *sync.Mutex {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.locks == nil {
		e.locks = map[string]*sync.Mutex{}
	}
	m, ok := e.locks[env]
	if !ok {
		m = &sync.Mutex{}
		e.locks[env] = m
	}
	return m
}

// handleClaim applies one claim Command to env's ledger under the per-env lock:
// load (replay) → Decide → fold → append (optimistic) → project. The loop exists
// ONLY to retry on a concurrency conflict (another writer advanced the stream);
// there is no fixpoint feedback — claims emit no RequestGrant-style observations.
func (sh *Shell) handleClaim(env string, cmd claims.Command) error {
	const maxIters = 8
	m := sh.envLocks.lockFor(env)
	m.Lock()
	defer m.Unlock()

	stream := envStreamID(env)
	for i := 0; i < maxIters; i++ {
		state, ver, err := sh.app.claimsDecider.Load(sh.app.eventStore, stream)
		if err != nil {
			return err
		}
		evs := claims.Decide(state, cmd)
		newState := state
		for _, e := range evs {
			newState = claims.Evolve(newState, e)
		}
		err = sh.app.claimsDecider.Append(sh.app.eventStore, stream, ver, evs, newState)
		if errors.Is(err, store.ErrConcurrencyConflict) {
			continue // another writer advanced the stream — reload and retry
		}
		if err != nil {
			return err
		}
		return sh.projectClaims(env, newState)
	}
	log.Printf("claimshell: maxIters=%d reached for env=%s", maxIters, env)
	return nil
}

// loadClaims replays env's claim ledger and returns the folded ClaimSet (the
// source of truth for held). Read-only: takes no lock.
func (sh *Shell) loadClaims(env string) (claims.ClaimSet, error) {
	state, _, err := sh.app.claimsDecider.Load(sh.app.eventStore, envStreamID(env))
	return state, err
}

// projectClaims rewrites the apply_claims projection for env from the folded
// ClaimSet — the cross-env index for the sweep + the live-UI list. The fold is
// the truth; this projection is rebuildable (RebuildClaims).
func (sh *Shell) projectClaims(env string, cs claims.ClaimSet) error {
	rows := make(map[string]store.Claim, len(cs))
	for stack, c := range cs {
		rows[stack] = store.Claim{Environment: env, StackPath: stack, OwnerPR: c.PR, ExpiresAt: c.ExpiresAt}
	}
	return store.ReplaceClaims(sh.app.db, env, rows)
}

// RebuildClaims replays env's claim ledger and rewrites its apply_claims
// projection from the folded state — the regenerate-the-read-model-from-the-log
// seam (used by the sweep to compact).
func (sh *Shell) RebuildClaims(env string) error {
	cs, err := sh.loadClaims(env)
	if err != nil {
		return err
	}
	return sh.projectClaims(env, cs)
}
