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

// hasAction reports whether actions contains an action of type A.
func hasAction[A Action](actions []Action) bool {
	for _, a := range actions {
		if _, ok := a.(A); ok {
			return true
		}
	}
	return false
}

// actionsOf returns all actions of type A.
func actionsOf[A Action](actions []Action) []A {
	var out []A
	for _, a := range actions {
		if v, ok := a.(A); ok {
			out = append(out, v)
		}
	}
	return out
}

// hasRender reports whether actions contains a RenderCheckRun with the given conclusion.
func hasRender(actions []Action, conclusion string) bool {
	for _, a := range actions {
		if r, ok := a.(RenderCheckRun); ok && r.Conclusion == conclusion {
			return true
		}
	}
	return false
}
