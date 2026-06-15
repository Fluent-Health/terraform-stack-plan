package reconcile

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestDisplayStatusDerivesGatedSafe(t *testing.T) {
	stack := Stack{Path: "s1", Project: "p1", RunStatus: events.StatusPlanned}
	pend := Pending{Targets: []Target{{Class: "iam", Target: "p1"}}}
	if got := DisplayStatus(stack, pend); got != events.StatusGated {
		t.Fatalf("want gated, got %q", got)
	}
	sat := Satisfied{Targets: []Target{{Class: "iam", Target: "p1", Grant: approval.StateActive}}}
	if got := DisplayStatus(stack, sat); got != events.StatusSafe {
		t.Fatalf("want safe, got %q", got)
	}
	failed := Stack{Path: "s1", Project: "p1", RunStatus: events.StatusFailed}
	if got := DisplayStatus(failed, pend); got != events.StatusFailed {
		t.Fatalf("want failed, got %q", got)
	}
}

func TestApplyGateVerdict(t *testing.T) {
	cases := []struct {
		name string
		gate GateState
		want bool
	}{
		{"not classified fails closed", NotClassified{}, false},
		{"clean passes", Clean{}, true},
		{"pending blocks", Pending{Targets: []Target{{Class: "iam", Target: "p1"}}}, false},
		{"blocked blocks", Blocked{By: Blocker{Reason: ReasonDenied}}, false},
		{"satisfied passes", Satisfied{Targets: []Target{{Class: "iam", Target: "p1", Grant: approval.StateActive}}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ApplyAllowed(c.gate); got != c.want {
				t.Fatalf("ApplyAllowed=%v want %v", got, c.want)
			}
		})
	}
}

func TestRequesterFromGate(t *testing.T) {
	g := Satisfied{Lease: Lease{Requester: "sa3"}, Targets: []Target{{Grant: approval.StateActive}}}
	if Requester(g) != "sa3" {
		t.Fatalf("want sa3, got %q", Requester(g))
	}
}
