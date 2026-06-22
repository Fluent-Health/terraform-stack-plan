package reconcile

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
)

// A no-gate apply (Clean gate) still holds a stack claim — ApplySucceeded must
// release it. This is the case the premature-release bug could not express,
// because release lived in the finalize handler, not the apply-done transition.
func TestApplySucceededReleasesClaimOnClean(t *testing.T) {
	got, actions := Step(World{Prior: ChangeSet{PR: 7, Environment: "staging", Gate: Clean{}}}, ApplySucceeded{})
	rel := actionsOf[ReleaseClaim](actions)
	if len(rel) != 1 || rel[0].PR != 7 || rel[0].Environment != "staging" {
		t.Fatalf("want one ReleaseClaim{7,staging}, got %+v", rel)
	}
	if _, ok := got.Gate.(Clean); !ok {
		t.Fatalf("gate should stay Clean, got %T", got.Gate)
	}
}

// A gated apply releases BOTH the claim and the grant when it finishes.
func TestApplySucceededReleasesClaimAndGrant(t *testing.T) {
	prior := ChangeSet{PR: 7, Environment: "staging", Gate: Satisfied{
		Lease:   Lease{Requester: "sa3"},
		Targets: []Target{{Class: "iam", Target: "p1", GrantName: "g1", Grant: approval.StateActive}},
	}}
	got, actions := Step(World{Prior: prior}, ApplySucceeded{})
	if len(actionsOf[ReleaseClaim](actions)) != 1 {
		t.Fatalf("want one ReleaseClaim, got %+v", actions)
	}
	if len(actionsOf[RevokeGrant](actions)) != 1 {
		t.Fatalf("want one RevokeGrant, got %+v", actions)
	}
	if _, ok := got.Gate.(Clean); !ok {
		t.Fatalf("want Clean after apply, got %T", got.Gate)
	}
}

// A NotClassified changeset never planned/merged, so no apply ran and no claim
// was ever acquired — ApplySucceeded stays a no-op (no spurious ReleaseClaim).
func TestApplySucceededOnNotClassifiedNoOp(t *testing.T) {
	got, actions := Step(World{Prior: ChangeSet{PR: 7, Environment: "staging", Gate: NotClassified{}}}, ApplySucceeded{})
	if len(actions) != 0 {
		t.Fatalf("NotClassified ApplySucceeded must be a no-op, got %+v", actions)
	}
	if _, ok := got.Gate.(NotClassified); !ok {
		t.Fatalf("gate should stay NotClassified, got %T", got.Gate)
	}
}
