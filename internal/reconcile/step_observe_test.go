package reconcile

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
)

func twoTargetPending() ChangeSet {
	return ChangeSet{PR: 7, Environment: "staging", Gate: Pending{
		Targets: []Target{{Class: "iam", Target: "p1"}, {Class: "iam", Target: "p2"}},
	}}
}

func TestObservePinsLeaseAndRequestsRest(t *testing.T) {
	got, actions := Step(World{Prior: twoTargetPending()}, GrantsObserved{Grants: []ObservedGrant{
		{Class: "iam", Target: "p1", Name: "g1", State: approval.StateAwaiting, Requester: "sa3"},
	}})
	p := got.Gate.(Pending)
	if p.Lease.Requester != "sa3" {
		t.Fatalf("want lease sa3, got %q", p.Lease.Requester)
	}
	reqs := actionsOf[RequestGrant](actions)
	if len(reqs) != 1 || reqs[0].Target != "p2" || reqs[0].Requester != "sa3" {
		t.Fatalf("want pinned request for p2 as sa3, got %+v", reqs)
	}
}

func TestObserveAllActiveBecomesSatisfied(t *testing.T) {
	prior := ChangeSet{PR: 7, Environment: "staging", Gate: Pending{
		Lease:   Lease{Requester: "sa3"},
		Targets: []Target{{Class: "iam", Target: "p1", GrantName: "g1", Grant: approval.StateActive}},
	}}
	got, actions := Step(World{Prior: prior}, GateTick{Grants: []ObservedGrant{
		{Class: "iam", Target: "p1", Name: "g1", State: approval.StateActive},
	}})
	if _, ok := got.Gate.(Satisfied); !ok {
		t.Fatalf("want Satisfied, got %T", got.Gate)
	}
	if !hasRender(actions, "success") {
		t.Fatalf("want terminal success render, got %v", actions)
	}
}

func TestTickPreservesLease(t *testing.T) {
	prior := ChangeSet{PR: 7, Environment: "staging", Gate: Satisfied{
		Lease:   Lease{Requester: "sa3"},
		Targets: []Target{{Class: "iam", Target: "p1", GrantName: "g1", Grant: approval.StateActive}},
	}}
	got, _ := Step(World{Prior: prior}, GateTick{Grants: []ObservedGrant{
		{Class: "iam", Target: "p1", Name: "g1", State: approval.StateActive},
	}})
	if priorLease(got.Gate).Requester != "sa3" {
		t.Fatalf("lease clobbered: %+v", got.Gate)
	}
}

func TestObserveActiveGrantGoneDowngradesToBlocked(t *testing.T) {
	prior := ChangeSet{PR: 7, Environment: "staging", Gate: Satisfied{
		Lease:   Lease{Requester: "sa3"},
		Targets: []Target{{Class: "iam", Target: "p1", GrantName: "g1", Grant: approval.StateActive}},
	}}
	got, _ := Step(World{Prior: prior}, GateTick{Grants: []ObservedGrant{
		{Class: "iam", Target: "p1", State: ""},
	}})
	b, ok := got.Gate.(Blocked)
	if !ok || b.By.Reason != ReasonExpired {
		t.Fatalf("want Blocked{expired}, got %T %+v", got.Gate, got.Gate)
	}
}

func TestObserveDeniedBecomesBlockedDenied(t *testing.T) {
	prior := twoTargetPending()
	got, actions := Step(World{Prior: prior}, GrantsObserved{Grants: []ObservedGrant{
		{Class: "iam", Target: "p1", Name: "g1", State: approval.StateDenied},
	}})
	b, ok := got.Gate.(Blocked)
	if !ok || b.By.Reason != ReasonDenied {
		t.Fatalf("want Blocked{denied}, got %T %+v", got.Gate, got.Gate)
	}
	if !hasRender(actions, "action_required") {
		t.Fatalf("want action_required render, got %v", actions)
	}
}

func TestGateTickAbsentTargetDowngradesSatisfied(t *testing.T) {
	// Full re-list (GateTick) that OMITS the target entirely (grant vanished
	// from the backend) must downgrade a previously-Satisfied gate. Regression
	// for the absent-as-signal gap.
	prior := ChangeSet{PR: 7, Environment: "staging", Gate: Satisfied{
		Lease:   Lease{Requester: "sa3"},
		Targets: []Target{{Class: "iam", Target: "p1", GrantName: "g1", Grant: approval.StateActive}},
	}}
	got, _ := Step(World{Prior: prior}, GateTick{Grants: []ObservedGrant{}}) // empty re-list
	if _, ok := got.Gate.(Blocked); !ok {
		t.Fatalf("want Blocked after target vanished from full re-list, got %T", got.Gate)
	}
}

func TestGrantsObservedAbsentTargetIsLeftUntouched(t *testing.T) {
	// Partial feedback (GrantsObserved) must NOT clear targets it doesn't mention.
	prior := ChangeSet{PR: 7, Environment: "staging", Gate: Satisfied{
		Lease:   Lease{Requester: "sa3"},
		Targets: []Target{{Class: "iam", Target: "p1", GrantName: "g1", Grant: approval.StateActive}},
	}}
	got, _ := Step(World{Prior: prior}, GrantsObserved{Grants: []ObservedGrant{}}) // empty partial
	if _, ok := got.Gate.(Satisfied); !ok {
		t.Fatalf("want Satisfied unchanged on empty partial feedback, got %T", got.Gate)
	}
}

func TestObserveSettledPendingRendersActionRequired(t *testing.T) {
	// All targets have grants, none ACTIVE yet → awaiting approval = a COMPLETED
	// check run with action_required (not in_progress).
	prior := ChangeSet{PR: 7, Environment: "staging", Gate: Pending{
		Lease:   Lease{Requester: "sa0"},
		Targets: []Target{{Class: "iam", Target: "p1", GrantName: "g1", Grant: approval.StateAwaiting}},
	}}
	_, actions := Step(World{Prior: prior}, GateTick{Grants: []ObservedGrant{
		{Class: "iam", Target: "p1", Name: "g1", State: approval.StateAwaiting},
	}})
	if !hasRender(actions, "action_required") {
		t.Fatalf("settled-Pending should render terminal action_required, got %v", actions)
	}
}
