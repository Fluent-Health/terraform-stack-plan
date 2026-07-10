package server

import (
	"context"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func TestAdoptRunMaterializesWithoutExecutor(t *testing.T) {
	a, fe, _ := newRunTriggerApp(t)
	cs := reconcile.ChangeSet{PR: 21, Environment: "nonprod"}

	a.shell.adoptRun(context.Background(), cs, "o/r", reconcile.AdoptRun{
		Kind: reconcile.RunKindPlan, ExecutionID: "run-21-nonprod-plan-abc-a2",
		SHA: "abcsha", BuildRef: "build-rerun",
	})

	// Execution row + check run exist; the executor was never called.
	e, err := store.GetExecution(a.db, "run-21-nonprod-plan-abc-a2")
	if err != nil {
		t.Fatalf("adopted execution missing: %v", err)
	}
	if e.PR != 21 || e.SHA != "abcsha" || e.StatusContext != "plan/nonprod" {
		t.Errorf("execution = %+v", e)
	}
	if !e.CheckRunID.Valid || e.CheckRunID.Int64 == 0 {
		t.Error("adopted execution has no check run")
	}
	if len(fe.starts) != 0 {
		t.Errorf("executor.Start called during adopt: %+v", fe.starts)
	}
}
