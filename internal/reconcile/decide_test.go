package reconcile

import (
	"reflect"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// --- RunnerFinalize ---

func TestDecideFinalizeFailed(t *testing.T) {
	cs := ChangeSet{}
	got := Decide(cs, RunnerFinalize{Failed: true})
	if len(got) != 0 {
		t.Fatalf("want no gate events for a failed run, got %#v", got)
	}
}

func TestDecideFinalizeCleanGatePassed(t *testing.T) {
	cs := ChangeSet{}
	got := Decide(cs, RunnerFinalize{})
	if _, ok := got[len(got)-1].(GatePassed); !ok {
		t.Fatalf("want trailing GatePassed, got %#v", got)
	}
}

func TestDecideFinalizeGatedRequestsFirst(t *testing.T) {
	cs := ChangeSet{PR: 7, Environment: "nonprod"}
	got := Decide(cs, RunnerFinalize{
		Gates: []events.GateTarget{{Class: "c", Target: "t"}},
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

func TestDecideFinalizeCarryForwardAllActiveSatisfies(t *testing.T) {
	// A new build re-finalizes the same plan while the PRIOR build's grants are
	// all still ACTIVE. Every target carries forward (no request), so without an
	// explicit GateSatisfied the gate would fold to Pending{all-ACTIVE} and render
	// in_progress forever — PendingGates excludes fully-ACTIVE gates, so the
	// ReconcileLoop never heals it. decideFinalize must emit GateSatisfied here.
	cs := ChangeSet{PR: 7, Environment: "prod", Gate: Satisfied{
		Lease: Lease{Requester: "tf-applier-0@x"},
		Targets: []Target{
			{Class: "iam", Target: "p1", GrantName: "g-p1", Grant: approval.StateActive},
			{Class: "iam", Target: "p2", GrantName: "g-p2", Grant: approval.StateActive},
		},
	}}
	got := Decide(cs, RunnerFinalize{Gates: []events.GateTarget{
		{Class: "iam", Target: "p1"},
		{Class: "iam", Target: "p2"},
	}})
	if len(eventsOf[GateTargetRequested](got)) != 0 {
		t.Fatalf("all targets carried forward ACTIVE, none must be requested, got %#v", got)
	}
	if _, ok := lastOf[GateSatisfied](got); !ok {
		t.Fatalf("want trailing GateSatisfied for all-ACTIVE carry-forward, got %#v", got)
	}
}

func TestDecideFinalizeCarryForwardAllAwaitingNoSatisfy(t *testing.T) {
	// All targets carry forward OPEN but AWAITING (not ACTIVE): no request (all
	// open) and no GateSatisfied (not all ACTIVE). The gate stays Pending and the
	// ReconcileLoop re-settles it (it remains in PendingGates since not all-ACTIVE).
	cs := ChangeSet{PR: 7, Environment: "staging", Gate: Pending{
		Lease:   Lease{Requester: "sa1"},
		Targets: []Target{{Class: "iam", Target: "p1", GrantName: "g-p1", Grant: approval.StateAwaiting}},
	}}
	got := Decide(cs, RunnerFinalize{Gates: []events.GateTarget{{Class: "iam", Target: "p1"}}})
	if _, ok := lastOf[GateSatisfied](got); ok {
		t.Fatalf("AWAITING is not ACTIVE, must not emit GateSatisfied, got %#v", got)
	}
	if len(eventsOf[GateTargetRequested](got)) != 0 {
		t.Fatalf("open AWAITING target must not be re-requested, got %#v", got)
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

// lastOf returns the last event of type T in evs (and whether one was found).
func lastOf[T Event](evs []Event) (T, bool) {
	var found T
	var ok bool
	for _, e := range evs {
		if v, is := e.(T); is {
			found, ok = v, true
		}
	}
	return found, ok
}

// eventsOf returns all events of type T in evs.
func eventsOf[T Event](evs []Event) []T {
	var out []T
	for _, e := range evs {
		if v, ok := e.(T); ok {
			out = append(out, v)
		}
	}
	return out
}

// --- GrantsObserved / GateTick (observe + collision) ---

func TestDecideObserveNonGatedNoEvents(t *testing.T) {
	for _, g := range []GateState{NotClassified{}, Clean{}, nil} {
		if got := Decide(ChangeSet{Gate: g}, GrantsObserved{}); len(got) != 0 {
			t.Fatalf("gate %T: want none, got %#v", g, got)
		}
	}
}

func TestDecideObserveAllActiveSatisfied(t *testing.T) {
	cs := ChangeSet{Gate: Pending{Targets: []Target{{Class: "c", Target: "t", GrantName: "g1", Grant: approval.StateActive}}}}
	got := Decide(cs, GrantsObserved{Grants: []ObservedGrant{
		{Class: "c", Target: "t", Name: "g1", State: approval.StateActive},
	}})
	if _, ok := lastOf[GateSatisfied](got); !ok {
		t.Fatalf("want trailing GateSatisfied, got %#v", got)
	}
	if obs := eventsOf[GrantObserved](got); len(obs) != 1 || obs[0].State != approval.StateActive {
		t.Fatalf("want one GrantObserved{ACTIVE}, got %#v", got)
	}
}

func TestDecideObserveDeniedBlocks(t *testing.T) {
	cs := ChangeSet{Gate: Pending{Targets: []Target{{Class: "c", Target: "t"}, {Class: "c", Target: "u"}}}}
	got := Decide(cs, GrantsObserved{Grants: []ObservedGrant{
		{Class: "c", Target: "t", Name: "g1", State: approval.StateDenied},
	}})
	gb, ok := lastOf[GateBlocked](got)
	if !ok || gb.Reason != ReasonDenied {
		t.Fatalf("want GateBlocked{denied}, got %#v", got)
	}
}

func TestDecideObserveRevokedBlocks(t *testing.T) {
	cs := ChangeSet{Gate: Pending{Targets: []Target{{Class: "c", Target: "t"}}}}
	got := Decide(cs, GrantsObserved{Grants: []ObservedGrant{
		{Class: "c", Target: "t", Name: "g1", State: approval.StateRevoked},
	}})
	gb, ok := lastOf[GateBlocked](got)
	if !ok || gb.Reason != ReasonRevoked {
		t.Fatalf("want GateBlocked{revoked}, got %#v", got)
	}
}

func TestDecideObserveUngrantedReArmsAll(t *testing.T) {
	cs := ChangeSet{Gate: Pending{
		Lease:   Lease{Requester: "sa3"},
		Targets: []Target{{Class: "c", Target: "t"}, {Class: "c", Target: "u"}},
	}}
	got := Decide(cs, GrantsObserved{Grants: []ObservedGrant{
		{Class: "c", Target: "t", Name: "g1", State: approval.StateAwaiting, Requester: "sa3"},
	}})
	reqs := eventsOf[GateTargetRequested](got)
	// t has a grant now; u is still ungranted → exactly one re-arm for u, pinned.
	if len(reqs) != 1 || reqs[0].Target != "u" || reqs[0].Requester != "sa3" {
		t.Fatalf("want one GateTargetRequested{u, sa3}, got %#v", got)
	}
}

func TestDecideObserveLapsedExpiredReArms(t *testing.T) {
	cs := ChangeSet{Gate: Pending{
		Lease:   Lease{Requester: "sa3"},
		Targets: []Target{{Class: "c", Target: "t", GrantName: "g1", Grant: approval.StateAwaiting}},
	}}
	got := Decide(cs, GateTick{Grants: []ObservedGrant{
		{Class: "c", Target: "t", Name: "g1", State: approval.StateExpired},
	}})
	reqs := eventsOf[GateTargetRequested](got)
	if len(reqs) != 1 || reqs[0].Target != "t" || reqs[0].Requester != "sa3" {
		t.Fatalf("want one GateTargetRequested{t, sa3}, got %#v", got)
	}
}

func TestDecideTickDropsTargetClearsAndDowngrades(t *testing.T) {
	cs := ChangeSet{Gate: Satisfied{
		Lease:   Lease{Requester: "sa3"},
		Targets: []Target{{Class: "c", Target: "t", GrantName: "g1", Grant: approval.StateActive}},
	}}
	got := Decide(cs, GateTick{Grants: []ObservedGrant{}}) // full re-list omits t
	if _, ok := lastOf[GrantCleared](got); !ok {
		t.Fatalf("want GrantCleared for the dropped target, got %#v", got)
	}
	gb, ok := lastOf[GateBlocked](got)
	if !ok || gb.Reason != ReasonExpired {
		t.Fatalf("want GateBlocked{expired} downgrade, got %#v", got)
	}
}

func TestDecideObservePartialLeavesUnmentionedUntouched(t *testing.T) {
	cs := ChangeSet{Gate: Satisfied{
		Lease:   Lease{Requester: "sa3"},
		Targets: []Target{{Class: "c", Target: "t", GrantName: "g1", Grant: approval.StateActive}},
	}}
	got := Decide(cs, GrantsObserved{Grants: []ObservedGrant{}}) // partial: mentions nothing
	if len(eventsOf[GrantCleared](got)) != 0 {
		t.Fatalf("partial feedback must not clear targets, got %#v", got)
	}
	if _, ok := lastOf[GateSatisfied](got); !ok {
		t.Fatalf("want GateSatisfied unchanged, got %#v", got)
	}
}

func TestDecideObserveSettledPendingEmitsNoOutcome(t *testing.T) {
	// All targets have grants, none ACTIVE, none re-armable, not was-active.
	cs := ChangeSet{Gate: Pending{
		Lease:   Lease{Requester: "sa0"},
		Targets: []Target{{Class: "c", Target: "t", GrantName: "g1", Grant: approval.StateAwaiting}},
	}}
	got := Decide(cs, GateTick{Grants: []ObservedGrant{
		{Class: "c", Target: "t", Name: "g1", State: approval.StateAwaiting},
	}})
	// No GateSatisfied / GateBlocked / GateTargetRequested — only the GrantObserved.
	if _, ok := lastOf[GateSatisfied](got); ok {
		t.Fatalf("settled-Pending must not emit GateSatisfied, got %#v", got)
	}
	if _, ok := lastOf[GateBlocked](got); ok {
		t.Fatalf("settled-Pending must not emit GateBlocked, got %#v", got)
	}
	if len(eventsOf[GateTargetRequested](got)) != 0 {
		t.Fatalf("settled-Pending must not re-arm, got %#v", got)
	}
	if len(eventsOf[GrantObserved](got)) != 1 {
		t.Fatalf("want the GrantObserved fold fact, got %#v", got)
	}
}

func TestDecideCollisionSelfBlocks(t *testing.T) {
	cs := ChangeSet{PR: 7, Gate: Pending{Targets: []Target{{Class: "c", Target: "t"}}}}
	got := Decide(cs, GrantsObserved{Grants: []ObservedGrant{
		{Class: "c", Target: "t", Collision: &Collision{ByPR: 7, ByEnv: "prod", BySelf: true}},
	}})
	gb, ok := lastOf[GateBlocked](got)
	if !ok || gb.Reason != ReasonSlotSelf || gb.ByEnv != "prod" {
		t.Fatalf("want GateBlocked{slot_self,prod}, got %#v", got)
	}
}

func TestDecideCollisionForeignOpenBlocks(t *testing.T) {
	cs := ChangeSet{PR: 8, Gate: Pending{Targets: []Target{{Class: "c", Target: "t"}}}}
	got := Decide(cs, GrantsObserved{Grants: []ObservedGrant{
		{Class: "c", Target: "t", Collision: &Collision{ByPR: 5, ByEnv: "staging", ByPRAbandoned: false}},
	}})
	gb, ok := lastOf[GateBlocked](got)
	if !ok || gb.Reason != ReasonSlotForeign || gb.ByPR != 5 {
		t.Fatalf("want GateBlocked{slot_foreign,5}, got %#v", got)
	}
}

func TestDecideCollisionAbandonedRevokesForeignThenRequests(t *testing.T) {
	cs := ChangeSet{PR: 8, Environment: "staging", Gate: Pending{Targets: []Target{{Class: "c", Target: "t"}}}}
	got := Decide(cs, GrantsObserved{Grants: []ObservedGrant{
		{Class: "c", Target: "t", Collision: &Collision{ByPR: 7, ByEnv: "staging", ByPRAbandoned: true}},
	}})
	want := []Event{
		TargetRevoked{Class: "c", Target: "t", PR: 7, Env: "staging"},
		GateTargetRequested{Class: "c", Target: "t"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
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

func TestDecideInboundBuild(t *testing.T) {
	base := ChangeSet{PR: 7, Environment: "nonprod", Gate: NotClassified{}}
	deadRun := func(phase RunPhase, buildRef string) map[string]Run {
		return map[string]Run{RunKindPlan: {
			ExecutionID: "run-7-nonprod-plan-abc123abc123-a1", Kind: RunKindPlan,
			SHA: "abc123abc123def", Branch: "feat/x", Attempt: 1,
			BuildRef: buildRef, Phase: phase,
		}}
	}

	t.Run("new build for a start-failed run supersedes and adopts", func(t *testing.T) {
		cs := base
		cs.Runs = deadRun(RunPhaseStartFailed, "build-old")
		evs := Decide(cs, InboundBuild{Kind: RunKindPlan, SHA: "abc123abc123def", BuildRef: "build-new"})
		wantNew := "run-7-nonprod-plan-abc123abc123-a2"
		sup, ok := evs[0].(RunSuperseded)
		if !ok || sup.OldExecutionID != "run-7-nonprod-plan-abc123abc123-a1" || sup.OldBuildRef != "build-old" || sup.NewExecutionID != wantNew {
			t.Fatalf("evs[0] = %+v", evs[0])
		}
		ad, ok := evs[1].(RunAdopted)
		if !ok || ad.ExecutionID != wantNew || ad.BuildRef != "build-new" || ad.SHA != "abc123abc123def" || ad.Attempt != 2 || ad.Branch != "feat/x" {
			t.Fatalf("evs[1] = %+v", evs[1])
		}
	})

	t.Run("same build id is idempotent no-op", func(t *testing.T) {
		cs := base
		cs.Runs = deadRun(RunPhaseStartFailed, "build-old")
		if evs := Decide(cs, InboundBuild{Kind: RunKindPlan, SHA: "abc123abc123def", BuildRef: "build-old"}); len(evs) != 0 {
			t.Fatalf("want no-op, got %+v", evs)
		}
	})

	t.Run("live run is left alone", func(t *testing.T) {
		cs := base
		cs.Runs = deadRun(RunPhaseStarted, "build-old")
		if evs := Decide(cs, InboundBuild{Kind: RunKindPlan, SHA: "abc123abc123def", BuildRef: "build-new"}); len(evs) != 0 {
			t.Fatalf("want no-op for live run, got %+v", evs)
		}
	})

	t.Run("different sha is ignored", func(t *testing.T) {
		cs := base
		cs.Runs = deadRun(RunPhaseStartFailed, "build-old")
		if evs := Decide(cs, InboundBuild{Kind: RunKindPlan, SHA: "othersha000000", BuildRef: "build-new"}); len(evs) != 0 {
			t.Fatalf("want no-op for different sha, got %+v", evs)
		}
	})

	t.Run("no serve run is ignored", func(t *testing.T) {
		if evs := Decide(base, InboundBuild{Kind: RunKindPlan, SHA: "abc123abc123def", BuildRef: "build-new"}); len(evs) != 0 {
			t.Fatalf("want no-op when no run tracked, got %+v", evs)
		}
	})

	t.Run("unknown kind is ignored", func(t *testing.T) {
		cs := base
		cs.Runs = deadRun(RunPhaseStartFailed, "build-old")
		if evs := Decide(cs, InboundBuild{Kind: "bogus", SHA: "abc123abc123def", BuildRef: "build-new"}); len(evs) != 0 {
			t.Fatalf("want no-op for unknown kind, got %+v", evs)
		}
	})

	t.Run("empty build ref is ignored", func(t *testing.T) {
		cs := base
		cs.Runs = deadRun(RunPhaseStartFailed, "build-old")
		if evs := Decide(cs, InboundBuild{Kind: RunKindPlan, SHA: "abc123abc123def", BuildRef: ""}); len(evs) != 0 {
			t.Fatalf("want no-op for empty build ref, got %+v", evs)
		}
	})
}
