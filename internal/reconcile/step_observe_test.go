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

func TestObserveMultipleGrantsPerTargetPrefersActive(t *testing.T) {
	// A full re-list can return several PAM grants for the same (PR, target):
	// stale terminal ones from earlier retries PLUS the current ACTIVE one (this
	// happens whenever a gate is re-requested after grants expire/are revoked).
	// The fold must prefer the ACTIVE grant; a stale REVOKED/EXPIRED grant must
	// not clobber it (which would trip firstTerminalBlock and wedge the gate
	// permanently, since terminal grants are immutable and re-listed forever).
	// The terminal grant is listed LAST here to defeat last-write-wins.
	prior := ChangeSet{PR: 7, Environment: "staging", Gate: Pending{
		Lease:   Lease{Requester: "sa3"},
		Targets: []Target{{Class: "iam", Target: "p1", GrantName: "g-old", Grant: approval.StateAwaiting}},
	}}
	got, actions := Step(World{Prior: prior}, GateTick{Grants: []ObservedGrant{
		{Class: "iam", Target: "p1", Name: "g-new", State: approval.StateActive},
		{Class: "iam", Target: "p1", Name: "g-old", State: approval.StateRevoked},
	}})
	if _, ok := got.Gate.(Satisfied); !ok {
		t.Fatalf("want Satisfied (an ACTIVE grant exists for the target), got %T %+v", got.Gate, got.Gate)
	}
	if !hasRender(actions, "success") {
		t.Fatalf("want terminal success render, got %v", actions)
	}
}

func TestObserveActiveBeatsTerminalRegardlessOfOrder(t *testing.T) {
	// Same as above but with the ACTIVE grant listed LAST — the outcome must not
	// depend on the (PAM-defined) re-list order.
	prior := ChangeSet{PR: 7, Environment: "staging", Gate: Pending{
		Lease:   Lease{Requester: "sa3"},
		Targets: []Target{{Class: "iam", Target: "p1", GrantName: "g-old", Grant: approval.StateAwaiting}},
	}}
	got, _ := Step(World{Prior: prior}, GateTick{Grants: []ObservedGrant{
		{Class: "iam", Target: "p1", Name: "g-old", State: approval.StateExpired},
		{Class: "iam", Target: "p1", Name: "g-new", State: approval.StateActive},
	}})
	if _, ok := got.Gate.(Satisfied); !ok {
		t.Fatalf("want Satisfied, got %T %+v", got.Gate, got.Gate)
	}
}

func TestObserveOpenPendingSupersedesStaleTerminal(t *testing.T) {
	// A target re-requested after a prior revoke shows BOTH a terminal REVOKED
	// grant and a fresh AWAITING one. The open request must win (Pending), not be
	// blocked by the stale revoke.
	prior := ChangeSet{PR: 7, Environment: "staging", Gate: Pending{
		Lease:   Lease{Requester: "sa3"},
		Targets: []Target{{Class: "iam", Target: "p1", GrantName: "g-new", Grant: approval.StateAwaiting}},
	}}
	got, _ := Step(World{Prior: prior}, GateTick{Grants: []ObservedGrant{
		{Class: "iam", Target: "p1", Name: "g-new", State: approval.StateAwaiting},
		{Class: "iam", Target: "p1", Name: "g-old", State: approval.StateRevoked},
	}})
	if _, ok := got.Gate.(Blocked); ok {
		t.Fatalf("a stale REVOKED grant must not block a target with an open request, got %+v", got.Gate)
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

func TestObserveEqualRankFoldIsDeterministic(t *testing.T) {
	// Two AWAITING grants (equal rank) for one target with different requesters.
	// The fold must pick the SAME grant regardless of backend re-list order, so
	// the pinned lease is reproducible. Tiebreak is by greater Name (lease empty).
	mk := func(order []ObservedGrant) Pending {
		prior := ChangeSet{PR: 7, Environment: "staging", Gate: Pending{
			Targets: []Target{{Class: "iam", Target: "p1"}},
		}}
		got, _ := Step(World{Prior: prior}, GateTick{Grants: order})
		return got.Gate.(Pending)
	}
	a := ObservedGrant{Class: "iam", Target: "p1", Name: "g-a", State: approval.StateAwaiting, Requester: "sa1"}
	b := ObservedGrant{Class: "iam", Target: "p1", Name: "g-b", State: approval.StateAwaiting, Requester: "sa2"}
	fwd := mk([]ObservedGrant{a, b})
	rev := mk([]ObservedGrant{b, a})
	if fwd.Lease.Requester != "sa2" || fwd.Targets[0].GrantName != "g-b" {
		t.Fatalf("want g-b/sa2 chosen (greater Name), got %+v", fwd)
	}
	if rev.Lease.Requester != fwd.Lease.Requester || rev.Targets[0].GrantName != fwd.Targets[0].GrantName {
		t.Fatalf("fold not order-independent: fwd=%+v rev=%+v", fwd, rev)
	}
}

func TestObserveEqualRankPrefersLeaseMatch(t *testing.T) {
	// At equal rank, the grant whose Requester matches the pinned lease wins —
	// even when the other grant has the lexicographically greater Name. This
	// preserves requester continuity across a re-list, order-independently.
	mk := func(order []ObservedGrant) Pending {
		prior := ChangeSet{PR: 7, Environment: "staging", Gate: Pending{
			Lease:   Lease{Requester: "sa1"},
			Targets: []Target{{Class: "iam", Target: "p1"}},
		}}
		got, _ := Step(World{Prior: prior}, GateTick{Grants: order})
		return got.Gate.(Pending)
	}
	// g-a matches the lease (sa1); g-z has the greater Name but a different requester.
	a := ObservedGrant{Class: "iam", Target: "p1", Name: "g-a", State: approval.StateAwaiting, Requester: "sa1"}
	z := ObservedGrant{Class: "iam", Target: "p1", Name: "g-z", State: approval.StateAwaiting, Requester: "sa2"}
	fwd := mk([]ObservedGrant{a, z})
	rev := mk([]ObservedGrant{z, a})
	if fwd.Targets[0].GrantName != "g-a" {
		t.Fatalf("want lease-matching g-a to win over greater-Name g-z, got %+v", fwd)
	}
	if rev.Targets[0].GrantName != fwd.Targets[0].GrantName {
		t.Fatalf("lease-match tiebreak not order-independent: fwd=%+v rev=%+v", fwd, rev)
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
