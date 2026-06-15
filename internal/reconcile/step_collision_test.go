package reconcile

import "testing"

func oneTargetPending() ChangeSet {
	return ChangeSet{PR: 8, Environment: "staging", Gate: Pending{
		Targets: []Target{{Class: "iam", Target: "p1"}},
	}}
}

func TestCollisionClosedForeignRevokesBlockerAndRetries(t *testing.T) {
	got, actions := Step(World{Prior: oneTargetPending()}, GrantsObserved{Grants: []ObservedGrant{
		{Class: "iam", Target: "p1", Collision: &Collision{ByPR: 7, ByEnv: "staging", BySelf: false, ByPRClosed: true}},
	}})
	revs := actionsOf[RevokeGrant](actions)
	if len(revs) != 1 || revs[0].PR != 7 {
		t.Fatalf("want revoke of blocker PR 7, got %+v", revs)
	}
	reqs := actionsOf[RequestGrant](actions)
	if len(reqs) != 1 || reqs[0].Target != "p1" {
		t.Fatalf("want retry request for p1, got %+v", reqs)
	}
	if _, ok := got.Gate.(Pending); !ok {
		t.Fatalf("want still Pending during retry, got %T", got.Gate)
	}
}

func TestCollisionOpenForeignBlocksAndWaits(t *testing.T) {
	got, actions := Step(World{Prior: oneTargetPending()}, GrantsObserved{Grants: []ObservedGrant{
		{Class: "iam", Target: "p1", Collision: &Collision{ByPR: 7, ByEnv: "staging", BySelf: false, ByPRClosed: false}},
	}})
	b, ok := got.Gate.(Blocked)
	if !ok || b.By.Reason != ReasonSlotForeign || b.By.ByPR != 7 {
		t.Fatalf("want Blocked{slot_foreign,7}, got %T %+v", got.Gate, got.Gate)
	}
	if len(actionsOf[RequestGrant](actions)) != 0 || len(actionsOf[RevokeGrant](actions)) != 0 {
		t.Fatalf("want no request/revoke while waiting, got %v", actions)
	}
}

func TestCollisionSelfBlocksWithoutSelfRevoke(t *testing.T) {
	got, actions := Step(World{Prior: oneTargetPending()}, GrantsObserved{Grants: []ObservedGrant{
		{Class: "iam", Target: "p1", Collision: &Collision{ByPR: 8, ByEnv: "prod", BySelf: true, ByPRClosed: false}},
	}})
	b, ok := got.Gate.(Blocked)
	if !ok || b.By.Reason != ReasonSlotSelf {
		t.Fatalf("want Blocked{slot_self}, got %T %+v", got.Gate, got.Gate)
	}
	if len(actionsOf[RevokeGrant](actions)) != 0 {
		t.Fatalf("must not self-revoke, got %v", actions)
	}
}
