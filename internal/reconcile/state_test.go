package reconcile

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
)

func TestGateStateVariantsImplementInterface(t *testing.T) {
	var gs []GateState = []GateState{
		NotClassified{},
		Clean{},
		Pending{Lease: Lease{Requester: "sa0"}},
		Satisfied{Lease: Lease{Requester: "sa0"}},
		Blocked{By: Blocker{Reason: ReasonDenied}},
	}
	if len(gs) != 5 {
		t.Fatalf("want 5 variants, got %d", len(gs))
	}
}

func TestTargetCarriesObservedGrantState(t *testing.T) {
	tg := Target{Class: "iam", Target: "proj-a", GrantName: "g1", Grant: approval.StateActive}
	if tg.Grant != approval.StateActive {
		t.Fatalf("want ACTIVE, got %q", tg.Grant)
	}
}
