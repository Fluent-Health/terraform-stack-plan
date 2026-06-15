package server

import (
	"context"
	"fmt"
	"sync"

	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
)

// Shell is the imperative shell around the pure reconcile core. It gathers a
// scoped World per signal, runs reconcile.Step to a fixpoint, executes the
// resulting Actions, and persists the new ChangeSet — serialized per
// (pr, environment).
type Shell struct {
	app *App // back-reference for store/Approval/gh/checkrun access

	mu    sync.Mutex
	locks map[string]*sync.Mutex
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

// Handle processes one signal for (pr, env) to a fixpoint: gather → Step →
// save → execute → feed results back, repeating while RequestGrant actions
// yield new observations. Serialized per (pr, env). maxIters guards the loop.
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
			world.Prior.PR, world.Prior.Environment = pr, env
			state, actions := reconcile.Step(world, cur)
			if err := sh.save(state); err != nil {
				outerErr = err
				return
			}
			results := sh.execute(ctx, state, repo, actions)
			if len(results) == 0 {
				return
			}
			cur = reconcile.GrantsObserved{Grants: results}
		}
	})
	return outerErr
}
