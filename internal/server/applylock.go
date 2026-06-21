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

// suppress unused import warning — env and pr are used in applyLockOutput
