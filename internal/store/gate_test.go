package store

import (
	"database/sql"
	"testing"
)

// seedGateTargetSQL inserts a row directly into the gate_targets projection
// table, mirroring the gate_targets projection write in shell.project exactly
// (same columns, ON CONFLICT clause, and update set). Tests for live read
// functions (PendingGates, PRTargets, TargetsFor) use this instead of the
// removed UpsertTarget writer.
func seedGateTargetSQL(t *testing.T, db *sql.DB, pr int, environment, class, target, grant, state, requester string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO gate_targets (pr, environment, class, target, grant_name, state, requester)
		 VALUES (?,?,?,?,?,?,?)
		 ON CONFLICT(pr, environment, class, target) DO UPDATE SET
		   grant_name=excluded.grant_name, state=excluded.state,
		   requester=excluded.requester, updated_at=CURRENT_TIMESTAMP`,
		pr, environment, class, target, grant, state, requester)
	if err != nil {
		t.Fatal(err)
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

// TestPendingGates seeds the gate_targets projection via raw SQL (the same path
// the production shell.project uses) and verifies PendingGates reads it correctly.
func TestPendingGates(t *testing.T) {
	db := newTestDB(t)
	// gate 42/staging: one target still awaiting → pending.
	seedGateTargetSQL(t, db, 42, "staging", "iam", "proj-a", "g1", "ACTIVE", "")
	seedGateTargetSQL(t, db, 42, "staging", "iam", "proj-b", "g2", "AWAITING", "")
	// gate 7/prod: all active → not pending.
	seedGateTargetSQL(t, db, 7, "prod", "iam", "proj-c", "g3", "ACTIVE", "")

	pending, err := PendingGates(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0] != (Gate{PR: 42, Environment: "staging"}) {
		t.Fatalf("pending = %+v; want [42/staging]", pending)
	}
}

// TestPRTargets seeds the gate_targets projection via raw SQL and verifies
// PRTargets returns the correct cross-environment set for a PR.
func TestPRTargets(t *testing.T) {
	db := newTestDB(t)
	// Two environments for PR 7.
	seedGateTargetSQL(t, db, 7, "nonprod", "iam", "proj-a", "g1", "AWAITING", "")
	seedGateTargetSQL(t, db, 7, "prod", "iam", "proj-b", "g2", "ACTIVE", "")
	// Different PR — must not appear in PR 7's results.
	seedGateTargetSQL(t, db, 8, "nonprod", "iam", "proj-a", "g3", "AWAITING", "")

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
