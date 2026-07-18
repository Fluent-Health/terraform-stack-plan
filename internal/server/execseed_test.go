package server

import (
	"context"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/execution"
)

// This file collects test-only seeding helpers that drive the execution
// aggregate the same way production does (handleInit/handlePhase/handleFinalize
// in handlers.go), replacing the retired direct-write store functions
// (UpsertInit/UpsertPhase/UpdateStack/SetExecutionStatus). Those functions wrote
// straight to the executions/stacks tables, bypassing the event-sourced
// execution aggregate (internal/execution) that is now the sole write path —
// so test fixtures must go through the same HandleExec seam the handlers use,
// or the event stream and its projection would disagree.

// seedInit drives an Init report through the execution aggregate + projects it
// (mirrors handleInit's HandleExec call).
func seedInit(t *testing.T, sh *Shell, in events.Init) {
	t.Helper()
	if err := sh.HandleExec(context.Background(), in.ID, execution.ReportInit{Exec: execInitFromEvents(in)}); err != nil {
		t.Fatalf("seedInit %s: %v", in.ID, err)
	}
}

// seedPhase drives a Phase report through the execution aggregate (mirrors
// handlePhase's HandleExec call).
func seedPhase(t *testing.T, sh *Shell, p events.PhaseEvent) {
	t.Helper()
	if err := sh.HandleExec(context.Background(), p.ID, execution.ReportPhase{
		ID: p.ID, Phase: p.Phase, Label: p.Label, Pct: p.ProgressPct,
		Repo: p.Repo, SHA: p.SHA, PR: p.PR, Environment: p.Environment,
		Context: p.Context, LogURL: p.LogURL,
	}); err != nil {
		t.Fatalf("seedPhase %s: %v", p.ID, err)
	}
}

// seedTerminalStatus force-concludes an execution through the execution
// aggregate's terminal signals (mirrors handleFinalize's ReportSucceed/
// ReportFail). Only "success"/"failure" are meaningful terminal outcomes; a
// row is "in_progress" from ReportInit onward, so reviving one back to
// in_progress is done by re-init through seedInit (HandleExec's Started fold),
// not this helper.
func seedTerminalStatus(t *testing.T, sh *Shell, id, status string) {
	t.Helper()
	var sig execution.Signal
	switch status {
	case "success":
		sig = execution.ReportSucceed{}
	case "failure":
		sig = execution.ReportFail{}
	default:
		t.Fatalf("seedTerminalStatus %s: unsupported status %q", id, status)
	}
	if err := sh.HandleExec(context.Background(), id, sig); err != nil {
		t.Fatalf("seedTerminalStatus %s: %v", id, err)
	}
}
