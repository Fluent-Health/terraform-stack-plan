package reconcile

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestSignalVariantsImplementInterface(t *testing.T) {
	var sigs []Signal = []Signal{
		RunnerInit{Exec: Execution{ID: "e1"}},
		RunnerPhase{Phase: events.PhasePlanning},
		RunnerUpdate{Stack: "s", Status: events.StatusRunning},
		RunnerFinalize{Gates: []events.GateTarget{{Class: "iam", Target: "p"}}},
		PRClosed{},
		GrantsObserved{Grants: []ObservedGrant{{Class: "iam", Target: "p", State: approval.StateActive}}},
		GateTick{Grants: []ObservedGrant{}},
		ApplySucceeded{},
	}
	if len(sigs) != 8 {
		t.Fatalf("want 8 signals, got %d", len(sigs))
	}
}
