package server

import (
	"context"
	"sort"
	"strconv"
	"strings"
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
// Empty stacks => clear (PR touches nothing in this env).
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

// postApplyLock creates-or-updates the apply-lock/<env> check on `sha` to match
// the verdict and persists the record (so a later release can re-PATCH it).
// held/unverifiable => left in_progress (empty conclusion) — blocks the merge;
// clear => conclusion success.
func (a *App) postApplyLock(ctx context.Context, repo, env, sha string, pr int, stacks []string, kind string, v applyLockVerdict) error {
	rec, ok, err := store.GetApplyLockCheck(a.db, env, sha)
	if err != nil {
		return err
	}
	var crID int64
	if ok {
		crID = rec.CheckRunID
	} else {
		crID, err = a.gh.CreateCheckRun(ctx, repo, sha, applyLockName(env), a.applyLockDetailsURL(env, pr))
		if err != nil {
			return err
		}
	}
	title, summary, conclusion := applyLockOutput(env, pr, v)
	if err := a.gh.UpdateCheckRun(ctx, repo, crID, CheckRunUpdate{
		Title: title, Summary: summary, DetailsURL: a.applyLockDetailsURL(env, pr), Conclusion: conclusion,
	}); err != nil {
		return err
	}
	return store.UpsertApplyLockCheck(a.db, store.ApplyLockCheck{
		Environment: env, HeadSHA: sha, CheckRunID: crID, PR: pr, Repo: repo, Stacks: stacks, State: v.State, Kind: kind,
	})
}

// applyLockOutput renders the check title/summary + the GitHub conclusion for a
// verdict. Help text uses the cause + next-steps style.
func applyLockOutput(env string, pr int, v applyLockVerdict) (title, summary, conclusion string) {
	switch v.State {
	case "clear":
		return "apply-lock: clear", "No overlapping apply for this environment.", "success"
	case "unverifiable":
		return "apply-lock: can't verify",
			"Can't verify apply-lock — " + v.Reason + ". Retrying; re-run the plan if it failed.", ""
	default: // held
		return "apply-lock: holding merge",
			"Holding — stacks `" + strings.Join(v.Blocking, "`, `") +
				"` are being applied by another PR. Clears automatically when that apply finishes " +
				"(or when its lease expires). Next: wait; if that apply is stuck, cancel/re-run it, " +
				"an admin may bypass, or run `tfstackplan claims release`.", ""
	}
}

func (a *App) applyLockDetailsURL(env string, pr int) string {
	if a.cfg.PublicBaseURL == "" {
		return ""
	}
	return a.cfg.PublicBaseURL + "/pr/" + strconv.Itoa(pr)
}

// handleMergeGroup evaluates the apply-lock for a GitHub merge group and posts
// apply-lock/<env> on the group head. On checks_requested: disjoint => claim at
// greenlight + success; overlap => held. On destroyed: release the tentative claim.
func (a *App) handleMergeGroup(ctx context.Context, repo, headSHA, action string) {
	if !a.cfg.ApplyLock {
		return
	}
	if action == "destroyed" {
		// Release per-env claims held by the merge group's constituent PRs.
		prs, err := a.gh.MergeGroupPRs(ctx, repo, headSHA)
		if err == nil {
			for _, pr := range prs {
				envs, _ := store.EnvironmentsForPR(a.db, pr)
				for _, env := range envs {
					if rec, ok, _ := store.GetApplyLockCheck(a.db, env, headSHA); ok {
						_ = store.ReleaseClaimsByPREnv(a.db, env, rec.PR)
						a.reevaluateHeld(ctx, env)
					}
				}
			}
		}
		return
	}
	// checks_requested: resolve PRs in the merge group and evaluate per env.
	prs, err := a.gh.MergeGroupPRs(ctx, repo, headSHA)
	if err != nil {
		// Fail closed: post unverifiable on whatever envs we can't determine.
		a.postApplyLockUnverifiable(ctx, repo, "", headSHA, 0, "merge_group", "merge group PR resolution failed")
		return
	}
	// Collect all envs touched by the group's PRs and evaluate per env.
	envPRStacks := map[string]map[int][]string{} // env -> pr -> stacks
	for _, pr := range prs {
		envs, err := store.EnvironmentsForPR(a.db, pr)
		if err != nil || len(envs) == 0 {
			a.postApplyLockUnverifiable(ctx, repo, "", headSHA, pr, "merge_group", "no environments for #"+strconv.Itoa(pr))
			return
		}
		for _, env := range envs {
			ps, ok := a.prChangedStacks(env, pr)
			if !ok {
				a.postApplyLockUnverifiable(ctx, repo, env, headSHA, pr, "merge_group", "no successful plan for #"+strconv.Itoa(pr))
				return
			}
			if envPRStacks[env] == nil {
				envPRStacks[env] = map[int][]string{}
			}
			envPRStacks[env][pr] = ps
		}
	}
	// Post one apply-lock/<env> check per env.
	now := time.Now()
	for env, prStacks := range envPRStacks {
		// Union stacks for the env; use the last PR as the owner (single-PR merge groups are the common case).
		var allStacks []string
		ownerPR := 0
		for pr, ss := range prStacks {
			allStacks = append(allStacks, ss...)
			ownerPR = pr
		}
		v := a.evalApplyLock(env, ownerPR, allStacks, now)
		if v.State == "clear" {
			_ = store.ClaimStacks(a.db, env, ownerPR, "", allStacks, now.Add(a.applyLockLease()))
		}
		_ = a.postApplyLock(ctx, repo, env, headSHA, ownerPR, allStacks, "merge_group", v)
	}
}

func (a *App) postApplyLockUnverifiable(ctx context.Context, repo, env, sha string, pr int, kind, reason string) {
	_ = a.postApplyLock(ctx, repo, env, sha, pr, nil, kind, applyLockVerdict{State: "unverifiable", Reason: reason})
}

// applyLockLease is the heartbeat lease window for a claim.
func (a *App) applyLockLease() time.Duration { return 30 * time.Minute }

// reevaluateHeld re-posts every held apply-lock check in env whose blocking
// stacks are now clear (called after any claim release). Each held-check record
// carries its own repo, so this needs no repo arg and works from a sweep too.
func (a *App) reevaluateHeld(ctx context.Context, env string) {
	if !a.cfg.ApplyLock {
		return
	}
	held, err := store.HeldApplyLockChecks(a.db, env)
	if err != nil {
		return
	}
	now := time.Now()
	for _, c := range held {
		v := a.evalApplyLock(env, c.PR, c.Stacks, now)
		if v.State == "clear" && c.Kind == "merge_group" {
			_ = store.ClaimStacks(a.db, env, c.PR, "", c.Stacks, now.Add(a.applyLockLease()))
		}
		_ = a.postApplyLock(ctx, c.Repo, env, c.HeadSHA, c.PR, c.Stacks, c.Kind, v)
	}
}
