package server

import (
	"context"
	"errors"
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

func TestObserveErrorNotCollision(t *testing.T) {
	app := New(newServerTestDB(t), &MockGitHub{}, Config{})
	sh := NewShell(app)

	cs := reconcile.ChangeSet{PR: 7, Environment: "staging"}
	act := reconcile.RequestGrant{Class: "iam", Target: "p1"}
	err := errors.New("internal error")

	obs := sh.observeError(context.Background(), cs, "repo", act, err)
	if obs.State != "" || obs.Collision != nil {
		t.Fatalf("unhandled error should leave State empty and Collision nil, got %+v", obs)
	}
}

func TestObserveErrorCollisionSelf(t *testing.T) {
	app := New(newServerTestDB(t), &MockGitHub{}, Config{})
	sh := NewShell(app)

	cs := reconcile.ChangeSet{PR: 7, Environment: "staging"}
	act := reconcile.RequestGrant{Class: "iam", Target: "p1"}
	colErr := &approval.SlotCollisionError{
		BlockingGrant: approval.Grant{Request: approval.Request{PR: 7, Environment: "staging"}},
	}

	obs := sh.observeError(context.Background(), cs, "repo", act, colErr)
	if obs.Collision == nil {
		t.Fatalf("expected collision information, got nil")
	}
	if !obs.Collision.BySelf {
		t.Errorf("expected BySelf to be true for collision with same PR")
	}
	if obs.Collision.ByPRAbandoned {
		t.Errorf("expected ByPRAbandoned to be false for self-collision")
	}
}

func TestObserveErrorCollisionOtherPR(t *testing.T) {
	ghCalled := false
	gh := &MockGitHub{
		PRAbandonedFn: func(ctx context.Context, repo string, pr int) (bool, error) {
			if pr == 5 {
				ghCalled = true
				return true, nil
			}
			return false, nil
		},
	}
	app := New(newServerTestDB(t), gh, Config{})
	sh := NewShell(app)

	cs := reconcile.ChangeSet{PR: 7, Environment: "staging"}
	act := reconcile.RequestGrant{Class: "iam", Target: "p1"}
	colErr := &approval.SlotCollisionError{
		BlockingGrant: approval.Grant{Request: approval.Request{PR: 5, Environment: "staging"}},
	}

	obs := sh.observeError(context.Background(), cs, "repo", act, colErr)
	if obs.Collision == nil {
		t.Fatalf("expected collision information, got nil")
	}
	if obs.Collision.BySelf {
		t.Errorf("expected BySelf to be false for collision with different PR")
	}
	if !obs.Collision.ByPRAbandoned {
		t.Errorf("expected ByPRAbandoned to be true based on MockGitHub response")
	}
	if !ghCalled {
		t.Errorf("expected MockGitHub.PRAbandoned to be called")
	}
}

func TestObserveErrorCollisionPRZero(t *testing.T) {
	ghCalled := false
	gh := &MockGitHub{
		PRAbandonedFn: func(ctx context.Context, repo string, pr int) (bool, error) {
			ghCalled = true
			return false, nil
		},
	}
	app := New(newServerTestDB(t), gh, Config{})
	sh := NewShell(app)

	cs := reconcile.ChangeSet{PR: 7, Environment: "staging"}
	act := reconcile.RequestGrant{Class: "iam", Target: "p1"}
	colErr := &approval.SlotCollisionError{
		BlockingGrant: approval.Grant{Request: approval.Request{PR: 0, Environment: "staging"}},
	}

	obs := sh.observeError(context.Background(), cs, "repo", act, colErr)
	if obs.Collision == nil {
		t.Fatalf("expected collision information, got nil")
	}
	if obs.Collision.BySelf {
		t.Errorf("expected BySelf to be false")
	}
	if obs.Collision.ByPRAbandoned {
		t.Errorf("expected ByPRAbandoned to be false")
	}
	if ghCalled {
		t.Errorf("expected MockGitHub.PRAbandoned NOT to be called when blocking PR is 0")
	}
}
