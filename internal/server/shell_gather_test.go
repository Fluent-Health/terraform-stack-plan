package server

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
)

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
