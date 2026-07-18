package server

import (
	"context"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
)

func newFlowShell(t *testing.T) (*Shell, *approval.Fake) {
	t.Helper()
	app := New(newServerTestDB(t), &MockGitHub{}, Config{})
	fake := approval.NewFake()
	fake.Pool = []string{"sa0"}
	app.Approval = fake
	sh := NewShell(app)
	seedInit(t, sh, events.Init{ID: "e1", PR: 7, Environment: "staging", Repo: "r"})
	return sh, fake
}

func TestHandleApproveThenTickBecomesApplyAllowed(t *testing.T) {
	sh, fake := newFlowShell(t)
	ctx := context.Background()
	if err := sh.Handle(ctx, 7, "staging", "r", reconcile.RunnerFinalize{
		Gates: []events.GateTarget{{Class: "iam", Target: "p1"}},
	}); err != nil {
		t.Fatal(err)
	}
	// Pre-approval: apply must be blocked.
	w0, _ := sh.gather(7, "staging")
	if applyAllowed(w0.Prior.Gate) {
		t.Fatalf("apply should be blocked before approval, gate=%T", w0.Prior.Gate)
	}
	// Approver flips the grant ACTIVE; a full re-list tick promotes to Satisfied.
	fake.Approve(approval.Request{Class: "iam", Target: "p1", PR: 7, Environment: "staging"})
	grants, _ := fake.ListGrants(ctx, "iam", "p1")
	var obs []reconcile.ObservedGrant
	for _, g := range grants {
		if g.Request.PR == 7 && g.Request.Environment == "staging" {
			obs = append(obs, reconcile.ObservedGrant{Class: "iam", Target: "p1", Name: g.Name, State: g.State, Requester: g.Requester})
		}
	}
	if err := sh.Handle(ctx, 7, "staging", "r", reconcile.GateTick{Grants: obs}); err != nil {
		t.Fatal(err)
	}
	w1, _ := sh.gather(7, "staging")
	if !applyAllowed(w1.Prior.Gate) {
		t.Fatalf("apply should be allowed after approval, gate=%T", w1.Prior.Gate)
	}
}

func TestHandlePRClosedRevokesAndBlocks(t *testing.T) {
	sh, fake := newFlowShell(t)
	ctx := context.Background()
	if err := sh.Handle(ctx, 7, "staging", "r", reconcile.RunnerFinalize{
		Gates: []events.GateTarget{{Class: "iam", Target: "p1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := sh.Handle(ctx, 7, "staging", "r", reconcile.PRClosed{}); err != nil {
		t.Fatal(err)
	}
	// Backend grant must be revoked (no open grant remains).
	grants, _ := fake.ListGrants(ctx, "iam", "p1")
	for _, g := range grants {
		if g.State.Open() {
			t.Fatalf("want grant revoked after PRClosed, got %s", g.State)
		}
	}
	// Gate must not permit apply.
	w, _ := sh.gather(7, "staging")
	if applyAllowed(w.Prior.Gate) {
		t.Fatalf("apply must be blocked after PR closed, gate=%T", w.Prior.Gate)
	}
}

// applyAllowed mirrors the fail-closed apply-gate verdict from the deleted
// reconcile.ApplyAllowed helper: only Clean or Satisfied permit apply.
func applyAllowed(g reconcile.GateState) bool {
	switch g.(type) {
	case reconcile.Clean, reconcile.Satisfied:
		return true
	}
	return false
}
