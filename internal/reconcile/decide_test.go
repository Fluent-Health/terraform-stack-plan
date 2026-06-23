package reconcile

import (
	"reflect"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// --- RunnerFinalize ---

func TestDecideFinalizeFailed(t *testing.T) {
	cs := ChangeSet{Exec: Execution{Stacks: []Stack{{Path: "a", RunStatus: events.StatusRunning}}}}
	got := Decide(cs, RunnerFinalize{Failed: true})
	if len(got) != 1 {
		t.Fatalf("want 1 event, got %#v", got)
	}
	if _, ok := got[0].(ExecutionFailed); !ok {
		t.Fatalf("want ExecutionFailed, got %T", got[0])
	}
}

func TestDecideFinalizeCleanGatePassed(t *testing.T) {
	cs := ChangeSet{Exec: Execution{Stacks: []Stack{{Path: "a"}}}}
	got := Decide(cs, RunnerFinalize{Projects: map[string]string{"a": "p"}})
	// StacksClassified backfill + GatePassed
	if _, ok := got[len(got)-1].(GatePassed); !ok {
		t.Fatalf("want trailing GatePassed, got %#v", got)
	}
}

func TestDecideFinalizeGatedRequestsFirst(t *testing.T) {
	cs := ChangeSet{PR: 7, Environment: "nonprod", Exec: Execution{Stacks: []Stack{{Path: "a"}}}}
	got := Decide(cs, RunnerFinalize{
		Projects: map[string]string{"a": "p"},
		Gates:    []events.GateTarget{{Class: "c", Target: "t"}},
	})
	var hasClassified, hasRequest bool
	for _, e := range got {
		switch e.(type) {
		case Classified:
			hasClassified = true
		case GateTargetRequested:
			hasRequest = true
		}
	}
	if !hasClassified || !hasRequest {
		t.Fatalf("want Classified + GateTargetRequested, got %#v", got)
	}
}

func TestDecideFinalizeGatedPrunesDropped(t *testing.T) {
	// Plan finalize with prior target p2 (has GrantName) that is NOT in new Gates:
	// must emit TargetRevoked for p2.
	cs := ChangeSet{PR: 7, Environment: "staging", Gate: Pending{
		Targets: []Target{
			{Class: "iam", Target: "p1", GrantName: "g1", Grant: "ACTIVE"},
			{Class: "iam", Target: "p2", GrantName: "g2", Grant: "DENIED"},
		},
	}}
	got := Decide(cs, RunnerFinalize{
		Gates: []events.GateTarget{{Class: "iam", Target: "p1"}},
	})
	var revoked []TargetRevoked
	for _, e := range got {
		if r, ok := e.(TargetRevoked); ok {
			revoked = append(revoked, r)
		}
	}
	if len(revoked) != 1 || revoked[0].Target != "p2" {
		t.Fatalf("want revoke of p2, got %#v", revoked)
	}
}

func TestDecideFinalizeApplyContextNoPrune(t *testing.T) {
	// ApplyContext=true must NOT prune even if prior has targets not in Gates.
	cs := ChangeSet{PR: 7, Environment: "prod", Gate: Satisfied{
		Lease:   Lease{Requester: "tf-applier-0@x"},
		Targets: []Target{{Class: "iam", Target: "proj-a", GrantName: "g1", Grant: "ACTIVE"}},
	}}
	got := Decide(cs, RunnerFinalize{ApplyContext: true, Gates: nil})
	for _, e := range got {
		if _, ok := e.(TargetRevoked); ok {
			t.Fatalf("apply finalize must not prune, got TargetRevoked in %#v", got)
		}
	}
}

func TestDecideFinalizeRequesterThreaded(t *testing.T) {
	// GateTargetRequested.Requester must equal priorLease(state.Gate).Requester.
	cs := ChangeSet{PR: 7, Environment: "staging", Gate: Pending{
		Lease:   Lease{Requester: "sa1"},
		Targets: []Target{{Class: "iam", Target: "p1"}},
	}}
	got := Decide(cs, RunnerFinalize{
		Gates: []events.GateTarget{{Class: "iam", Target: "p1"}},
	})
	for _, e := range got {
		if req, ok := e.(GateTargetRequested); ok {
			if req.Requester != "sa1" {
				t.Fatalf("want Requester=sa1, got %q", req.Requester)
			}
			return
		}
	}
	t.Fatalf("no GateTargetRequested found in %#v", got)
}

func TestDecideFinalizeCarryForwardSkipsRequest(t *testing.T) {
	// Prior has p1 ACTIVE (open grant): it must be carried forward, no request for p1.
	// p2 is fresh: must be requested.
	cs := ChangeSet{PR: 7, Environment: "staging", Gate: Pending{
		Lease:   Lease{Requester: "sa1"},
		Targets: []Target{{Class: "iam", Target: "p1", GrantName: "g-p1", Grant: approval.StateActive}},
	}}
	got := Decide(cs, RunnerFinalize{Gates: []events.GateTarget{
		{Class: "iam", Target: "p1"},
		{Class: "iam", Target: "p2"},
	}})
	for _, e := range got {
		if req, ok := e.(GateTargetRequested); ok {
			if req.Target == "p1" {
				t.Fatalf("p1 has live grant, must not be requested")
			}
			if req.Target == "p2" {
				return
			}
		}
	}
	t.Fatalf("expected GateTargetRequested for p2, got %#v", got)
}

func TestDecideRunnerInitEmitsExecutionStarted(t *testing.T) {
	exec := Execution{ID: "e1"}
	got := Decide(ChangeSet{}, RunnerInit{Exec: exec})
	want := []Event{ExecutionStarted{Exec: exec}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestDecideRunnerPhaseEmitsPhaseChanged(t *testing.T) {
	got := Decide(ChangeSet{}, RunnerPhase{Phase: events.PhaseApplying})
	if len(got) != 1 || got[0] != (PhaseChanged{Phase: events.PhaseApplying}) {
		t.Fatalf("got %#v", got)
	}
}

func TestDecideRunnerUpdateEmitsStackStatusChanged(t *testing.T) {
	got := Decide(ChangeSet{}, RunnerUpdate{Stack: "a", Status: events.StatusRunning, Detail: "d"})
	if len(got) != 1 || got[0] != (StackStatusChanged{Stack: "a", Status: events.StatusRunning, Detail: "d"}) {
		t.Fatalf("got %#v", got)
	}
}

func TestDecideApplySucceededNotClassifiedNoEvents(t *testing.T) {
	if got := Decide(ChangeSet{Gate: NotClassified{}}, ApplySucceeded{}); len(got) != 0 {
		t.Fatalf("want no events, got %#v", got)
	}
}

func TestDecideApplySucceededReleasesClaimAndRevokesGrants(t *testing.T) {
	cs := ChangeSet{PR: 7, Environment: "nonprod", Gate: Satisfied{
		Targets: []Target{{Class: "c", Target: "t", GrantName: "g1", Grant: approval.StateActive}},
	}}
	got := Decide(cs, ApplySucceeded{})
	want := []Event{
		ClaimReleased{PR: 7, Environment: "nonprod"},
		TargetRevoked{Class: "c", Target: "t", PR: 7, Env: "nonprod"},
		GateReleased{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestDecidePRClosedNoTargetsNoEvents(t *testing.T) {
	if got := Decide(ChangeSet{Gate: Clean{}}, PRClosed{}); len(got) != 0 {
		t.Fatalf("want none, got %#v", got)
	}
}

func TestDecidePRClosedRevokesAndBlocks(t *testing.T) {
	cs := ChangeSet{PR: 7, Environment: "nonprod", Gate: Pending{
		Targets: []Target{{Class: "c", Target: "t", GrantName: "g1", Grant: approval.StateActive}},
	}}
	got := Decide(cs, PRClosed{})
	want := []Event{
		TargetRevoked{Class: "c", Target: "t", PR: 7, Env: "nonprod"},
		PRClosedRecorded{},
		GateBlocked{Reason: ReasonRevoked},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}
