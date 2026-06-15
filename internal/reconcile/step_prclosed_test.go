package reconcile

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
)

func TestPRClosedRevokesAndTerminalizes(t *testing.T) {
	prior := ChangeSet{PR: 7, Environment: "staging", Gate: Satisfied{
		Lease:   Lease{Requester: "sa3"},
		Targets: []Target{{Class: "iam", Target: "p1", GrantName: "g1", Grant: approval.StateActive}},
	}}
	got, actions := Step(World{Prior: prior}, PRClosed{})
	revs := actionsOf[RevokeGrant](actions)
	if len(revs) != 1 || revs[0].Target != "p1" || revs[0].PR != 7 {
		t.Fatalf("want revoke p1 for PR 7, got %+v", revs)
	}
	b, ok := got.Gate.(Blocked)
	if !ok || b.By.Reason != ReasonRevoked {
		t.Fatalf("want Blocked{revoked} after close, got %T %+v", got.Gate, got.Gate)
	}
}

func TestPRClosedOnCleanIsNoOp(t *testing.T) {
	got, actions := Step(World{Prior: ChangeSet{PR: 7, Environment: "staging", Gate: Clean{}}}, PRClosed{})
	if len(actions) != 0 {
		t.Fatalf("want no actions, got %v", actions)
	}
	if _, ok := got.Gate.(Clean); !ok {
		t.Fatalf("want Clean unchanged, got %T", got.Gate)
	}
}
