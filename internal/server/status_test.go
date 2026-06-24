package server

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// newServerTestDB opens a fresh migrated SQLite database for server tests.
func newServerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "server.db")
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestConclusion(t *testing.T) {
	cases := []struct {
		name string
		s    snapshot
		want string
	}{
		{"still planning", snapshot{totalStacks: 3, plannedStacks: 1}, ""},
		{"failed", snapshot{anyFailed: true, finalized: true}, "failure"},
		{"clean finalized", snapshot{finalized: true}, "success"},
		{"gated awaiting", snapshot{finalized: true, totalGates: 2, activeGates: 1}, "action_required"},
		{"gated all active", snapshot{finalized: true, totalGates: 2, activeGates: 2}, "success"},
		{"failure beats gates", snapshot{anyFailed: true, totalGates: 2}, "failure"},
	}
	for _, c := range cases {
		if got := conclusion(c.s); got != c.want {
			t.Errorf("%s: conclusion = %q, want %q", c.name, got, c.want)
		}
	}
}

// seedProjectionTarget inserts a row directly into the gate_targets projection
// table, mirroring the gate_targets projection write in shell.project exactly
// (same columns, ON CONFLICT clause, and update set). Used here to seed the
// derived projection for read-path tests (loadSnapshot, etc.) without going
// through the full Handle flow.
func seedProjectionTarget(t *testing.T, db *sql.DB, pr int, environment, class, target, grant, state, requester string) {
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

func TestLoadSnapshot(t *testing.T) {
	db := newServerTestDB(t)
	in := events.Init{
		ID: "e1", Repo: "o/r", SHA: "sha", PR: 7, Environment: "staging",
		Stacks: []events.StackState{
			{Path: "a", Status: events.StatusPlanned},
			{Path: "b", Status: events.StatusFailed},
			{Path: "c"},
		},
	}
	if err := store.UpsertInit(db, in); err != nil {
		t.Fatal(err)
	}
	_ = store.SetReport(db, "e1", "# report")
	// Seed the gate_targets projection directly (same SQL as shell.project) so
	// loadSnapshot's TargetsFor call sees the expected rows.
	seedProjectionTarget(t, db, 7, "staging", "iam", "proj-a", "g1", "ACTIVE", "")
	seedProjectionTarget(t, db, 7, "staging", "iam", "proj-b", "g2", "AWAITING", "")

	snap, exec, ok := loadSnapshot(db, "e1")
	if !ok {
		t.Fatal("loadSnapshot ok=false")
	}
	if exec.Repo != "o/r" || exec.PR != 7 || exec.Environment != "staging" {
		t.Errorf("exec = %+v", exec)
	}
	if snap.totalStacks != 3 || snap.plannedStacks != 2 || !snap.anyFailed || !snap.finalized {
		t.Errorf("snap stacks = %+v", snap)
	}
	if snap.totalGates != 2 || snap.activeGates != 1 {
		t.Errorf("snap gates = %d/%d, want 1/2 active", snap.activeGates, snap.totalGates)
	}
}
