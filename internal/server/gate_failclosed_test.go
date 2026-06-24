package server

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// errListGrantsBackend wraps a Backend but fails every ListGrants — simulates an
// unreachable PAM backend so the gate-check's fresh reconcile cannot confirm.
type errListGrantsBackend struct{ approval.Backend }

func (errListGrantsBackend) ListGrants(context.Context, string, string) ([]approval.Grant, error) {
	return nil, fmt.Errorf("pam unreachable")
}

// seedGateViaHandle drives a RunnerFinalize signal through the shell to land a
// pending gate for PR 7 / staging in the gate_targets projection. This is the
// production path that populates gate_targets; the gate-check handler then
// performs a live reconcile on top of that stored state.
func seedGateViaHandle(t *testing.T, a *App) {
	t.Helper()
	if err := store.UpsertInit(a.db, events.Init{ID: "e1", PR: 7, Environment: "staging", Repo: "r"}); err != nil {
		t.Fatal(err)
	}
	// Use a real Fake backend (with no pool) so RequestGrant is skipped but the
	// gate state (Pending) is persisted and the projection is written.
	if err := a.shell.Handle(context.Background(), 7, "staging", "r", reconcile.RunnerFinalize{
		Gates: []events.GateTarget{{Class: "iam", Target: "proj-a"}},
	}); err != nil {
		t.Fatal(err)
	}
}

func gateCheckGH() *MockGitHub {
	return &MockGitHub{
		CreateCheckRunFn: func(context.Context, string, string, string, string) (int64, error) { return 1, nil },
		UpdateCheckRunFn: func(context.Context, string, int64, CheckRunUpdate) error { return nil },
	}
}

func TestGateCheckFailsClosedOnReconcileError(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, gateCheckGH(), Config{})
	// Seed the gate via the event path so gate_targets has targets for the check.
	a.Approval = approval.NewFake()
	seedGateViaHandle(t, a)
	// Now replace the backend with one that always fails ListGrants.
	a.Approval = errListGrantsBackend{approval.NewFake()}

	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	if code := post(t, srv, "/api/gate/check", events.GateCheck{PR: 7, Environment: "staging"}); code != 503 {
		t.Fatalf("gate/check with failing ListGrants = %d, want 503 (fail-closed)", code)
	}
}
