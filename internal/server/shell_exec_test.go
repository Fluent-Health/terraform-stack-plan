package server

import (
	"context"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/execution"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func TestHandleExecProjectsFullLifecycle(t *testing.T) {
	sh := newTestShell(t)
	ctx := context.Background()
	id := "run-7-nonprod-plan-abc-a1"

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(sh.HandleExec(ctx, id, execution.ReportInit{Exec: execution.State{
		ID: id, PR: 7, Environment: "nonprod", Context: "terraform/nonprod", Repo: "r", SHA: "abc",
		Stacks: []execution.Stack{{Path: "a"}, {Path: "b"}}, Edges: []execution.Edge{{From: "a", To: "b"}},
	}}))
	must(sh.HandleExec(ctx, id, execution.ReportPhase{ID: id, Phase: events.PhaseApplying, Label: "applying"}))
	must(sh.HandleExec(ctx, id, execution.ReportTick{Stack: "a", Status: events.StatusRunning}))
	must(sh.HandleExec(ctx, id, execution.ReportAnnotate{Projects: map[string]string{"a": "proj-a"}}))
	must(sh.HandleExec(ctx, id, execution.ReportSucceed{}))

	e, err := store.GetExecution(sh.app.db, id)
	must(err)
	if e.PR != 7 || e.Environment != "nonprod" || e.StatusContext != "terraform/nonprod" {
		t.Fatalf("identity not projected: %#v", e)
	}
	if e.Phase != "applying" || e.Status != "success" {
		t.Fatalf("phase/status not projected: phase=%q status=%q", e.Phase, e.Status)
	}
	g, err := store.LoadGraph(sh.app.db, id)
	must(err)
	if len(g.Stacks) != 2 || len(g.Edges) != 1 {
		t.Fatalf("graph not projected: %d stacks, %d edges", len(g.Stacks), len(g.Edges))
	}
	if g.Stacks[0].Project != "proj-a" || g.Stacks[0].Status != events.StatusRunning {
		t.Fatalf("stack a not projected: %#v", g.Stacks[0])
	}
	ph, err := store.PhasesFor(sh.app.db, id)
	must(err)
	if len(ph) != 1 || ph[0].Phase != "applying" {
		t.Fatalf("phase history not appended: %#v", ph)
	}

	// Replay proves the stream (not just the projection) is the source of truth.
	st, _, err := sh.app.execDecider.Load(sh.app.eventStore, runStreamID(id))
	must(err)
	if st.Status != "success" || st.PR != 7 || len(st.Stacks) != 2 {
		t.Fatalf("replayed state mismatch: %#v", st)
	}
}
