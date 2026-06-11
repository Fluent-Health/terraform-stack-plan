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

func TestClassifiedMarker(t *testing.T) {
	db := newTestDB(t)
	ok, err := IsClassified(db, 42, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("not classified yet, want false")
	}
	if err := MarkClassified(db, 42, "staging"); err != nil {
		t.Fatal(err)
	}
	ok, _ = IsClassified(db, 42, "staging")
	if !ok {
		t.Fatal("want classified true after MarkClassified")
	}
	// Idempotent.
	if err := MarkClassified(db, 42, "staging"); err != nil {
		t.Fatalf("re-mark: %v", err)
	}
}
