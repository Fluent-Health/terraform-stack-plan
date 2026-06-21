package server

import (
	"sort"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// applyLockVerdict is the evaluation of one PR's mergeability against the env's
// claimed-stack set. State is clear|held|unverifiable.
type applyLockVerdict struct {
	State    string
	Blocking []string
	Reason   string
}

// overlap returns the stacks in `stacks` that are claimed by a PR other than
// ownerPR (sorted, deterministic).
func overlap(claimed map[string]int, stacks []string, ownerPR int) []string {
	var out []string
	for _, s := range stacks {
		if pr, ok := claimed[s]; ok && pr != ownerPR {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// prChangedStacks returns a PR's plan-time changed-stack set for env. ok=false
// when there is no successful plan to read (caller fails closed).
func (a *App) prChangedStacks(env string, pr int) ([]string, bool) {
	id, ok := store.LatestExecutionID(a.db, pr, env)
	if !ok {
		return nil, false
	}
	g, err := store.LoadGraph(a.db, id)
	if err != nil {
		return nil, false
	}
	stacks := make([]string, 0, len(g.Stacks))
	for _, s := range g.Stacks {
		stacks = append(stacks, s.Path)
	}
	return stacks, true
}

// evalApplyLock computes the verdict for ownerPR's `stacks` in env at `now`.
// Empty stacks ⇒ clear (PR touches nothing in this env).
func (a *App) evalApplyLock(env string, pr int, stacks []string, now time.Time) applyLockVerdict {
	claimed, err := store.ClaimedStacks(a.db, env, now)
	if err != nil {
		return applyLockVerdict{State: "unverifiable", Reason: "claimed-set query failed"}
	}
	blocking := overlap(claimed, stacks, pr)
	if len(blocking) == 0 {
		return applyLockVerdict{State: "clear"}
	}
	return applyLockVerdict{State: "held", Blocking: blocking,
		Reason: "stacks being applied by another PR"}
}
