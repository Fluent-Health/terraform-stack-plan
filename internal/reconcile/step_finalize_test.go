package reconcile

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestFinalizeCleanPlanBecomesClean(t *testing.T) {
	prior := ChangeSet{PR: 7, Environment: "staging", Gate: NotClassified{}}
	got, _ := Step(World{Prior: prior}, RunnerFinalize{Gates: nil})
	if _, ok := got.Gate.(Clean); !ok {
		t.Fatalf("want Clean, got %T", got.Gate)
	}
}

func TestFinalizeFailedKeepsGateAndRendersFailure(t *testing.T) {
	prior := ChangeSet{PR: 7, Environment: "staging", Gate: NotClassified{},
		Exec: Execution{Stacks: []Stack{{Path: "s1", RunStatus: events.StatusRunning}}}}
	got, actions := Step(World{Prior: prior}, RunnerFinalize{Failed: true})
	if got.Exec.Stacks[0].RunStatus != events.StatusFailed {
		t.Fatalf("want failed stack, got %q", got.Exec.Stacks[0].RunStatus)
	}
	if !hasRender(actions, "failure") {
		t.Fatalf("want failure render, got %v", actions)
	}
}

func TestFinalizeWithGatesBecomesPendingAndRequestsFirstUnpinned(t *testing.T) {
	prior := ChangeSet{PR: 7, Environment: "staging", Gate: NotClassified{}}
	got, actions := Step(World{Prior: prior}, RunnerFinalize{
		Gates: []events.GateTarget{{Class: "iam", Target: "p1"}, {Class: "iam", Target: "p2"}},
	})
	p, ok := got.Gate.(Pending)
	if !ok || len(p.Targets) != 2 {
		t.Fatalf("want Pending with 2 targets, got %T %+v", got.Gate, got.Gate)
	}
	reqs := actionsOf[RequestGrant](actions)
	if len(reqs) != 1 || reqs[0].Target != "p1" || reqs[0].Requester != "" {
		t.Fatalf("want one unpinned request for p1, got %+v", reqs)
	}
}

func TestFinalizePrunesDroppedTargets(t *testing.T) {
	prior := ChangeSet{PR: 7, Environment: "staging", Gate: Pending{
		Lease: Lease{Requester: "sa0"},
		Targets: []Target{
			{Class: "iam", Target: "p1", GrantName: "g1", Grant: "ACTIVE"},
			{Class: "iam", Target: "p2", GrantName: "g2", Grant: "DENIED"},
		},
	}}
	got, actions := Step(World{Prior: prior}, RunnerFinalize{
		Gates: []events.GateTarget{{Class: "iam", Target: "p1"}},
	})
	p := got.Gate.(Pending)
	if len(p.Targets) != 1 || p.Targets[0].Target != "p1" {
		t.Fatalf("want only p1 retained, got %+v", p.Targets)
	}
	revs := actionsOf[RevokeGrant](actions)
	if len(revs) != 1 || revs[0].Target != "p2" {
		t.Fatalf("want revoke of dropped p2, got %+v", revs)
	}
}
