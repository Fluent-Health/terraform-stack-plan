package server

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// errListGrantsBackend wraps a Backend but fails every ListGrants — simulates an
// unreachable PAM backend so the gate-check's fresh reconcile cannot confirm.
type errListGrantsBackend struct{ approval.Backend }

func (errListGrantsBackend) ListGrants(context.Context, string, string) ([]approval.Grant, error) {
	return nil, fmt.Errorf("pam unreachable")
}

// seedStaleActiveGate records a classified gate with one ACTIVE target — the
// stale cache the gate-check must NOT trust when the live reconcile fails.
func seedStaleActiveGate(t *testing.T, a *App) {
	t.Helper()
	if err := store.MarkClassified(a.db, 7, "staging"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTarget(a.db, 7, "staging", "iam", "proj-a", "g1", "ACTIVE"); err != nil {
		t.Fatal(err)
	}
}

func gateCheckGH() *MockGitHub {
	return &MockGitHub{
		CreateCheckRunFn: func(context.Context, string, string, string, string) (int64, error) { return 1, nil },
		UpdateCheckRunFn: func(context.Context, string, int64, CheckRunUpdate) error { return nil },
	}
}

func TestGateCheckFailsClosedOnReconcileErrorCore(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, gateCheckGH(), Config{ReconcilerCore: true})
	a.Approval = errListGrantsBackend{approval.NewFake()}
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	seedStaleActiveGate(t, a)

	if code := post(t, srv, "/api/gate/check", events.GateCheck{PR: 7, Environment: "staging"}); code != 503 {
		t.Fatalf("core gate/check with failing ListGrants = %d, want 503 (fail-closed)", code)
	}
}

func TestGateCheckFailsClosedOnReconcileErrorLegacy(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, gateCheckGH(), Config{}) // ReconcilerCore off → legacy path
	a.Approval = errListGrantsBackend{approval.NewFake()}
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	seedStaleActiveGate(t, a)

	if code := post(t, srv, "/api/gate/check", events.GateCheck{PR: 7, Environment: "staging"}); code != 503 {
		t.Fatalf("legacy gate/check with failing ListGrants = %d, want 503 (fail-closed)", code)
	}
}
