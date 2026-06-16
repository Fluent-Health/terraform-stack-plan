package server

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// storeRaw / storeTarget mirror the store shapes so tests can build fixtures.
type storeRaw = store.RawChangeSet
type storeTarget = store.GateTarget

func TestGatherMapsRawGateToSumType(t *testing.T) {
	raw := store_RawChangeSet(7, "staging", true, []rawTarget{
		{class: "iam", target: "p1", grant: "g1", state: "ACTIVE", requester: "sa3"},
	})
	cs := mapRawGate(raw)
	sat, ok := cs.Gate.(reconcile.Satisfied)
	if !ok {
		t.Fatalf("want Satisfied for all-ACTIVE, got %T", cs.Gate)
	}
	if sat.Lease.Requester != "sa3" || len(sat.Targets) != 1 {
		t.Fatalf("bad mapping: %+v", sat)
	}
}

func TestGatherNotClassifiedWhenUnclassified(t *testing.T) {
	raw := store_RawChangeSet(7, "staging", false, nil)
	cs := mapRawGate(raw)
	if _, ok := cs.Gate.(reconcile.NotClassified); !ok {
		t.Fatalf("want NotClassified, got %T", cs.Gate)
	}
}

type rawTarget struct{ class, target, grant, state, requester string }

func store_RawChangeSet(pr int, env string, classified bool, targets []rawTarget) storeRaw {
	r := storeRaw{PR: pr, Environment: env, Classified: classified}
	for _, t := range targets {
		r.Targets = append(r.Targets, storeTarget{Class: t.class, Target: t.target, GrantName: t.grant, State: t.state, Requester: t.requester})
	}
	return r
}

func TestGatherExpiredReloadsAsPending(t *testing.T) {
	// A persisted EXPIRED target reloads as Pending, NOT Blocked: the live core
	// keeps a never-active EXPIRED target Pending ("no misfire"), and the flat row
	// can't distinguish that from a was-active downgrade — so Pending matches the
	// gate that was persisted. Apply stays fail-closed while Pending regardless.
	g := mapRawGate(store_RawChangeSet(7, "staging", true, []rawTarget{
		{class: "iam", target: "p1", grant: "g1", state: "EXPIRED", requester: "sa3"},
	}))
	if _, ok := g.Gate.(reconcile.Pending); !ok {
		t.Fatalf("want Pending for EXPIRED reload, got %T", g.Gate)
	}
}

func TestGatherMapsBlockedPendingClean(t *testing.T) {
	bl := mapRawGate(store_RawChangeSet(7, "staging", true, []rawTarget{
		{class: "iam", target: "p1", grant: "g1", state: "DENIED", requester: "sa3"},
	}))
	if _, ok := bl.Gate.(reconcile.Blocked); !ok {
		t.Fatalf("want Blocked for DENIED, got %T", bl.Gate)
	}
	pe := mapRawGate(store_RawChangeSet(7, "staging", true, []rawTarget{
		{class: "iam", target: "p1", grant: "g1", state: "AWAITING", requester: "sa3"},
	}))
	if _, ok := pe.Gate.(reconcile.Pending); !ok {
		t.Fatalf("want Pending for AWAITING, got %T", pe.Gate)
	}
	cl := mapRawGate(store_RawChangeSet(7, "staging", true, nil))
	if _, ok := cl.Gate.(reconcile.Clean); !ok {
		t.Fatalf("want Clean for classified zero-target, got %T", cl.Gate)
	}
}
