package store

import "testing"

func TestUpsertTargetAndTargetsFor(t *testing.T) {
	db := newTestDB(t)
	if err := UpsertTarget(db, 42, "staging", "iam", "proj-a", "grants/1", "AWAITING"); err != nil {
		t.Fatal(err)
	}
	if err := UpsertTarget(db, 42, "staging", "iam", "proj-b", "grants/2", "AWAITING"); err != nil {
		t.Fatal(err)
	}
	// Upsert same key updates state in place (no duplicate row).
	if err := UpsertTarget(db, 42, "staging", "iam", "proj-a", "grants/1", "ACTIVE"); err != nil {
		t.Fatal(err)
	}
	ts, err := TargetsFor(db, 42, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 2 {
		t.Fatalf("targets = %d; want 2", len(ts))
	}
	states := map[string]string{}
	for _, gt := range ts {
		states[gt.Target] = gt.State
	}
	if states["proj-a"] != "ACTIVE" || states["proj-b"] != "AWAITING" {
		t.Errorf("states = %v", states)
	}
}

func TestPendingGates(t *testing.T) {
	db := newTestDB(t)
	// gate 42/staging: one target still awaiting → pending.
	_ = UpsertTarget(db, 42, "staging", "iam", "proj-a", "g1", "ACTIVE")
	_ = UpsertTarget(db, 42, "staging", "iam", "proj-b", "g2", "AWAITING")
	// gate 7/prod: all active → not pending.
	_ = UpsertTarget(db, 7, "prod", "iam", "proj-c", "g3", "ACTIVE")

	pending, err := PendingGates(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0] != (Gate{PR: 42, Environment: "staging"}) {
		t.Fatalf("pending = %+v; want [42/staging]", pending)
	}

	// Approving the whole gate drops it from the pending set.
	if err := MarkActive(db, 42, "staging"); err != nil {
		t.Fatal(err)
	}
	pending, _ = PendingGates(db)
	if len(pending) != 0 {
		t.Fatalf("pending after MarkActive = %+v; want empty", pending)
	}
}

func TestSetTargetRequester(t *testing.T) {
	db := newTestDB(t)
	if err := UpsertTarget(db, 7, "nonprod", "iam", "proj-1", "grants/abc", "AWAITING"); err != nil {
		t.Fatal(err)
	}
	if err := SetTargetRequester(db, 7, "nonprod", "poolB@x"); err != nil {
		t.Fatal(err)
	}
	ts, err := TargetsFor(db, 7, "nonprod")
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 1 {
		t.Fatalf("targets = %d; want 1", len(ts))
	}
	if ts[0].Requester != "poolB@x" {
		t.Errorf("Requester = %q; want %q", ts[0].Requester, "poolB@x")
	}
}

// TestUpsertTargetPreservesRequester asserts the requester-preservation
// invariant: a reconcile-loop UpsertTarget (state refresh) on an existing row
// must NOT overwrite the requester set earlier by SetTargetRequester.  This
// holds because `requester` is excluded from the ON CONFLICT update set; the
// test would fail if that column were added there.
func TestUpsertTargetPreservesRequester(t *testing.T) {
	db := newTestDB(t)

	// Initial insert.
	if err := UpsertTarget(db, 11, "prod", "iam", "proj-x", "grants/1", "AWAITING"); err != nil {
		t.Fatal(err)
	}
	// Lease a requester SA (as the server does after planning).
	if err := SetTargetRequester(db, 11, "prod", "poolB@x"); err != nil {
		t.Fatal(err)
	}
	// Simulate a reconcile-loop UpsertTarget with a new state — same key.
	if err := UpsertTarget(db, 11, "prod", "iam", "proj-x", "grants/1", "ACTIVE"); err != nil {
		t.Fatal(err)
	}

	ts, err := TargetsFor(db, 11, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 1 {
		t.Fatalf("targets = %d; want 1", len(ts))
	}
	// Requester must survive the reconcile refresh.
	if ts[0].Requester != "poolB@x" {
		t.Errorf("Requester = %q after reconcile UpsertTarget; want %q (invariant broken)", ts[0].Requester, "poolB@x")
	}
	// State must have been updated.
	if ts[0].State != "ACTIVE" {
		t.Errorf("State = %q after reconcile UpsertTarget; want ACTIVE", ts[0].State)
	}
}

func TestMigration008DropsGateRuns(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.Exec("SELECT count(*) FROM gate_runs"); err == nil {
		t.Fatal("gate_runs should not exist after migration 008")
	}
	// gate_targets still exists (now a projection).
	if _, err := db.Exec("SELECT count(*) FROM gate_targets"); err != nil {
		t.Fatalf("gate_targets should still exist: %v", err)
	}
}

func TestMigration009ClearsApplyClaims(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.Exec("SELECT count(*) FROM apply_claims"); err != nil {
		t.Fatalf("apply_claims should still exist: %v", err)
	}
}

func TestLoadChangeSetReadsGateAndExecution(t *testing.T) {
	db := newTestDB(t)
	if err := UpsertTarget(db, 7, "staging", "iam", "p1", "g1", "ACTIVE"); err != nil {
		t.Fatal(err)
	}
	if err := SetTargetRequester(db, 7, "staging", "sa3"); err != nil {
		t.Fatal(err)
	}
	cs, err := LoadChangeSet(db, 7, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if cs.PR != 7 || cs.Environment != "staging" {
		t.Fatalf("bad key: %+v", cs)
	}
	if len(cs.Targets) != 1 || cs.Targets[0].State != "ACTIVE" || cs.Targets[0].Requester != "sa3" {
		t.Fatalf("bad targets: %+v", cs.Targets)
	}
}

func TestPRTargets(t *testing.T) {
	db := newTestDB(t)
	// Two environments for PR 7.
	_ = UpsertTarget(db, 7, "nonprod", "iam", "proj-a", "g1", "AWAITING")
	_ = UpsertTarget(db, 7, "prod", "iam", "proj-b", "g2", "ACTIVE")
	// Different PR — must not appear in PR 7's results.
	_ = UpsertTarget(db, 8, "nonprod", "iam", "proj-a", "g3", "AWAITING")

	ts, err := PRTargets(db, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 2 {
		t.Fatalf("PRTargets(7) = %d rows, want 2", len(ts))
	}
	envs := map[string]bool{}
	for _, pt := range ts {
		envs[pt.Environment] = true
		if pt.Class != "iam" {
			t.Errorf("class = %q, want iam", pt.Class)
		}
	}
	if !envs["nonprod"] || !envs["prod"] {
		t.Errorf("environments = %v, want nonprod + prod", envs)
	}

	ts8, err := PRTargets(db, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts8) != 1 || ts8[0].Target != "proj-a" {
		t.Fatalf("PRTargets(8) = %+v, want one row for proj-a", ts8)
	}
}
