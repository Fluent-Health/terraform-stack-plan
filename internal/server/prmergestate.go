package server

import (
	"strconv"

	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// PRMergeState is a read-only snapshot of a PR's merge-readiness on this
// serve's tier: the terraform/<env> required-check conclusion (GitHub's
// success/failure/action_required/"" vocabulary — "" means still in
// progress) plus the apply-lock verdict. Task 4 exposes this via
// GET /api/pr/{n}.
type PRMergeState struct {
	Environment     string
	RequiredCheck   string
	CheckConclusion string
	MergeBlocked    bool
	Blocker         string
}

// prMergeState reports pr's merge state on this serve's tier (a.cfg.Environment):
// the terraform/<env> check conclusion, derived from the latest gate execution
// the runner actually reported on, plus whether the apply-lock verdict blocks
// merge and a short human blocker string. Read-only: no state writes, no
// GitHub calls. Builds on the same helpers the check-run render path uses
// (evalApplyLock, loadSnapshot, conclusion, lockBlocked, lockTitle in
// applylock.go/status.go) rather than re-deriving verdict logic.
func (a *App) prMergeState(pr int) PRMergeState {
	env := a.cfg.Environment
	state := PRMergeState{Environment: env, RequiredCheck: "terraform/" + env}

	// LatestReportedExecutionID (not LatestExecutionID): a serve-queued row
	// with no runner data yet must not read as "PR touches nothing" — same
	// fail-closed reasoning as prChangedStacks in applylock.go.
	id, ok := store.LatestReportedExecutionID(a.db, pr, env)
	if !ok {
		lock := applyLockVerdict{State: "unverifiable", Reason: "no successful plan for #" + strconv.Itoa(pr)}
		state.MergeBlocked = lockBlocked(lock)
		state.Blocker = lockTitle(lock)
		return state
	}

	var stacks []string
	if g, err := store.LoadGraph(a.db, id); err == nil {
		stacks = make([]string, 0, len(g.Stacks))
		for _, s := range g.Stacks {
			stacks = append(stacks, s.Path)
		}
	}

	lock := a.evalApplyLock(env, pr, stacks, a.now())
	state.MergeBlocked = lockBlocked(lock)
	if state.MergeBlocked {
		state.Blocker = lockTitle(lock)
	}

	if snap, _, ok := loadSnapshot(a.db, id); ok {
		state.CheckConclusion = conclusion(snap, lock)
	}
	return state
}
