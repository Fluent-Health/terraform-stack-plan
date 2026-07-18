package reconcile

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestSignalVariantsImplementInterface(t *testing.T) {
	var sigs []Signal = []Signal{
		RunnerFinalize{Gates: []events.GateTarget{{Class: "iam", Target: "p"}}},
		PRClosed{},
		GrantsObserved{Grants: []ObservedGrant{{Class: "iam", Target: "p", State: approval.StateActive}}},
		GateTick{Grants: []ObservedGrant{}},
		ApplySucceeded{},
	}
	if len(sigs) != 5 {
		t.Fatalf("want 5 signals, got %d", len(sigs))
	}
}
