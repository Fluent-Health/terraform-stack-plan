package reconcile

import "testing"

func TestStepUnknownSignalIsNoOp(t *testing.T) {
	prior := ChangeSet{PR: 7, Environment: "staging", Gate: NotClassified{}}
	got, actions := Step(World{Prior: prior}, ApplySucceeded{})
	// ApplySucceeded on a NotClassified gate has nothing to revoke.
	if len(actions) != 0 {
		t.Fatalf("want no actions, got %v", actions)
	}
	if _, ok := got.Gate.(NotClassified); !ok {
		t.Fatalf("want gate unchanged NotClassified, got %T", got.Gate)
	}
}
