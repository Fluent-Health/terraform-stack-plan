package server

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/claims"
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

// evalApplyLock computes the verdict for ownerPR's `stacks` in env at `now` by
// folding env's claim ledger (the source of truth) and querying claims.Held.
// Empty stacks => clear (PR touches nothing in this env). Expiry is enforced at
// read time inside Held (no sweep needed for correctness).
func (a *App) evalApplyLock(env string, pr int, stacks []string, now time.Time) applyLockVerdict {
	cs, err := a.shell.loadClaims(env)
	if err != nil {
		return applyLockVerdict{State: "unverifiable", Reason: "claim-ledger load failed"}
	}
	v := claims.Held(cs, pr, stacks, now)
	if !v.Held {
		return applyLockVerdict{State: "clear"}
	}
	return applyLockVerdict{State: "held", Blocking: v.Blocking,
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

// handlePRApplyLock drives the auto-merge front-end: on open/sync/reopen it posts
// apply-lock/<env> on the PR head for every env the PR touches; on merge it claims
// the PR's stacks (the apply is imminent).
func (a *App) handlePRApplyLock(ctx context.Context, repo string, pr int, merged bool) {
	envs, err := store.EnvironmentsForPR(a.db, pr)
	if err != nil {
		return
	}
	now := a.now()
	for _, env := range envs {
		stacks, ok := a.prChangedStacks(env, pr)
		if merged {
			if ok {
				_ = a.shell.handleClaim(env, claims.AcquireClaim{PR: pr, Stacks: stacks, Now: now})
			}
			continue
		}
		sha, err := a.gh.PRHeadSHA(ctx, repo, pr)
		if err != nil {
			continue
		}
		if !ok {
			a.postApplyLockUnverifiable(ctx, repo, env, sha, pr, "pr_head", "no successful plan for #"+strconv.Itoa(pr))
			continue
		}
		_ = a.postApplyLock(ctx, repo, env, sha, pr, stacks, "pr_head", a.evalApplyLock(env, pr, stacks, now))
	}
}

// handleMergeGroup evaluates the apply-lock for a GitHub merge group and posts
// apply-lock/<env> on the group head. On checks_requested: disjoint => claim at
// greenlight + success; overlap => held. On destroyed: release the tentative claim.
// Returns a non-nil error when the group's PRs or their envs can't be resolved so
// the caller can return 5xx and let GitHub redeliver (fail-closed).
func (a *App) handleMergeGroup(ctx context.Context, repo, headSHA, action string) error {
	if action == "destroyed" {
		// Release per-env claims held by the merge group's constituent PRs.
		// Best-effort: errors here are backed by the TTL backstop.
		prs, err := a.gh.MergeGroupPRs(ctx, repo, headSHA)
		if err == nil {
			for _, pr := range prs {
				envs, _ := store.EnvironmentsForPR(a.db, pr)
				for _, env := range envs {
					if rec, ok, _ := store.GetApplyLockCheck(a.db, env, headSHA); ok {
						_ = a.shell.handleClaim(env, claims.ReleaseClaim{PR: rec.PR})
						a.reevaluateHeld(ctx, env)
					}
				}
			}
		}
		return nil
	}
	// checks_requested: resolve PRs in the merge group and evaluate per env.
	prs, err := a.gh.MergeGroupPRs(ctx, repo, headSHA)
	if err != nil {
		// Can't identify which envs to post on — return error so the webhook
		// returns 5xx and GitHub redelivers, keeping required checks unreported
		// (fail-closed). Do NOT post a misnamed apply-lock check (env="").
		return err
	}
	// Collect all envs touched by the group's PRs and evaluate per env.
	envPRStacks := map[string]map[int][]string{} // env -> pr -> stacks
	for _, pr := range prs {
		envs, err := store.EnvironmentsForPR(a.db, pr)
		if err != nil {
			// Can't determine which envs this PR touches — fail-closed same as above.
			return err
		}
		if len(envs) == 0 {
			// PR has no known envs: post unverifiable on the known env so the
			// check is visible; no env="" misname possible here.
			continue
		}
		for _, env := range envs {
			ps, ok := a.prChangedStacks(env, pr)
			if !ok {
				a.postApplyLockUnverifiable(ctx, repo, env, headSHA, pr, "merge_group", "no successful plan for #"+strconv.Itoa(pr))
				return nil
			}
			if envPRStacks[env] == nil {
				envPRStacks[env] = map[int][]string{}
			}
			envPRStacks[env][pr] = ps
		}
	}
	// Post one apply-lock/<env> check per env.
	now := a.now()
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
			_ = a.shell.handleClaim(env, claims.AcquireClaim{PR: ownerPR, Stacks: allStacks, Now: now})
		}
		_ = a.postApplyLock(ctx, repo, env, headSHA, ownerPR, allStacks, "merge_group", v)
	}
	return nil
}

func (a *App) postApplyLockUnverifiable(ctx context.Context, repo, env, sha string, pr int, kind, reason string) {
	_ = a.postApplyLock(ctx, repo, env, sha, pr, nil, kind, applyLockVerdict{State: "unverifiable", Reason: reason})
}

// sweepClaimsOnce re-evaluates held checks for every environment whose projection
// carries a now-expired claim (the auto-heal tick). The env:<env> event stream is
// the source of truth and Held filters by `now`, so expiry needs no deletion for
// correctness — SweepExpiredClaims only enumerates affected envs (and compacts the
// projection); RebuildClaims then re-derives the projection from the fold.
func (a *App) sweepClaimsOnce(ctx context.Context) {
	envs, err := store.SweepExpiredClaims(a.db, a.now())
	if err != nil {
		return
	}
	for _, env := range envs {
		a.reevaluateHeld(ctx, env)
		_ = a.shell.RebuildClaims(env)
	}
}

// ClaimsSweepLoop periodically releases expired claims and re-evaluates held
// checks (mirrors OrphanSweepLoop).
func (a *App) ClaimsSweepLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.sweepClaimsOnce(ctx)
		}
	}
}

// applyLockLease is the heartbeat lease window for a claim.
func (a *App) applyLockLease() time.Duration { return 30 * time.Minute }

// adminReleaseClaims is the manual un-wedge path: an admin releases one stack's
// claim (stack != "") or all of a PR's claims in env (stack == ""), then
// re-evaluates held checks.
func (a *App) adminReleaseClaims(ctx context.Context, env string, pr int, stack string) {
	if stack != "" {
		_ = a.shell.handleClaim(env, claims.ReleaseClaimStack{PR: pr, Stack: stack})
	} else {
		_ = a.shell.handleClaim(env, claims.ReleaseClaim{PR: pr})
	}
	a.reevaluateHeld(ctx, env)
}

// postPlanApplyLock posts apply-lock/<env> for a PR right after its plan
// finalizes, when the PR's changed stacks are known. The auto-merge (pr_head)
// front-end's pull_request handler fires on PR open — before the plan registers
// the stacks — so without this the check would only appear on a later push.
// Posting here makes it appear reliably alongside plan/<env>, on the same SHA.
func (a *App) postPlanApplyLock(ctx context.Context, e store.Execution) {
	g, err := store.LoadGraph(a.db, e.ID)
	if err != nil {
		return
	}
	stacks := make([]string, 0, len(g.Stacks))
	for _, s := range g.Stacks {
		stacks = append(stacks, s.Path)
	}
	v := a.evalApplyLock(e.Environment, e.PR, stacks, a.now())
	_ = a.postApplyLock(ctx, e.Repo, e.Environment, e.SHA, e.PR, stacks, "pr_head", v)
}

// releaseApplyClaims drops a PR's claims in an env and re-evaluates held checks
// (each held-check record carries its own repo).
func (a *App) releaseApplyClaims(ctx context.Context, env string, pr int) {
	_ = a.shell.handleClaim(env, claims.ReleaseClaim{PR: pr})
	a.reevaluateHeld(ctx, env)
}

// renewApplyClaims extends a PR's lease in an env (apply heartbeat).
func (a *App) renewApplyClaims(env string, pr int) {
	_ = a.shell.handleClaim(env, claims.RenewClaim{PR: pr, Now: a.now()})
}

// reevaluateHeld re-posts every held apply-lock check in env whose blocking
// stacks are now clear (called after any claim release). Each held-check record
// carries its own repo, so this needs no repo arg and works from a sweep too.
func (a *App) reevaluateHeld(ctx context.Context, env string) {
	held, err := store.HeldApplyLockChecks(a.db, env)
	if err != nil {
		return
	}
	now := a.now()
	for _, c := range held {
		v := a.evalApplyLock(env, c.PR, c.Stacks, now)
		if v.State == "clear" && c.Kind == "merge_group" {
			_ = a.shell.handleClaim(env, claims.AcquireClaim{PR: c.PR, Stacks: c.Stacks, Now: now})
		}
		_ = a.postApplyLock(ctx, c.Repo, env, c.HeadSHA, c.PR, c.Stacks, c.Kind, v)
	}
}
