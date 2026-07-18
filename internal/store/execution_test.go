package store

import (
	"database/sql"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// sampleExec returns a (ProjectedExecution, []ProjectedStack, [][2]string)
// triple mirroring the shape shell.projectExecution writes from a folded
// execution.State — the only path executions/stacks/edges rows are written
// through now (UpsertInit/UpsertPhase/UpdateStack/SetExecutionStatus were the
// legacy direct-write authority; the execution aggregate + these Project*
// functions replaced it, see internal/execution and shell_exec.go).
func sampleExec() (ProjectedExecution, []ProjectedStack, [][2]string) {
	e := ProjectedExecution{
		ID: "exec-1", Repo: "owner/repo", SHA: "abc123", PR: 42,
		Environment: "staging", LogURL: "https://ci/log", Context: "iam/staging",
		Status: "in_progress",
	}
	stacks := []ProjectedStack{
		{Path: "stacks/a", Project: "proj-a", Status: events.StatusPending},
		{Path: "stacks/b", Project: "proj-b", Status: events.StatusRunning},
	}
	edges := [][2]string{{"stacks/a", "stacks/b"}}
	return e, stacks, edges
}

// seedExec writes an execution + its stacks/edges in one transaction, mirroring
// shell.projectExecution's write pattern.
func seedExec(t *testing.T, db *sql.DB, e ProjectedExecution, stacks []ProjectedStack, edges [][2]string) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := ProjectExecutionRow(tx, e); err != nil {
		tx.Rollback()
		t.Fatalf("ProjectExecutionRow: %v", err)
	}
	for _, s := range stacks {
		if err := ProjectStack(tx, e.ID, s); err != nil {
			tx.Rollback()
			t.Fatalf("ProjectStack: %v", err)
		}
	}
	for _, ed := range edges {
		if err := ProjectEdge(tx, e.ID, ed[0], ed[1]); err != nil {
			tx.Rollback()
			t.Fatalf("ProjectEdge: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// mustProjectStack projects a single stack update in its own transaction — a
// convenience for tests that only need to tick one stack after the initial seed.
func mustProjectStack(t *testing.T, db *sql.DB, execID string, s ProjectedStack) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := ProjectStack(tx, execID, s); err != nil {
		tx.Rollback()
		t.Fatalf("ProjectStack: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadGraphAfterProjection(t *testing.T) {
	db := newTestDB(t)
	e, stacks, edges := sampleExec()
	seedExec(t, db, e, stacks, edges)

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
	e, stacks, edges := sampleExec()
	seedExec(t, db, e, stacks, edges)

	got, err := GetExecution(db, "exec-1")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.Repo != "owner/repo" || got.PR != 42 || got.Environment != "staging" {
		t.Errorf("execution = %+v", got)
	}
	if got.StatusContext != "iam/staging" {
		t.Errorf("status context = %q", got.StatusContext)
	}
}

func TestListExecutions(t *testing.T) {
	db := newTestDB(t)
	for _, e := range []ProjectedExecution{
		{ID: "e1", Repo: "o/r", PR: 1, Environment: "staging"},
		{ID: "e2", Repo: "o/r", PR: 2, Environment: "prod"},
		{ID: "e3", Repo: "o/r", PR: 1, Environment: "staging"},
	} {
		seedExec(t, db, e, nil, nil)
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
	seedExec(t, db, ProjectedExecution{ID: "plan-1", Repo: "o/r", PR: 7, Environment: "staging", Context: "plan/staging"}, nil, nil)
	seedExec(t, db, ProjectedExecution{ID: "verify-1", Repo: "o/r", PR: 7, Environment: "staging", Context: "verify/staging"}, nil, nil)
	seedExec(t, db, ProjectedExecution{ID: "verify-2", Repo: "o/r", PR: 7, Environment: "staging", Context: "verify/staging"}, nil, nil)

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
	seedExec(t, db, ProjectedExecution{ID: "e1"}, []ProjectedStack{{Path: "a"}}, nil)
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

// TestReviveExecutionResetsSupersededAndStatus: a fresh Init means the id's
// runner is alive again — ReviveExecution resets a terminal/superseded row back
// to in_progress with superseded_by cleared. This replaced the reset the legacy
// UpsertInit performed atomically inside its own INSERT ... ON CONFLICT clause
// (see ReviveExecution's doc comment).
func TestReviveExecutionResetsSupersededAndStatus(t *testing.T) {
	db := newTestDB(t)
	seedExec(t, db, ProjectedExecution{ID: "e1", Repo: "o/r", Environment: "prod", Context: "apply/prod", Status: "in_progress"}, nil, nil)

	// Mark terminal + superseded (as a finished, replaced execution would be).
	seedExec(t, db, ProjectedExecution{ID: "e1", Status: "failure"}, nil, nil)
	if err := SupersedeExecution(db, "e1", "e-newer"); err != nil {
		t.Fatal(err)
	}
	old, err := GetExecution(db, "e1")
	if err != nil {
		t.Fatal(err)
	}
	if old.Status != "failure" || old.SupersededBy != "e-newer" {
		t.Fatalf("unexpected pre-state: %+v", old)
	}

	if err := ReviveExecution(db, "e1"); err != nil {
		t.Fatal(err)
	}

	updated, err := GetExecution(db, "e1")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "in_progress" {
		t.Errorf("status = %q, want in_progress", updated.Status)
	}
	if updated.SupersededBy != "" {
		t.Errorf("superseded_by = %q, want empty", updated.SupersededBy)
	}
}

func TestReportRevAndCheckRunID(t *testing.T) {
	db := newTestDB(t)
	e, stacks, edges := sampleExec()
	seedExec(t, db, e, stacks, edges)

	mustProjectStack(t, db, "exec-1", ProjectedStack{Path: "stacks/a", Status: events.StatusFailed, Detail: "boom"})
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
	got, _ := GetExecution(db, "exec-1")
	if got.ReportMarkdown != "# report" || got.Rev != 1 || !got.CheckRunID.Valid || got.CheckRunID.Int64 != 12345 {
		t.Errorf("execution after writes = %+v", got)
	}
}

func TestFindAndSupersedeExecution(t *testing.T) {
	db := newTestDB(t)
	exec1, stacks1, edges1 := sampleExec()
	seedExec(t, db, exec1, stacks1, edges1)

	// Lookup should find nothing yet (same incoming ID)
	_, found, err := FindNonSupersededExecution(db, exec1.PR, exec1.Environment, exec1.SHA, exec1.Context, "exec-1")
	if err != nil {
		t.Fatalf("FindNonSupersededExecution: %v", err)
	}
	if found {
		t.Errorf("found non-superseded execution prematurely")
	}

	exec2, stacks2, edges2 := sampleExec()
	exec2.ID = "exec-2"
	seedExec(t, db, exec2, stacks2, edges2)

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
	exec1, stacks1, edges1 := sampleExec()
	exec1.ID = "exec-latest-1"
	exec1.PR = 7
	exec1.Environment = "prod"
	seedExec(t, db, exec1, stacks1, edges1)

	id, ok := LatestExecutionID(db, 7, "prod")
	if !ok || id != "exec-latest-1" {
		t.Errorf("LatestExecutionID = %q, ok=%t, want exec-latest-1, true", id, ok)
	}
}

func TestEnvironmentsForPR(t *testing.T) {
	db := newTestDB(t)

	// Insert two executions for the same PR with different environments
	exec1, stacks1, edges1 := sampleExec()
	exec1.ID = "exec-env-1"
	exec1.PR = 12
	exec1.Environment = "prod"
	seedExec(t, db, exec1, stacks1, edges1)

	exec2, stacks2, edges2 := sampleExec()
	exec2.ID = "exec-env-2"
	exec2.PR = 12
	exec2.Environment = "staging"
	seedExec(t, db, exec2, stacks2, edges2)

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

func TestPhasesForOrdersOldestFirst(t *testing.T) {
	db := newTestDB(t)
	e, stacks, edges := sampleExec()
	seedExec(t, db, e, stacks, edges)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, ph := range []string{"warming", "planning", "report"} {
		if err := AppendPhaseHistory(tx, "exec-1", ph, "", nil); err != nil {
			t.Fatalf("AppendPhaseHistory %s: %v", ph, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	rows, err := PhasesFor(db, "exec-1")
	if err != nil {
		t.Fatalf("PhasesFor: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("history len = %d; want 3", len(rows))
	}
	got := []string{rows[0].Phase, rows[1].Phase, rows[2].Phase}
	want := []string{"warming", "planning", "report"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("history[%d] = %q; want %q", i, got[i], want[i])
		}
	}
}

func TestPhasesForEmpty(t *testing.T) {
	db := newTestDB(t)
	rows, err := PhasesFor(db, "nope")
	if err != nil {
		t.Fatalf("PhasesFor: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("len = %d; want 0", len(rows))
	}
}

func TestFindExecutionBySHA(t *testing.T) {
	db := newTestDB(t)
	seedExec(t, db, ProjectedExecution{ID: "e-serve", Repo: "o/r", SHA: "sha1", PR: 9, Environment: "nonprod", Context: "plan/nonprod"}, nil, nil)
	seedExec(t, db, ProjectedExecution{ID: "e-noprfoo", Repo: "o/r", SHA: "sha1", PR: 0, Environment: "nonprod", Context: "plan/nonprod"}, nil, nil)

	id, ok, err := FindExecutionBySHA(db, "nonprod", "plan/nonprod", "sha1")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || id != "e-serve" {
		t.Fatalf("got (%q,%v), want (e-serve,true)", id, ok)
	}

	// Superseded rows are excluded.
	if err := SupersedeExecution(db, "e-serve", "e-newer"); err != nil {
		t.Fatal(err)
	}
	_, ok, err = FindExecutionBySHA(db, "nonprod", "plan/nonprod", "sha1")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("superseded execution should not be returned")
	}

	// Miss returns ("", false, nil).
	if id, ok, err := FindExecutionBySHA(db, "nonprod", "plan/nonprod", "absent"); err != nil || ok || id != "" {
		t.Fatalf("miss = (%q,%v,%v)", id, ok, err)
	}
}

func TestProjectExecutionRowOwnedColumnsOnly(t *testing.T) {
	db := newTestDB(t)
	tx, _ := db.Begin()
	// Seed a row with a non-owned column set (report_markdown). repo/sha are
	// NOT NULL in the schema, so the seed must supply placeholder values for
	// them (they're owned columns and get overwritten by the projection below).
	_, err := tx.Exec(`INSERT INTO executions (id, repo, sha, status, report_markdown) VALUES ('e1','seed-repo','seed-sha','in_progress','REPORT')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := ProjectExecutionRow(tx, ProjectedExecution{
		ID: "e1", Repo: "r", SHA: "abc", PR: 7, Environment: "nonprod",
		Context: "terraform/nonprod", Phase: "applying", Status: "success",
	}); err != nil {
		t.Fatal(err)
	}
	tx.Commit()
	e, err := GetExecution(db, "e1")
	if err != nil {
		t.Fatal(err)
	}
	if e.Repo != "r" || e.PR != 7 || e.Status != "success" || e.Phase != "applying" {
		t.Fatalf("owned columns not written: %#v", e)
	}
	if e.ReportMarkdown != "REPORT" {
		t.Fatalf("non-owned report_markdown clobbered: %q", e.ReportMarkdown)
	}
}

func TestProjectStackEdgeAndPhaseHistoryRoundTrip(t *testing.T) {
	db := newTestDB(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO executions (id, repo, sha) VALUES ('e1','r','sha1')`); err != nil {
		t.Fatal(err)
	}
	counts := &events.Counts{Add: 3, Change: 1}
	if err := ProjectStack(tx, "e1", ProjectedStack{
		Path: "stacks/a", Project: "proj-a", Status: events.StatusRunning,
		Detail: "running now", Categories: []events.Category{{Name: "iam"}}, Counts: counts,
	}); err != nil {
		t.Fatalf("ProjectStack: %v", err)
	}
	if err := ProjectStack(tx, "e1", ProjectedStack{Path: "stacks/b", Project: "proj-b", Status: events.StatusPending}); err != nil {
		t.Fatalf("ProjectStack: %v", err)
	}
	if err := ProjectEdge(tx, "e1", "stacks/a", "stacks/b"); err != nil {
		t.Fatalf("ProjectEdge: %v", err)
	}
	// Duplicate edge insert must be ignored, not error.
	if err := ProjectEdge(tx, "e1", "stacks/a", "stacks/b"); err != nil {
		t.Fatalf("ProjectEdge dup: %v", err)
	}
	pct := 50
	if err := AppendPhaseHistory(tx, "e1", "applying", "applying stacks...", &pct); err != nil {
		t.Fatalf("AppendPhaseHistory: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	g, err := LoadGraph(db, "e1")
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Stacks) != 2 || len(g.Edges) != 1 {
		t.Fatalf("graph = %d stacks, %d edges; want 2,1", len(g.Stacks), len(g.Edges))
	}
	if g.Stacks[0].Status != events.StatusRunning || g.Stacks[0].Detail != "running now" {
		t.Errorf("stack a = %+v", g.Stacks[0])
	}
	if g.Stacks[0].Counts == nil || g.Stacks[0].Counts.Add != 3 {
		t.Errorf("stack a counts = %+v", g.Stacks[0].Counts)
	}
	if len(g.Stacks[0].Categories) != 1 || g.Stacks[0].Categories[0].Name != "iam" {
		t.Errorf("stack a categories = %+v", g.Stacks[0].Categories)
	}

	rows, err := PhasesFor(db, "e1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Phase != "applying" || rows[0].Label != "applying stacks..." {
		t.Errorf("phase history = %+v", rows)
	}
}
