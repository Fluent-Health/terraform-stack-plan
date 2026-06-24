package server

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// Shell is the imperative shell around the pure reconcile core. It gathers a
// World by replaying the event stream, runs Decide to produce events, folds
// them via Evolve, persists (appends events + snapshot + projects), and executes
// the React actions — serialized per (pr, environment).
type Shell struct {
	app *App // back-reference for store/Approval/gh/checkrun access

	mu    sync.Mutex
	locks map[string]*sync.Mutex

	// envLocks serializes claim-ledger writes per environment (handleClaim). Kept
	// separate from the per-(pr,env) gate locks above so the exec→env nesting
	// (ApplySucceeded → ReleaseClaim) is strictly one-directional and deadlock-free.
	envLocks envLocks
}

// NewShell builds a Shell bound to an App.
func NewShell(app *App) *Shell {
	return &Shell{app: app, locks: map[string]*sync.Mutex{}}
}

func changeSetKey(pr int, env string) string { return fmt.Sprintf("%d|%s", pr, env) }

// lockFor returns the per-ChangeSet mutex, creating it on first use.
func (sh *Shell) lockFor(pr int, env string) *sync.Mutex {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	k := changeSetKey(pr, env)
	m, ok := sh.locks[k]
	if !ok {
		m = &sync.Mutex{}
		sh.locks[k] = m
	}
	return m
}

// withLock runs fn while holding the (pr, env) lock.
func (sh *Shell) withLock(_ context.Context, pr int, env string, fn func()) {
	m := sh.lockFor(pr, env)
	m.Lock()
	defer m.Unlock()
	fn()
}

// Handle processes one signal for (pr, env) to a fixpoint: gather (replay) →
// Decide → persist (append+snapshot+project) → React → execute, repeating while
// RequestGrant actions yield new observations. Serialized per (pr, env). maxIters
// guards the loop.
func (sh *Shell) Handle(ctx context.Context, pr int, env, repo string, sig reconcile.Signal) error {
	const maxIters = 16
	var outerErr error
	sh.withLock(ctx, pr, env, func() {
		cur := sig
		for i := 0; i < maxIters; i++ {
			world, err := sh.gather(pr, env)
			if err != nil {
				outerErr = err
				return
			}
			// Defensive: gather already sets these from the args; keep them set
			// even if a future gather path leaves them zero.
			world.Prior.PR, world.Prior.Environment = pr, env
			evs := reconcile.Decide(world.Prior, cur)
			state := world.Prior
			for _, e := range evs {
				state = reconcile.Evolve(state, e)
			}
			if err := sh.persist(pr, env, world.Version, evs, state); err != nil {
				outerErr = err
				return
			}
			actions := sh.execute(ctx, state, repo, reconcile.React(state, evs))
			if len(actions) == 0 {
				return
			}
			cur = reconcile.GrantsObserved{Grants: actions}
		}
		// Reached the iteration ceiling with work still pending — should not
		// happen in normal flows (a finalize converges in ≤3 iterations). Log so
		// a silent partial state is visible.
		log.Printf("shell: maxIters=%d reached for pr=%d env=%s", maxIters, pr, env)
	})
	return outerErr
}

// tick builds a full grant re-list for the changeset's stored targets and runs
// it as a GateTick (which folds states, promotes to Satisfied, downgrades
// vanished grants, and drives the check run). Used by the reconcile-loop and
// the apply-time gate pre-check.
func (sh *Shell) tick(ctx context.Context, pr int, env string) error {
	targets, err := store.TargetsFor(sh.app.db, pr, env)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}
	var obs []reconcile.ObservedGrant
	if sh.app.Approval != nil {
		for _, t := range targets {
			grants, lerr := sh.app.Approval.ListGrants(ctx, t.Class, t.Target)
			if lerr != nil {
				// Abort the whole tick on a backend error rather than omitting the
				// target: an omitted target reads as a vanished grant (full re-list)
				// and would spuriously downgrade a Satisfied gate. A genuinely-gone
				// grant returns an empty list (no error), so the legitimate
				// downgrade path is unaffected. The next tick retries.
				return fmt.Errorf("tick: list grants %s/%s pr=%d env=%s: %w", t.Class, t.Target, pr, env, lerr)
			}
			for _, g := range grants {
				if g.Request.PR == pr && g.Request.Environment == env {
					obs = append(obs, reconcile.ObservedGrant{
						Class: t.Class, Target: t.Target, Name: g.Name, State: g.State, Requester: g.Requester,
					})
				}
			}
		}
	}
	repo := ""
	if id, ok := store.LatestExecutionID(sh.app.db, pr, env); ok {
		if e, gerr := store.GetExecution(sh.app.db, id); gerr == nil {
			repo = e.Repo
		}
	}
	return sh.Handle(ctx, pr, env, repo, reconcile.GateTick{Grants: obs})
}
