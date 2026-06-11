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
