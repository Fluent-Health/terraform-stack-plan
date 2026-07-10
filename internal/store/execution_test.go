package store

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func sampleInit() events.Init {
	return events.Init{
		ID:          "exec-1",
		Repo:        "owner/repo",
		SHA:         "abc123",
		PR:          42,
		Environment: "staging",
		LogURL:      "https://ci/log",
		Context:     "iam/staging",
		Stacks: []events.StackState{
			{Path: "stacks/a", Project: "proj-a"}, // status defaults to pending
			{Path: "stacks/b", Project: "proj-b", Status: events.StatusRunning},
		},
		Edges: []events.Edge{{From: "stacks/a", To: "stacks/b"}},
	}
}

func TestUpsertInitAndLoadGraph(t *testing.T) {
	db := newTestDB(t)
	if err := UpsertInit(db, sampleInit()); err != nil {
		t.Fatalf("UpsertInit: %v", err)
	}
	g, err := LoadGraph(db, "exec-1")
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	if len(g.Stacks) != 2 || len(g.Edges) != 1 {
		t.Fatalf("graph = %d stacks, %d edges; want 2,1", len(g.Stacks), len(g.Edges))
	}
	if g.Stacks[0].Path != "stacks/a" || g.Stacks[0].Status != events.StatusPending {
		t.Errorf("stack a = %+v; want path stacks/a, status pending", g.Stacks[0])
	}
	if g.Stacks[1].Status != events.StatusRunning {
		t.Errorf("stack b status = %q; want running", g.Stacks[1].Status)
	}
	if g.Edges[0] != (events.Edge{From: "stacks/a", To: "stacks/b"}) {
		t.Errorf("edge = %+v", g.Edges[0])
	}
}

func TestGetExecution(t *testing.T) {
	db := newTestDB(t)
	if err := UpsertInit(db, sampleInit()); err != nil {
		t.Fatal(err)
	}
	e, err := GetExecution(db, "exec-1")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if e.Repo != "owner/repo" || e.PR != 42 || e.Environment != "staging" {
		t.Errorf("execution = %+v", e)
	}
	if e.StatusContext != "iam/staging" {
		t.Errorf("status context = %q", e.StatusContext)
	}
}

func TestUpsertPhaseDoesNotClobberIdentity(t *testing.T) {
	db := newTestDB(t)
	if err := UpsertInit(db, sampleInit()); err != nil {
		t.Fatal(err)
	}
	if err := UpsertPhase(db, events.PhaseEvent{ID: "exec-1", Phase: events.PhasePlanning}); err != nil {
		t.Fatalf("UpsertPhase: %v", err)
	}
	e, err := GetExecution(db, "exec-1")
	if err != nil {
		t.Fatal(err)
	}
	if e.Repo != "owner/repo" || e.PR != 42 || e.Environment != "staging" {
		t.Errorf("phase bump clobbered identity: %+v", e)
	}
	if e.Phase != string(events.PhasePlanning) {
		t.Errorf("phase = %q; want planning", e.Phase)
	}
}

func TestUpsertPhaseBeforeInit(t *testing.T) {
	db := newTestDB(t)
	if err := UpsertPhase(db, events.PhaseEvent{ID: "exec-9", Phase: events.PhaseWarming}); err != nil {
		t.Fatalf("UpsertPhase: %v", err)
	}
	in := sampleInit()
	in.ID = "exec-9"
	if err := UpsertInit(db, in); err != nil {
		t.Fatalf("UpsertInit after phase: %v", err)
	}
	e, err := GetExecution(db, "exec-9")
	if err != nil {
		t.Fatal(err)
	}
	if e.Repo != "owner/repo" || e.Phase != string(events.PhaseWarming) {
		t.Errorf("converged row = %+v; want repo set and phase warming preserved", e)
	}
}

func TestUpsertInitIsReRunnable(t *testing.T) {
	db := newTestDB(t)
	if err := UpsertInit(db, sampleInit()); err != nil {
		t.Fatal(err)
	}
	// A tick advances stack a past pending.
	if err := UpdateStack(db, "exec-1", "stacks/a", events.StatusFailed, "boom"); err != nil {
		t.Fatal(err)
	}
	// Re-running the same Init is safe: identity intact, no duplicate edges, and
	// the advanced stack status is preserved (not regressed back to pending).
	if err := UpsertInit(db, sampleInit()); err != nil {
		t.Fatalf("re-init: %v", err)
	}
	g, err := LoadGraph(db, "exec-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Stacks) != 2 || len(g.Edges) != 1 {
		t.Fatalf("re-init graph = %d stacks, %d edges; want 2,1 (no duplicates)", len(g.Stacks), len(g.Edges))
	}
	if g.Stacks[0].Path != "stacks/a" || g.Stacks[0].Status != events.StatusFailed {
		t.Errorf("re-init stack a = %+v; want status preserved as failed", g.Stacks[0])
	}
	e, err := GetExecution(db, "exec-1")
	if err != nil {
		t.Fatal(err)
	}
	if e.Repo != "owner/repo" || e.PR != 42 || e.Environment != "staging" {
		t.Errorf("re-init clobbered identity: %+v", e)
	}
}

func TestListExecutions(t *testing.T) {
	db := newTestDB(t)
	for _, in := range []events.Init{
		{ID: "e1", Repo: "o/r", PR: 1, Environment: "staging"},
		{ID: "e2", Repo: "o/r", PR: 2, Environment: "prod"},
		{ID: "e3", Repo: "o/r", PR: 1, Environment: "staging"},
	} {
		if err := UpsertInit(db, in); err != nil {
			t.Fatal(err)
		}
	}

	all, err := ListExecutions(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("ListExecutions = %d, want 3", len(all))
	}
	lim, _ := ListExecutions(db, 2)
	if len(lim) != 2 {
		t.Errorf("ListExecutions(2) = %d, want 2", len(lim))
	}

	pr1, err := ListExecutionsForPR(db, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr1) != 2 {
		t.Fatalf("ListExecutionsForPR(1) = %d, want 2", len(pr1))
	}
	for _, e := range pr1 {
		if e.PR != 1 {
			t.Errorf("PR filter leaked execution %s (pr=%d)", e.ID, e.PR)
		}
		if e.Repo != "o/r" {
			t.Errorf("row missing repo: %+v", e)
		}
	}
}

func TestLatestVerifyExecutionID(t *testing.T) {
	db := newTestDB(t)
	_ = UpsertInit(db, events.Init{ID: "plan-1", Repo: "o/r", PR: 7, Environment: "staging", Context: "plan/staging"})
	_ = UpsertInit(db, events.Init{ID: "verify-1", Repo: "o/r", PR: 7, Environment: "staging", Context: "verify/staging"})
	_ = UpsertInit(db, events.Init{ID: "verify-2", Repo: "o/r", PR: 7, Environment: "staging", Context: "verify/staging"})

	id, ok := LatestVerifyExecutionID(db, 7, "staging")
	if !ok {
		t.Fatal("expected a verify execution")
	}
	if id != "verify-1" && id != "verify-2" {
		t.Errorf("latest verify = %q, want a verify-* id (not the plan run)", id)
	}

	if _, ok := LatestVerifyExecutionID(db, 99, "staging"); ok {
		t.Error("no verify run for PR 99 — ok should be false")
	}
}

func TestLoadGraphSurfacesCounts(t *testing.T) {
	db := newTestDB(t)
	if err := UpsertInit(db, events.Init{ID: "e1", Stacks: []events.StackState{{Path: "a"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE stacks SET counts = ? WHERE execution_id = ? AND stack_path = ?`,
		`{"add":6,"change":2}`, "e1", "a"); err != nil {
		t.Fatal(err)
	}
	g, err := LoadGraph(db, "e1")
	if err != nil {
		t.Fatal(err)
	}
	if g.Stacks[0].Counts == nil || g.Stacks[0].Counts.Add != 6 || g.Stacks[0].Counts.Change != 2 {
		t.Fatalf("LoadGraph must surface counts, got %+v", g.Stacks[0].Counts)
	}
}

func TestUpsertInitPreservesStatus(t *testing.T) {
	db := newTestDB(t)
	in := events.Init{ID: "e1", Stacks: []events.StackState{{Path: "a", Status: events.StatusPending}}}
	if err := UpsertInit(db, in); err != nil {
		t.Fatal(err)
	}
	if err := UpdateStack(db, "e1", "a", events.StatusPlanned, ""); err != nil {
		t.Fatal(err)
	}
	// A second Init (e.g. run register then run plan) must not regress the stack.
	if err := UpsertInit(db, in); err != nil {
		t.Fatal(err)
	}
	g, err := LoadGraph(db, "e1")
	if err != nil {
		t.Fatal(err)
	}
	if g.Stacks[0].Status != events.StatusPlanned {
		t.Fatalf("status after re-Init = %q, want planned", g.Stacks[0].Status)
	}
}

func TestSetExecutionStatus(t *testing.T) {
	db := newTestDB(t) // use the package's existing test-db helper
	if err := UpsertInit(db, events.Init{ID: "e1", Repo: "r", Context: "apply/prod",
		Stacks: []events.StackState{{Path: "a", Status: events.StatusPending}}}); err != nil {
		t.Fatal(err)
	}
	if err := SetExecutionStatus(db, "e1", "success"); err != nil {
		t.Fatal(err)
	}
	e, err := GetExecution(db, "e1")
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != "success" {
		t.Fatalf("status = %q, want success", e.Status)
	}
}

func TestUpdateStackAndReportAndRev(t *testing.T) {
	db := newTestDB(t)
	if err := UpsertInit(db, sampleInit()); err != nil {
		t.Fatal(err)
	}
	if err := UpdateStack(db, "exec-1", "stacks/a", events.StatusFailed, "boom"); err != nil {
		t.Fatalf("UpdateStack: %v", err)
	}
	g, _ := LoadGraph(db, "exec-1")
	if g.Stacks[0].Status != events.StatusFailed || g.Stacks[0].Detail != "boom" {
		t.Errorf("stack a = %+v; want failed/boom", g.Stacks[0])
	}
	if err := SetReport(db, "exec-1", "# report"); err != nil {
		t.Fatal(err)
	}
	if err := BumpRev(db, "exec-1"); err != nil {
		t.Fatal(err)
	}
	if err := SetCheckRunID(db, "exec-1", 12345); err != nil {
		t.Fatal(err)
	}
	e, _ := GetExecution(db, "exec-1")
	if e.ReportMarkdown != "# report" || e.Rev != 1 || !e.CheckRunID.Valid || e.CheckRunID.Int64 != 12345 {
		t.Errorf("execution after writes = %+v", e)
	}
}

func TestFindAndSupersedeExecution(t *testing.T) {
	db := newTestDB(t)
	exec1 := sampleInit()
	exec1.ID = "exec-1"
	if err := UpsertInit(db, exec1); err != nil {
		t.Fatalf("UpsertInit exec1: %v", err)
	}

	// Lookup should find nothing yet (same incoming ID)
	_, found, err := FindNonSupersededExecution(db, exec1.PR, exec1.Environment, exec1.SHA, exec1.Context, "exec-1")
	if err != nil {
		t.Fatalf("FindNonSupersededExecution: %v", err)
	}
	if found {
		t.Errorf("found non-superseded execution prematurely")
	}

	exec2 := sampleInit()
	exec2.ID = "exec-2"
	if err := UpsertInit(db, exec2); err != nil {
		t.Fatalf("UpsertInit exec2: %v", err)
	}

	// Lookup from exec-2 perspective should find exec-1
	oldID, found, err := FindNonSupersededExecution(db, exec2.PR, exec2.Environment, exec2.SHA, exec2.Context, exec2.ID)
	if err != nil {
		t.Fatalf("FindNonSupersededExecution: %v", err)
	}
	if !found || oldID != "exec-1" {
		t.Errorf("FindNonSupersededExecution = %q, %t; want exec-1, true", oldID, found)
	}

	// Supersede old
	if err := SupersedeExecution(db, "exec-1", "exec-2"); err != nil {
		t.Fatalf("SupersedeExecution: %v", err)
	}

	// Verify it was marked
	e1, err := GetExecution(db, "exec-1")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if e1.SupersededBy != "exec-2" {
		t.Errorf("e1.SupersededBy = %q; want exec-2", e1.SupersededBy)
	}

	// Lookup should now be empty (since exec-1 is superseded, and exec-2 is the incoming ID)
	_, found, err = FindNonSupersededExecution(db, exec2.PR, exec2.Environment, exec2.SHA, exec2.Context, "exec-2")
	if err != nil {
		t.Fatalf("FindNonSupersededExecution: %v", err)
	}
	if found {
		t.Errorf("should not find superseded execution")
	}
}

func TestLatestExecutionID(t *testing.T) {
	db := newTestDB(t)

	// Verify on empty DB
	if _, ok := LatestExecutionID(db, 7, "prod"); ok {
		t.Errorf("expected ok = false on empty DB")
	}

	// Insert one execution
	exec1 := sampleInit()
	exec1.ID = "exec-latest-1"
	exec1.PR = 7
	exec1.Environment = "prod"
	if err := UpsertInit(db, exec1); err != nil {
		t.Fatal(err)
	}

	id, ok := LatestExecutionID(db, 7, "prod")
	if !ok || id != "exec-latest-1" {
		t.Errorf("LatestExecutionID = %q, ok=%t, want exec-latest-1, true", id, ok)
	}
}

func TestEnvironmentsForPR(t *testing.T) {
	db := newTestDB(t)

	// Insert two executions for the same PR with different environments
	exec1 := sampleInit()
	exec1.ID = "exec-env-1"
	exec1.PR = 12
	exec1.Environment = "prod"
	if err := UpsertInit(db, exec1); err != nil {
		t.Fatal(err)
	}

	exec2 := sampleInit()
	exec2.ID = "exec-env-2"
	exec2.PR = 12
	exec2.Environment = "staging"
	if err := UpsertInit(db, exec2); err != nil {
		t.Fatal(err)
	}

	envs, err := EnvironmentsForPR(db, 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 2 {
		t.Fatalf("expected 2 environments, got %v", envs)
	}

	// Check content
	foundProd, foundStaging := false, false
	for _, env := range envs {
		if env == "prod" {
			foundProd = true
		}
		if env == "staging" {
			foundStaging = true
		}
	}
	if !foundProd || !foundStaging {
		t.Errorf("expected environments to contain both prod and staging, got %v", envs)
	}
}

func TestFindExecutionBySHA(t *testing.T) {
	db := newTestDB(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(UpsertInit(db, events.Init{ID: "e-serve", Repo: "o/r", SHA: "sha1", PR: 9, Environment: "nonprod", Context: "plan/nonprod"}))
	must(UpsertInit(db, events.Init{ID: "e-noprfoo", Repo: "o/r", SHA: "sha1", PR: 0, Environment: "nonprod", Context: "plan/nonprod"}))

	id, ok, err := FindExecutionBySHA(db, "nonprod", "plan/nonprod", "sha1")
	must(err)
	if !ok || id != "e-serve" {
		t.Fatalf("got (%q,%v), want (e-serve,true)", id, ok)
	}

	// Superseded rows are excluded.
	must(SupersedeExecution(db, "e-serve", "e-newer"))
	_, ok, err = FindExecutionBySHA(db, "nonprod", "plan/nonprod", "sha1")
	must(err)
	if ok {
		t.Fatal("superseded execution should not be returned")
	}

	// Miss returns ("", false, nil).
	if id, ok, err := FindExecutionBySHA(db, "nonprod", "plan/nonprod", "absent"); err != nil || ok || id != "" {
		t.Fatalf("miss = (%q,%v,%v)", id, ok, err)
	}
}
