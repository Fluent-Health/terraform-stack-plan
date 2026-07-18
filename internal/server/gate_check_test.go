package server

// gate_check_test.go — Phase 4b Task 3: four explicit gate-check cases against
// the event-sourced (replayed) gate state. The harness mirrors gate_test.go:
// New + httptest.NewServer + post/postResp.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
)

// TestGateCheckNeverPlannedFailsClosed verifies that a PR with no event stream
// at all (never planned) returns 409 GateNotClassified — not 503.
func TestGateCheckNeverPlannedFailsClosed(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})
	a.Approval = approval.NewFake()
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	if code := post(t, srv, "/api/gate/check", events.GateCheck{PR: 7, Environment: "nonprod"}); code != http.StatusConflict {
		t.Fatalf("status = %d; want 409 (never planned)", code)
	}
}

// TestGateCheckCleanPlannedPasses verifies that a clean finalize (no gate
// targets) causes gate/check to return 200 with an empty requester.
func TestGateCheckCleanPlannedPasses(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})
	a.Approval = approval.NewFake()
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	// A clean finalize establishes a classified, zero-gate stream.
	if err := a.shell.Handle(context.Background(), 7, "nonprod", "repo",
		reconcile.RunnerFinalize{}); err != nil {
		t.Fatalf("Handle finalize: %v", err)
	}

	if code := post(t, srv, "/api/gate/check", events.GateCheck{PR: 7, Environment: "nonprod"}); code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (clean planned)", code)
	}
}

// TestGateCheckGatedNotActiveFails verifies that a gated-but-unapproved PR
// returns 409 GateNotSatisfied.
func TestGateCheckGatedNotActiveFails(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})
	a.Approval = approval.NewFake()
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	// Gated finalize: one IAM target, unapproved.
	if err := a.shell.Handle(context.Background(), 7, "nonprod", "repo",
		reconcile.RunnerFinalize{Gates: []events.GateTarget{{Class: "iam", Target: "proj-a"}}}); err != nil {
		t.Fatalf("Handle finalize: %v", err)
	}

	if code := post(t, srv, "/api/gate/check", events.GateCheck{PR: 7, Environment: "nonprod"}); code != http.StatusConflict {
		t.Fatalf("status = %d; want 409 (gated, not approved)", code)
	}
}

// TestGateCheckAllActiveReturnsRequester verifies that after approval and
// reconcile, gate/check returns 200 with the leased requester SA.
func TestGateCheckAllActiveReturnsRequester(t *testing.T) {
	db := newServerTestDB(t)
	fake := approval.NewFake()
	fake.Pool = []string{"sa0@project.iam.gserviceaccount.com"}
	a := New(db, &MockGitHub{
		CreateCheckRunFn: func(_ context.Context, _, _, _, _ string) (int64, error) { return 1, nil },
		UpdateCheckRunFn: func(_ context.Context, _ string, _ int64, _ CheckRunUpdate) error { return nil },
	}, Config{})
	a.Approval = fake
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	// Gated finalize + approve + reconcile → Satisfied gate.
	if err := a.shell.Handle(context.Background(), 7, "nonprod", "repo",
		reconcile.RunnerFinalize{Gates: []events.GateTarget{{Class: "iam", Target: "proj-a"}}}); err != nil {
		t.Fatalf("Handle finalize: %v", err)
	}
	fake.Approve(approval.Request{Class: "iam", Target: "proj-a", PR: 7, Environment: "nonprod"})
	if err := a.reconcileGate(context.Background(), 7, "nonprod"); err != nil {
		t.Fatalf("reconcileGate: %v", err)
	}

	resp := postResp(t, srv, "/api/gate/check", events.GateCheck{PR: 7, Environment: "nonprod"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200 (all active)", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	want := "sa0@project.iam.gserviceaccount.com"
	if body["requester"] != want {
		t.Errorf("body[requester] = %q; want %q", body["requester"], want)
	}
}

// The gate-reconcile nudge is a fire-and-forget trigger: always 202, even
// with no approval backend configured (the reconcile no-ops).
func TestGateReconcileNudgeAccepts(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/gate/reconcile", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d; want 202", resp.StatusCode)
	}
}
