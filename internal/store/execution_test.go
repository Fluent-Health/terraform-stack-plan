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
