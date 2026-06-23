package reconcile

import (
	"reflect"
	"testing"
)

func TestReactExecEventsRenderNonTerminalAndSSE(t *testing.T) {
	for _, e := range []Event{ExecutionStarted{}, PhaseChanged{}, StackStatusChanged{}} {
		got := React(ChangeSet{}, []Event{e})
		want := []Action{RenderCheckRun{}, PublishSSE{}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%T: got %#v want %#v", e, got, want)
		}
	}
}

func TestReactApplyCleanupNoPresentation(t *testing.T) {
	evs := []Event{
		ClaimReleased{PR: 7, Environment: "nonprod"},
		TargetRevoked{Class: "c", Target: "t", PR: 7, Env: "nonprod"},
		GateReleased{},
	}
	got := React(ChangeSet{}, evs)
	want := []Action{
		ReleaseClaim{PR: 7, Environment: "nonprod"},
		RevokeGrant{Class: "c", Target: "t", PR: 7, Environment: "nonprod"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v (no Render/SSE expected)", got, want)
	}
}
