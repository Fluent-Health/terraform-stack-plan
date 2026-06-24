package server

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/claims"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// activeClaimedStacks returns unexpired claims as stack→PR (mirrors the old
// ClaimedStacks projection query).
func activeClaimedStacks(db *sql.DB, env string, now time.Time) map[string]int {
	cs, _ := store.ListClaims(db, env)
	out := map[string]int{}
	for _, c := range cs {
		if c.ExpiresAt.After(now) {
			out[c.StackPath] = c.OwnerPR
		}
	}
	return out
}

func ctx() context.Context { return context.Background() }

func newApplyLockTestApp(t *testing.T) (*App, *recordingGitHub) {
	t.Helper()
	db := newServerTestDB(t)
	gh := &recordingGitHub{}
	a := New(db, gh, Config{PublicBaseURL: "https://srv"})
	return a, gh
}

// seedPlan upserts an execution with the given stacks so prChangedStacks
// (and EnvironmentsForPR) finds a plan for (pr, env).
func seedPlan(t *testing.T, db *sql.DB, pr int, env, repo, headSHA string, stacks []string) {
	t.Helper()
	ss := make([]events.StackState, len(stacks))
	for i, s := range stacks {
		ss[i] = events.StackState{Path: s}
	}
	if err := store.UpsertInit(db, events.Init{
		ID:          headSHA + "-" + env,
		Repo:        repo,
		SHA:         headSHA,
		PR:          pr,
		Environment: env,
		Stacks:      ss,
	}); err != nil {
		t.Fatalf("seedPlan: %v", err)
	}
}

// recordingGitHub is a test double that records UpdateCheckRun calls
// and exposes a configurable list of PRs for MergeGroupPRs.
type recordingGitHub struct {
	lastUpdate       CheckRunUpdate
	updateCount      int
	mergeGroupPRs    []int
	mergeGroupPRsErr error
	prHeadSHA        string
}

func (r *recordingGitHub) CreateCheckRun(_ context.Context, _, _, _ string, _ string) (int64, error) {
	return 99, nil
}

func (r *recordingGitHub) UpdateCheckRun(_ context.Context, _ string, _ int64, u CheckRunUpdate) error {
	r.lastUpdate = u
	r.updateCount++
	return nil
}

func (r *recordingGitHub) PostStatus(_ context.Context, _, _, _, _, _, _ string) error {
	return nil
}

func (r *recordingGitHub) PRHeadSHA(_ context.Context, _ string, _ int) (string, error) {
	return r.prHeadSHA, nil
}

func (r *recordingGitHub) PRAbandoned(_ context.Context, _ string, _ int) (bool, error) {
	return false, nil
}

func (r *recordingGitHub) MergeGroupPRs(_ context.Context, _, _ string) ([]int, error) {
	if r.mergeGroupPRsErr != nil {
		return nil, r.mergeGroupPRsErr
	}
	return r.mergeGroupPRs, nil
}

func TestClaimsEndpoints(t *testing.T) {
	a, _ := newApplyLockTestApp(t)
	// Seed through the ledger (the source of truth) so adminReleaseClaims —
	// which now drives handleClaim(ReleaseClaim*) → fold → project — sees it.
	_ = a.shell.handleClaim("prod", claims.AcquireClaim{PR: 7, Stacks: []string{"a", "b"}, Now: time.Now()})
	// release via the App method the handler calls:
	a.adminReleaseClaims(ctx(), "prod", 7, "a") // single stack
	got, _ := store.ListClaims(a.db, "prod")
	if len(got) != 1 || got[0].StackPath != "b" {
		t.Fatalf("after single-stack release: %+v", got)
	}
	// release the remaining stack (pr-level)
	a.adminReleaseClaims(ctx(), "prod", 7, "")
	got2, _ := store.ListClaims(a.db, "prod")
	if len(got2) != 0 {
		t.Fatalf("after pr-level release: %+v", got2)
	}
}

func TestPostApplyLock(t *testing.T) {
	a, gh := newApplyLockTestApp(t)
	// held verdict => check created, left in_progress (no conclusion), record persisted held.
	v := applyLockVerdict{State: "held", Blocking: []string{"a"}, Reason: "x"}
	if err := a.postApplyLock(ctx(), "o/r", "prod", "sha1", 7, []string{"a"}, "merge_group", v); err != nil {
		t.Fatal(err)
	}
	if gh.lastUpdate.Conclusion != "" {
		t.Errorf("held check should have empty conclusion, got %q", gh.lastUpdate.Conclusion)
	}
	rec, ok, _ := store.GetApplyLockCheck(a.db, "prod", "sha1")
	if !ok || rec.State != "held" {
		t.Fatalf("record = %+v ok=%v, want held", rec, ok)
	}
	// clear verdict => conclusion success.
	_ = a.postApplyLock(ctx(), "o/r", "prod", "sha1", 7, []string{"a"}, "merge_group", applyLockVerdict{State: "clear"})
	if gh.lastUpdate.Conclusion != "success" {
		t.Errorf("clear check conclusion = %q, want success", gh.lastUpdate.Conclusion)
	}
}

func TestMergeGroupHeldThenClaim(t *testing.T) {
	a, gh := newApplyLockTestApp(t)
	gh.mergeGroupPRs = []int{7}
	seedPlan(t, a.db, 7, "prod", "o/r", "headPR", []string{"a", "b"}) // helper: Init w/ stacks
	// Another PR holds stack "a" => held (seed via the ledger, the fold backs evalApplyLock).
	_ = a.shell.handleClaim("prod", claims.AcquireClaim{PR: 5, Stacks: []string{"a"}, Now: time.Now()})
	if err := a.handleMergeGroup(ctx(), "o/r", "mgsha", "checks_requested"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gh.lastUpdate.Conclusion != "" {
		t.Fatalf("overlap merge group should be held, got conclusion %q", gh.lastUpdate.Conclusion)
	}
	// Now the conflicting claim is released => a fresh checks_requested clears + claims.
	_ = a.shell.handleClaim("prod", claims.ReleaseClaim{PR: 5})
	gh.lastUpdate = CheckRunUpdate{}
	if err := a.handleMergeGroup(ctx(), "o/r", "mgsha", "checks_requested"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gh.lastUpdate.Conclusion != "success" {
		t.Fatalf("disjoint merge group conclusion = %q, want success", gh.lastUpdate.Conclusion)
	}
	if c := activeClaimedStacks(a.db, "prod", time.Now()); c["a"] != 7 || c["b"] != 7 {
		t.Fatalf("greenlight did not claim: %v", c)
	}
}

func TestMergeGroupPRResolutionError(t *testing.T) {
	a, gh := newApplyLockTestApp(t)
	gh.mergeGroupPRsErr = fmt.Errorf("github unavailable")
	err := a.handleMergeGroup(ctx(), "o/r", "mgsha", "checks_requested")
	if err == nil {
		t.Fatal("expected non-nil error when MergeGroupPRs fails, got nil")
	}
	if gh.updateCount != 0 {
		t.Fatalf("expected no check posted, got %d UpdateCheckRun call(s)", gh.updateCount)
	}
}

func TestPRApplyLockEvaluateAndClaim(t *testing.T) {
	a, gh := newApplyLockTestApp(t)
	gh.prHeadSHA = "prhead"
	seedPlan(t, a.db, 7, "prod", "o/r", "prhead", []string{"a"})
	// open/sync: disjoint ⇒ success on PR head.
	a.handlePRApplyLock(ctx(), "o/r", 7, false)
	if gh.lastUpdate.Conclusion != "success" {
		t.Fatalf("PR-head check = %q, want success", gh.lastUpdate.Conclusion)
	}
	// merged ⇒ claim the PR's stacks.
	a.handlePRApplyLock(ctx(), "o/r", 7, true)
	if c := activeClaimedStacks(a.db, "prod", time.Now()); c["a"] != 7 {
		t.Fatalf("merged did not claim: %v", c)
	}
}

func TestApplyFinalizeReleasesClaims(t *testing.T) {
	a, _ := newApplyLockTestApp(t)
	_ = a.shell.handleClaim("prod", claims.AcquireClaim{PR: 7, Stacks: []string{"a"}, Now: time.Now()})
	a.releaseApplyClaims(ctx(), "prod", 7)
	if c := activeClaimedStacks(a.db, "prod", time.Now()); len(c) != 0 {
		t.Fatalf("finalize did not release: %v", c)
	}
	if cs, _ := a.shell.loadClaims("prod"); len(cs) != 0 {
		t.Fatalf("finalize did not release from the fold: %v", cs)
	}
}

func TestSweepClaimsOnceReleasesAndReevaluates(t *testing.T) {
	a, gh := newApplyLockTestApp(t)
	// A held merge-group check waiting on stack "a" claimed by PR 5 with an expired lease.
	seedPlan(t, a.db, 7, "prod", "o/r", "mgsha", []string{"a"})
	// Seed via the ledger at a back-dated Now so the lease (Now+Lease()) is already
	// expired relative to the sweep's a.now() — the fold backs the held re-eval.
	_ = a.shell.handleClaim("prod", claims.AcquireClaim{PR: 5, Stacks: []string{"a"}, Now: time.Now().Add(-2 * claims.Lease())})
	_ = store.UpsertApplyLockCheck(a.db, store.ApplyLockCheck{
		Environment: "prod", HeadSHA: "mgsha", CheckRunID: 1, PR: 7,
		Repo: "o/r", Stacks: []string{"a"}, State: "held", Kind: "merge_group"})
	a.sweepClaimsOnce(ctx())
	if gh.lastUpdate.Conclusion != "success" {
		t.Fatalf("sweep did not clear the held check: %q", gh.lastUpdate.Conclusion)
	}
}

func TestSweepExpiredClaimsUsesInjectedClock(t *testing.T) {
	a, _ := newApplyLockTestApp(t)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return base }

	// Seed via the ledger for env="nonprod" pr=1 stack="a" (lease = base + Lease()).
	_ = a.shell.handleClaim("nonprod", claims.AcquireClaim{PR: 1, Stacks: []string{"a"}, Now: base})

	// Before expiry: sweep keeps the claim.
	a.now = func() time.Time { return base.Add(time.Second) }
	a.sweepClaimsOnce(ctx())
	if c := activeClaimedStacks(a.db, "nonprod", base.Add(time.Second)); c["a"] != 1 {
		t.Fatalf("claim should still exist before expiry: %v", c)
	}

	// After expiry: advancing the injected clock past the lease releases it.
	a.now = func() time.Time { return base.Add(24 * time.Hour) }
	a.sweepClaimsOnce(ctx())
	if c := activeClaimedStacks(a.db, "nonprod", base.Add(24*time.Hour)); len(c) != 0 {
		t.Fatalf("claim should be gone after expiry: %v", c)
	}
}

// TestPostPlanApplyLockOnFinalize covers the fix for the auto-merge front-end:
// the apply-lock/<env> check must be posted when the plan FINALIZES (stacks now
// known), since the pull_request webhook fires before the plan registers them.
func TestPostPlanApplyLockOnFinalize(t *testing.T) {
	a, gh := newApplyLockTestApp(t)
	seedPlan(t, a.db, 7, "prod", "o/r", "sha7", []string{"a", "b"})
	e, err := store.GetExecution(a.db, "sha7-prod")
	if err != nil {
		t.Fatal(err)
	}
	// Disjoint ⇒ apply-lock/prod posted as success on the plan's SHA + recorded.
	a.postPlanApplyLock(ctx(), e)
	if gh.lastUpdate.Conclusion != "success" {
		t.Fatalf("plan-finalize apply-lock conclusion = %q, want success", gh.lastUpdate.Conclusion)
	}
	if rec, ok, _ := store.GetApplyLockCheck(a.db, "prod", "sha7"); !ok || rec.PR != 7 {
		t.Fatalf("applylock_checks not persisted on sha7: %+v ok=%v", rec, ok)
	}
	// Overlap with another PR's in-flight apply ⇒ held (empty conclusion).
	_ = a.shell.handleClaim("prod", claims.AcquireClaim{PR: 9, Stacks: []string{"a"}, Now: time.Now()})
	gh.lastUpdate = CheckRunUpdate{}
	a.postPlanApplyLock(ctx(), e)
	if gh.lastUpdate.Conclusion != "" {
		t.Fatalf("overlapping plan-finalize should be held (empty conclusion), got %q", gh.lastUpdate.Conclusion)
	}
}
