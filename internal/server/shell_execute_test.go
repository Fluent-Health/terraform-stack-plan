package server

import (
	"context"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
)

func TestExecuteRequestGrantYieldsObservedGrant(t *testing.T) {
	app := New(newServerTestDB(t), &MockGitHub{}, Config{})
	app.Approval = approval.NewFake()
	sh := NewShell(app)

	cs := reconcile.ChangeSet{PR: 7, Environment: "staging"}
	obs := sh.execute(context.Background(), cs, "repo", []reconcile.Action{
		reconcile.RequestGrant{Class: "iam", Target: "p1", Requester: ""},
	})
	if len(obs) != 1 || obs[0].Target != "p1" || obs[0].State == "" {
		t.Fatalf("want one observed grant for p1, got %+v", obs)
	}
}

func TestExecuteRevokeYieldsNoResult(t *testing.T) {
	app := New(newServerTestDB(t), &MockGitHub{}, Config{})
	app.Approval = approval.NewFake()
	sh := NewShell(app)
	obs := sh.execute(context.Background(), reconcile.ChangeSet{PR: 7, Environment: "staging"}, "repo",
		[]reconcile.Action{reconcile.RevokeGrant{Class: "iam", Target: "p1", PR: 7, Environment: "staging"}})
	if len(obs) != 0 {
		t.Fatalf("revoke must yield no result, got %+v", obs)
	}
}
