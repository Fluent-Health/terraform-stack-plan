package reconcile

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
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

func TestFinalizeFailedSweepsInitStatuses(t *testing.T) {
	// A stack stuck in "initializing" or "initialized" at a failed finalize must
	// be swept to "failed", just like pending/running.
	for _, initStatus := range []events.Status{events.StatusInitializing, events.StatusInitialized} {
		initStatus := initStatus
		t.Run(string(initStatus), func(t *testing.T) {
			prior := ChangeSet{PR: 7, Environment: "staging", Gate: NotClassified{},
				Exec: Execution{Stacks: []Stack{{Path: "s1", RunStatus: initStatus}}}}
			got, actions := Step(World{Prior: prior}, RunnerFinalize{Failed: true})
			if got.Exec.Stacks[0].RunStatus != events.StatusFailed {
				t.Fatalf("want failed stack, got %q", got.Exec.Stacks[0].RunStatus)
			}
			if !hasRender(actions, "failure") {
				t.Fatalf("want failure render, got %v", actions)
			}
		})
	}
}

func TestApplyFinalizeNeverWeakensEstablishedGate(t *testing.T) {
	// Issue #103: an apply-time re-classify that under-reports (empty Gates) must
	// NOT clobber a plan-established gate. An apply-context finalize is a recovery
	// signal, not an authority — it carries the prior gate forward so GateCheck
	// still returns the leased requester (the apply impersonates the approved
	// grant) instead of fail-opening to the ambient SA.
	prior := ChangeSet{PR: 7, Environment: "prod", Gate: Satisfied{
		Lease:   Lease{Requester: "tf-applier-0@x"},
		Targets: []Target{{Class: "iam", Target: "proj-a", GrantName: "g1", Grant: approval.StateActive}},
	}}
	got, actions := Step(World{Prior: prior}, RunnerFinalize{ApplyContext: true, Gates: nil})
	ts := gateTargets(got.Gate)
	if len(ts) != 1 || ts[0].Target != "proj-a" || ts[0].GrantName != "g1" {
		t.Fatalf("apply finalize weakened the gate: %T %+v", got.Gate, got.Gate)
	}
	if priorLease(got.Gate).Requester != "tf-applier-0@x" {
		t.Fatalf("apply finalize lost the leased requester: %+v", got.Gate)
	}
	if revs := actionsOf[RevokeGrant](actions); len(revs) != 0 {
		t.Fatalf("apply finalize must not revoke an established grant, got %+v", revs)
	}
}

func TestPlanFinalizeEmptyGatesStillClears(t *testing.T) {
	// A plan-context finalize is authoritative: a re-plan that drops all gates
	// clears the gate (unchanged behavior — only apply-context is additive).
	prior := ChangeSet{PR: 7, Environment: "prod", Gate: Satisfied{
		Targets: []Target{{Class: "iam", Target: "proj-a", GrantName: "g1", Grant: approval.StateActive}},
	}}
	got, _ := Step(World{Prior: prior}, RunnerFinalize{Gates: nil}) // ApplyContext false
	if _, ok := got.Gate.(Clean); !ok {
		t.Fatalf("plan finalize with no gates should clear the gate, got %T", got.Gate)
	}
}

func TestFinalizeReArmsTerminalButKeepsLiveGrant(t *testing.T) {
	// Mixed prior: p1 ACTIVE (live), p2 REVOKED (terminal). A re-plan listing both
	// must re-request ONLY p2 — the live ACTIVE grant on p1 is carried forward and
	// not needlessly re-requested (preserves the re-plan anti-clobber intent).
	prior := ChangeSet{PR: 7, Environment: "staging", Gate: Pending{
		Lease: Lease{Requester: "sa1"},
		Targets: []Target{
			{Class: "iam", Target: "p1", GrantName: "g-p1", Grant: approval.StateActive},
			{Class: "iam", Target: "p2", GrantName: "g-p2", Grant: approval.StateRevoked},
		},
	}}
	_, actions := Step(World{Prior: prior}, RunnerFinalize{Gates: []events.GateTarget{
		{Class: "iam", Target: "p1"},
		{Class: "iam", Target: "p2"},
	}})
	reqs := actionsOf[RequestGrant](actions)
	if len(reqs) != 1 || reqs[0].Target != "p2" {
		t.Fatalf("want exactly one RequestGrant for p2, got %+v", reqs)
	}
}
