package server

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/claims"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// consolidatedApp: armed serve + a MockGitHub that records every check update.
func consolidatedApp(t *testing.T) (*App, *fakeExecutor, *httptest.Server, func() []CheckRunUpdate) {
	t.Helper()
	var mu sync.Mutex
	var updates []CheckRunUpdate
	gh := &MockGitHub{
		CreateCheckRunFn: func(context.Context, string, string, string, string) (int64, error) { return 4242, nil },
		UpdateCheckRunFn: func(_ context.Context, _ string, _ int64, upd CheckRunUpdate) error {
			mu.Lock()
			updates = append(updates, upd)
			mu.Unlock()
			return nil
		},
	}
	a := New(newServerTestDB(t), gh, Config{GitHubWebhookSecret: whSecret, Environment: "nonprod", PublicBaseURL: "https://serve.test"})
	fe := &fakeExecutor{}
	a.Executor = fe
	srv := httptest.NewServer(a.Routes())
	t.Cleanup(srv.Close)
	snap := func() []CheckRunUpdate {
		mu.Lock()
		defer mu.Unlock()
		return append([]CheckRunUpdate(nil), updates...)
	}
	return a, fe, srv, snap
}

// TestConsolidatedCheckHoldsOnOverlappingApply: a clean plan whose stacks
// overlap another PR's in-flight apply must stay in_progress with the
// "waiting on PR #N's apply" title, and record a held pr_head entry pointing
// at the execution.
func TestConsolidatedCheckHoldsOnOverlappingApply(t *testing.T) {
	a, fe, srv, snap := consolidatedApp(t)

	// PR #3 is applying stacks/a right now.
	if err := a.shell.handleClaim("nonprod", claims.AcquireClaim{PR: 3, Stacks: []string{"stacks/a"}, Now: a.now()}); err != nil {
		t.Fatal(err)
	}

	// PR #7 opens; the queued run is created…
	webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "sha-one")).Body.Close()
	id := fe.starts[0].ExecutionID

	// …the runner replays the same execution id and finalizes a clean plan
	// touching stacks/a.
	seedInit(t, a.shell, events.Init{
		ID: id, Repo: "o/r", SHA: "sha-one", PR: 7, Environment: "nonprod",
		Context: "plan/nonprod", Stacks: []events.StackState{{Path: "stacks/a", Status: events.StatusSafe}},
	})
	if err := store.SetReport(a.db, id, "report"); err != nil {
		t.Fatal(err)
	}
	if err := a.shell.Handle(context.Background(), 7, "nonprod", "o/r", reconcile.RunnerFinalize{}); err != nil {
		t.Fatal(err)
	}

	upds := snap()
	if len(upds) == 0 {
		t.Fatal("no check updates recorded")
	}
	last := upds[len(upds)-1]
	if last.Conclusion != "" {
		t.Fatalf("conclusion = %q, want in_progress (lock held)", last.Conclusion)
	}
	if last.Title != "waiting on PR #3's apply" {
		t.Errorf("title = %q", last.Title)
	}
	rec, ok, err := store.GetApplyLockCheck(a.db, "nonprod", "sha-one")
	if err != nil || !ok {
		t.Fatalf("no applylock record: %v ok=%v", err, ok)
	}
	if rec.State != "held" || rec.Kind != "pr_head" || rec.ExecutionID != id || rec.CheckRunID != 4242 {
		t.Errorf("record = %+v", rec)
	}
}

// TestConsolidatedCheckClearConcludesSuccess: no overlap → success, no lock
// section, and the record persisted as clear.
func TestConsolidatedCheckClearConcludesSuccess(t *testing.T) {
	a, fe, srv, snap := consolidatedApp(t)
	webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "sha-one")).Body.Close()
	id := fe.starts[0].ExecutionID
	seedInit(t, a.shell, events.Init{
		ID: id, Repo: "o/r", SHA: "sha-one", PR: 7, Environment: "nonprod",
		Context: "plan/nonprod", Stacks: []events.StackState{{Path: "stacks/a", Status: events.StatusSafe}},
	})
	if err := store.SetReport(a.db, id, "report"); err != nil {
		t.Fatal(err)
	}
	if err := a.shell.Handle(context.Background(), 7, "nonprod", "o/r", reconcile.RunnerFinalize{}); err != nil {
		t.Fatal(err)
	}
	upds := snap()
	last := upds[len(upds)-1]
	if last.Conclusion != "success" {
		t.Fatalf("conclusion = %q, want success", last.Conclusion)
	}
	if strings.Contains(last.Text, "Merge lock") {
		t.Errorf("clear verdict must render no lock section, text = %q", last.Text)
	}
	rec, ok, _ := store.GetApplyLockCheck(a.db, "nonprod", "sha-one")
	if !ok || rec.State != "clear" {
		t.Errorf("record = %+v ok=%v, want clear", rec, ok)
	}
}

// TestConsolidatedReleaseFlipsHeldToSuccess: when the overlapping apply
// releases its claim, the held consolidated check must be re-driven to a
// success conclusion (not a lock-only apply-lock patch). The finalize goes
// through POST /api/finalize so the handler-level postPlanApplyLock gate is
// exercised: ungated, it would re-patch the consolidated check with lock-only
// content ("apply-lock: holding merge") right after the terminal render.
func TestConsolidatedReleaseFlipsHeldToSuccess(t *testing.T) {
	a, fe, srv, snap := consolidatedApp(t)
	if err := a.shell.handleClaim("nonprod", claims.AcquireClaim{PR: 3, Stacks: []string{"stacks/a"}, Now: a.now()}); err != nil {
		t.Fatal(err)
	}
	webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "sha-one")).Body.Close()
	id := fe.starts[0].ExecutionID
	seedInit(t, a.shell, events.Init{
		ID: id, Repo: "o/r", SHA: "sha-one", PR: 7, Environment: "nonprod",
		Context: "plan/nonprod", Stacks: []events.StackState{{Path: "stacks/a", Status: events.StatusSafe}},
	})
	if code := post(t, srv, "/api/finalize", events.Finalize{ID: id, ReportMarkdown: "report"}); code != 200 {
		t.Fatalf("finalize = %d", code)
	}
	// Precondition: the LAST update after finalize is the held consolidated
	// render — not a lock-only postPlanApplyLock re-patch of the same check.
	if last := snap()[len(snap())-1]; last.Conclusion != "" || last.Title != "waiting on PR #3's apply" {
		t.Fatalf("precondition: last update after finalize = title %q conclusion %q, want held render", last.Title, last.Conclusion)
	}

	// PR #3's apply finishes.
	a.releaseApplyClaims(context.Background(), "nonprod", 3)

	upds := snap()
	last := upds[len(upds)-1]
	if last.Conclusion != "success" {
		t.Fatalf("post-release conclusion = %q, want success", last.Conclusion)
	}
	// The release must re-drive the FULL consolidated render (a.drive), not a
	// lock-only patch: the legacy reevaluateHeld→postApplyLock path titles the
	// check "apply-lock: …" and carries no Text/report at all.
	if strings.HasPrefix(last.Title, "apply-lock:") {
		t.Errorf("post-release title = %q — lock-only patch, want full render", last.Title)
	}
	if !strings.Contains(last.Text, "report") {
		t.Errorf("post-release text %q lacks the report markdown — lock-only patch, want full render", last.Text)
	}
	rec, ok, _ := store.GetApplyLockCheck(a.db, "nonprod", "sha-one")
	if !ok || rec.State != "clear" {
		t.Errorf("record after release = %+v ok=%v, want clear", rec, ok)
	}
}

// TestArmedTierPostsNoSeparateApplyLockCheck: the pull_request webhook must not
// create an apply-lock/<env> check run on an armed tier. A REPORTED execution is
// seeded first so handlePRApplyLock's per-env loop actually runs (EnvironmentsForPR
// non-empty, prChangedStacks ok): ungated, it would create a second check run for
// the lock on the PR head, on top of the queued run's terraform/<env> check.
func TestArmedTierPostsNoSeparateApplyLockCheck(t *testing.T) {
	var mu sync.Mutex
	var created []string
	gh := &MockGitHub{
		CreateCheckRunFn: func(_ context.Context, _, _, name, _ string) (int64, error) {
			mu.Lock()
			created = append(created, name)
			mu.Unlock()
			return 4242, nil
		},
		PRHeadSHAFn: func(context.Context, string, int) (string, error) { return "sha-one", nil },
	}
	a := New(newServerTestDB(t), gh, Config{GitHubWebhookSecret: whSecret, Environment: "nonprod", PublicBaseURL: "https://serve.test"})
	a.Executor = &fakeExecutor{}
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	// Seed a reported plan for PR 7 in nonprod (stacks present ⇒
	// LatestReportedExecutionID finds it, prChangedStacks returns ok=true).
	seedInit(t, a.shell, events.Init{
		ID: "seed-1", Repo: "o/r", SHA: "sha-zero", PR: 7, Environment: "nonprod",
		Context: "plan/nonprod", Stacks: []events.StackState{{Path: "stacks/a", Status: events.StatusSafe}},
	})

	webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "sha-one")).Body.Close()
	mu.Lock()
	defer mu.Unlock()
	for _, n := range created {
		if strings.HasPrefix(n, "apply-lock/") {
			t.Fatalf("armed tier created %q — apply-lock is folded into terraform/<env>", n)
		}
	}
	// Exactly one check run: the queued run's terraform/nonprod. An ungated
	// handlePRApplyLock would create a second one for the pr_head lock surface.
	if len(created) != 1 {
		t.Fatalf("created %d check runs %v, want exactly 1 (the queued run's)", len(created), created)
	}
}

// TestMergeGroupCheckNameFollowsArming: merge-group heads keep a lock-only check
// posted via postApplyLock, whose NAME must be the consolidated terraform/<env>
// on an armed tier and stay apply-lock/<env> unarmed.
func TestMergeGroupCheckNameFollowsArming(t *testing.T) {
	run := func(t *testing.T, armed bool, want string) {
		var mu sync.Mutex
		var created []string
		gh := &MockGitHub{
			CreateCheckRunFn: func(_ context.Context, _, _, name, _ string) (int64, error) {
				mu.Lock()
				created = append(created, name)
				mu.Unlock()
				return 4242, nil
			},
			MergeGroupPRsFn: func(context.Context, string, string) ([]int, error) { return []int{5}, nil },
		}
		a := New(newServerTestDB(t), gh, Config{GitHubWebhookSecret: whSecret, Environment: "nonprod", PublicBaseURL: "https://serve.test"})
		if armed {
			a.Executor = &fakeExecutor{}
		}
		// Reported plan for PR 5 so prChangedStacks resolves the group's stacks.
		seedInit(t, a.shell, events.Init{
			ID: "seed-5", Repo: "o/r", SHA: "sha-five", PR: 5, Environment: "nonprod",
			Context: "plan/nonprod", Stacks: []events.StackState{{Path: "stacks/a", Status: events.StatusSafe}},
		})
		if err := a.handleMergeGroup(context.Background(), "o/r", "mg-sha", "", "checks_requested"); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(created) != 1 || created[0] != want {
			t.Fatalf("merge-group check runs = %v, want exactly [%s]", created, want)
		}
	}
	t.Run("armed", func(t *testing.T) { run(t, true, "terraform/nonprod") })
	t.Run("unarmed", func(t *testing.T) { run(t, false, "apply-lock/nonprod") })
}

// TestConsolidatedGateLockThenReleaseSucceeds: the full gate → lock → success
// sequencing on the consolidated check. A PAM-gated plan whose stacks overlap
// another PR's in-flight apply stays in_progress with the awaiting-approval
// title (waiting on a human is pending, not action_required; the held lock
// must NOT mask the gate) → "" with the lock's waiting title (gate satisfied,
// lock still held) → success (claim released).
func TestConsolidatedGateLockThenReleaseSucceeds(t *testing.T) {
	a, fe, srv, snap := consolidatedApp(t)
	fake := approval.NewFake()
	fake.Pool = []string{"sa0@project.iam.gserviceaccount.com"}
	a.Approval = fake

	// PR #3 is applying stacks/a right now.
	if err := a.shell.handleClaim("nonprod", claims.AcquireClaim{PR: 3, Stacks: []string{"stacks/a"}, Now: a.now()}); err != nil {
		t.Fatal(err)
	}

	// PR #7 opens a gated plan touching the same stack.
	webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "sha-one")).Body.Close()
	id := fe.starts[0].ExecutionID
	seedInit(t, a.shell, events.Init{
		ID: id, Repo: "o/r", SHA: "sha-one", PR: 7, Environment: "nonprod",
		Context: "plan/nonprod", Stacks: []events.StackState{{Path: "stacks/a", Project: "proj-a", Status: events.StatusPlanned}},
	})
	if code := post(t, srv, "/api/finalize", events.Finalize{
		ID: id, ReportMarkdown: "report",
		Gates: []events.GateTarget{{Class: "iam", Target: "proj-a"}},
	}); code != 200 {
		t.Fatalf("finalize = %d", code)
	}

	// The gate is pending approval: the check stays in_progress and the title
	// names the wait — the gate takes precedence over the (also-waiting) lock.
	last := snap()[len(snap())-1]
	if last.Conclusion != "" {
		t.Fatalf("conclusion while gate pending = %q, want \"\" (in_progress)", last.Conclusion)
	}
	if last.Title != "awaiting approval — 0 of 1 gates active" {
		t.Fatalf("title while gate pending = %q", last.Title)
	}

	// Approver grants the gate; the reconcile tick converges it to ACTIVE and
	// re-drives the check terminally.
	fake.Approve(approval.Request{Class: "iam", Target: "proj-a", PR: 7, Environment: "nonprod"})
	if err := a.reconcileGate(context.Background(), 7, "nonprod"); err != nil {
		t.Fatalf("reconcileGate: %v", err)
	}

	// Gate satisfied but PR #3's claim is still held: in_progress (empty
	// conclusion), titled for the lock since it is now the sole blocker.
	last = snap()[len(snap())-1]
	if last.Conclusion != "" {
		t.Fatalf("conclusion after gate satisfied (lock still held) = %q, want \"\" (in_progress)", last.Conclusion)
	}
	if last.Title != "waiting on PR #3's apply" {
		t.Fatalf("title after gate satisfied = %q, want \"waiting on PR #3's apply\"", last.Title)
	}

	// PR #3's apply finishes, releasing the claim.
	a.releaseApplyClaims(context.Background(), "nonprod", 3)

	last = snap()[len(snap())-1]
	if last.Conclusion != "success" {
		t.Fatalf("conclusion after release = %q, want success", last.Conclusion)
	}
}

// TestSupersededHeldRecordDoesNotResurrectOldCheck: push sha-two while
// sha-one's record is held; releasing the claim re-drives BOTH records'
// executions, but the superseded run's render must not conclude anything
// that contradicts sha-two's live check. We assert the sha-two record is
// the one keyed for (env, sha-two) and that driving the old execution does
// not error (best-effort, GitHub keyed by its own check_run_id keeps the
// surfaces separate).
func TestSupersededHeldRecordDoesNotResurrectOldCheck(t *testing.T) {
	a, fe, srv, _ := consolidatedApp(t)

	// PR #3 is applying stacks/a right now.
	if err := a.shell.handleClaim("nonprod", claims.AcquireClaim{PR: 3, Stacks: []string{"stacks/a"}, Now: a.now()}); err != nil {
		t.Fatal(err)
	}

	// PR #7 opens at sha-one; its plan overlaps PR #3's claim and holds.
	webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "sha-one")).Body.Close()
	id1 := fe.starts[0].ExecutionID
	seedInit(t, a.shell, events.Init{
		ID: id1, Repo: "o/r", SHA: "sha-one", PR: 7, Environment: "nonprod",
		Context: "plan/nonprod", Stacks: []events.StackState{{Path: "stacks/a", Status: events.StatusSafe}},
	})
	if err := store.SetReport(a.db, id1, "report"); err != nil {
		t.Fatal(err)
	}
	if err := a.shell.Handle(context.Background(), 7, "nonprod", "o/r", reconcile.RunnerFinalize{}); err != nil {
		t.Fatal(err)
	}
	if rec, ok, _ := store.GetApplyLockCheck(a.db, "nonprod", "sha-one"); !ok || rec.State != "held" {
		t.Fatalf("precondition: sha-one record = %+v ok=%v, want held", rec, ok)
	}

	// PR #7 is superseded by a push to sha-two, which replays the same flow.
	webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "sha-two")).Body.Close()
	id2 := fe.starts[1].ExecutionID
	seedInit(t, a.shell, events.Init{
		ID: id2, Repo: "o/r", SHA: "sha-two", PR: 7, Environment: "nonprod",
		Context: "plan/nonprod", Stacks: []events.StackState{{Path: "stacks/a", Status: events.StatusSafe}},
	})
	if err := store.SetReport(a.db, id2, "report"); err != nil {
		t.Fatal(err)
	}
	if err := a.shell.Handle(context.Background(), 7, "nonprod", "o/r", reconcile.RunnerFinalize{}); err != nil {
		t.Fatal(err)
	}
	if rec, ok, _ := store.GetApplyLockCheck(a.db, "nonprod", "sha-two"); !ok || rec.State != "held" {
		t.Fatalf("precondition: sha-two record = %+v ok=%v, want held", rec, ok)
	}

	// PR #3's apply finishes: releasing the claim re-drives every held record in
	// the env, including the superseded sha-one execution. This must not panic
	// or error even though sha-one's execution is no longer PR #7's latest.
	a.releaseApplyClaims(context.Background(), "nonprod", 3)

	// sha-two — the live record for (env, sha-two) — must clear.
	rec, ok, err := store.GetApplyLockCheck(a.db, "nonprod", "sha-two")
	if err != nil || !ok {
		t.Fatalf("sha-two record: %v ok=%v", err, ok)
	}
	if rec.State != "clear" {
		t.Errorf("sha-two record state = %q, want clear", rec.State)
	}
	// sha-one's record may stay held or flip clear — both are harmless: its
	// check run lives on the old SHA and GitHub keys check runs by their own
	// check_run_id, so re-driving it cannot resurrect a stale surface onto
	// sha-two's PR head. We only require that driving it did not error above.
}

// TestReevaluateHeldFallsBackToLegacyWhenDisarmed: a DISARMED serve (rollback
// or misconfig) that still carries a leftover held CONSOLIDATED record
// (ExecutionID set, from before the disarm) must NOT take the consolidated
// re-drive branch in reevaluateHeld. Consolidated re-drive calls
// a.drive→renderAndPatch, whose lock fold is itself armed-gated — on an
// unarmed app it would skip the lock fold entirely and conclude "success"
// while the lock is STILL held (a merge-lock bypass). The legacy fallback
// (evaluate c.Stacks directly, PATCH the check held/in_progress) is the
// fail-safe path and must run instead.
func TestReevaluateHeldFallsBackToLegacyWhenDisarmed(t *testing.T) {
	var mu sync.Mutex
	var updates []CheckRunUpdate
	gh := &MockGitHub{
		UpdateCheckRunFn: func(_ context.Context, _ string, _ int64, upd CheckRunUpdate) error {
			mu.Lock()
			updates = append(updates, upd)
			mu.Unlock()
			return nil
		},
	}
	// No Executor wired => a.runTriggerArmed() is false (disarmed tier).
	a := New(newServerTestDB(t), gh, Config{Environment: "nonprod", PublicBaseURL: "https://serve.test"})

	// PR #3 is applying stacks/a right now — the blocking claim.
	if err := a.shell.handleClaim("nonprod", claims.AcquireClaim{PR: 3, Stacks: []string{"stacks/a"}, Now: a.now()}); err != nil {
		t.Fatal(err)
	}

	// A matching execution row for PR #7 so the legacy path has stacks to read
	// if it ever needs to resolve them independently of the record.
	seedInit(t, a.shell, events.Init{
		ID: "exec-7", Repo: "o/r", SHA: "sha-one", PR: 7, Environment: "nonprod",
		Context: "plan/nonprod", Stacks: []events.StackState{{Path: "stacks/a", Status: events.StatusSafe}},
	})
	if err := store.SetCheckRunID(a.db, "exec-7", 4242); err != nil {
		t.Fatal(err)
	}
	if err := store.SetReport(a.db, "exec-7", "report"); err != nil {
		t.Fatal(err)
	}

	// A leftover HELD consolidated record (ExecutionID set) — as if this SHA's
	// check was last rendered while the tier was armed, before being disarmed.
	if err := store.UpsertApplyLockCheck(a.db, store.ApplyLockCheck{
		Environment: "nonprod", HeadSHA: "sha-one", CheckRunID: 4242, PR: 7, Repo: "o/r",
		Stacks: []string{"stacks/a"}, State: "held", Kind: "pr_head", ExecutionID: "exec-7",
	}); err != nil {
		t.Fatal(err)
	}

	// The blocking claim is still held when we re-evaluate.
	a.reevaluateHeld(context.Background(), "nonprod")

	mu.Lock()
	defer mu.Unlock()
	if len(updates) == 0 {
		t.Fatal("no check updates recorded — reevaluateHeld should have patched via the legacy path")
	}
	last := updates[len(updates)-1]
	if last.Conclusion == "success" {
		t.Fatalf("conclusion = %q, want NOT success — the lock is still held (merge-lock bypass)", last.Conclusion)
	}
	rec, ok, err := store.GetApplyLockCheck(a.db, "nonprod", "sha-one")
	if err != nil || !ok {
		t.Fatalf("applylock record: %v ok=%v", err, ok)
	}
	if rec.State != "held" {
		t.Errorf("record state = %q, want held", rec.State)
	}
}

// TestConsolidatedClearRecordSurvivesFailedPatch: the "clear" record must only
// be persisted AFTER UpdateCheckRun succeeds. If the release re-drive's PATCH
// fails transiently, the record must stay "held" — so a later
// release/sweep still finds it via HeldApplyLockChecks and retries — rather
// than being wrongly marked "clear" while the live check is stuck in_progress
// forever.
func TestConsolidatedClearRecordSurvivesFailedPatch(t *testing.T) {
	var mu sync.Mutex
	var updates []CheckRunUpdate
	failNext := false
	gh := &MockGitHub{
		CreateCheckRunFn: func(context.Context, string, string, string, string) (int64, error) { return 4242, nil },
		UpdateCheckRunFn: func(_ context.Context, _ string, _ int64, upd CheckRunUpdate) error {
			mu.Lock()
			defer mu.Unlock()
			if failNext {
				failNext = false
				return fmt.Errorf("transient github error")
			}
			updates = append(updates, upd)
			return nil
		},
	}
	a := New(newServerTestDB(t), gh, Config{GitHubWebhookSecret: whSecret, Environment: "nonprod", PublicBaseURL: "https://serve.test"})
	fe := &fakeExecutor{}
	a.Executor = fe
	srv := httptest.NewServer(a.Routes())
	t.Cleanup(srv.Close)
	snap := func() []CheckRunUpdate {
		mu.Lock()
		defer mu.Unlock()
		return append([]CheckRunUpdate(nil), updates...)
	}

	// PR #3 is applying stacks/a right now.
	if err := a.shell.handleClaim("nonprod", claims.AcquireClaim{PR: 3, Stacks: []string{"stacks/a"}, Now: a.now()}); err != nil {
		t.Fatal(err)
	}
	webhookReq(t, srv, whSecret, "pull_request", prSyncPayload(7, "sha-one")).Body.Close()
	id := fe.starts[0].ExecutionID
	seedInit(t, a.shell, events.Init{
		ID: id, Repo: "o/r", SHA: "sha-one", PR: 7, Environment: "nonprod",
		Context: "plan/nonprod", Stacks: []events.StackState{{Path: "stacks/a", Status: events.StatusSafe}},
	})
	if code := post(t, srv, "/api/finalize", events.Finalize{ID: id, ReportMarkdown: "report"}); code != 200 {
		t.Fatalf("finalize = %d", code)
	}
	if rec, ok, _ := store.GetApplyLockCheck(a.db, "nonprod", "sha-one"); !ok || rec.State != "held" {
		t.Fatalf("precondition: record = %+v ok=%v, want held", rec, ok)
	}

	// PR #3's apply finishes, but the re-drive's PATCH fails transiently.
	mu.Lock()
	failNext = true
	mu.Unlock()
	a.releaseApplyClaims(context.Background(), "nonprod", 3)

	rec, ok, err := store.GetApplyLockCheck(a.db, "nonprod", "sha-one")
	if err != nil || !ok {
		t.Fatalf("applylock record after failed patch: %v ok=%v", err, ok)
	}
	if rec.State != "held" {
		t.Fatalf("record state after failed patch = %q, want held (failed patch must not clear the record)", rec.State)
	}

	// A subsequent release/sweep succeeds: the record flips to clear and the
	// check concludes success.
	a.releaseApplyClaims(context.Background(), "nonprod", 3)
	upds := snap()
	if len(upds) == 0 {
		t.Fatal("no successful update recorded after retry")
	}
	last := upds[len(upds)-1]
	if last.Conclusion != "success" {
		t.Fatalf("conclusion after retry = %q, want success", last.Conclusion)
	}
	rec, ok, err = store.GetApplyLockCheck(a.db, "nonprod", "sha-one")
	if err != nil || !ok {
		t.Fatalf("applylock record after retry: %v ok=%v", err, ok)
	}
	if rec.State != "clear" {
		t.Errorf("record state after retry = %q, want clear", rec.State)
	}
}
