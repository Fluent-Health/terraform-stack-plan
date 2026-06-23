package reconcile

import (
	"reflect"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// --- RunnerFinalize React tests ---

func TestReactExecutionFailedRendersFailure(t *testing.T) {
	got := React(ChangeSet{}, []Event{ExecutionFailed{}})
	want := []Action{RenderCheckRun{Terminal: true, Conclusion: "failure"}, PublishSSE{}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestReactGatePassedRendersSuccess(t *testing.T) {
	got := React(ChangeSet{Gate: Clean{}}, []Event{StacksClassified{}, GatePassed{}})
	want := []Action{RenderCheckRun{Terminal: true, Conclusion: "success"}, PublishSSE{}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestReactGatedFinalizeRequestsAndRenders(t *testing.T) {
	evs := []Event{
		StacksClassified{},
		Classified{Gates: []events.GateTarget{{Class: "c", Target: "t"}}},
		GateTargetRequested{Class: "c", Target: "t"},
	}
	got := React(ChangeSet{Gate: Pending{}}, evs)
	want := []Action{
		RequestGrant{Class: "c", Target: "t"},
		RenderCheckRun{},
		PublishSSE{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestReactFailurePrecedenceOverInProgress(t *testing.T) {
	// ExecutionFailed seen alongside GateTargetRequested: failure wins.
	evs := []Event{
		ExecutionFailed{},
		GateTargetRequested{Class: "c", Target: "t"},
	}
	got := React(ChangeSet{}, evs)
	// Should only have RenderCheckRun{Terminal:true, Conclusion:"failure"} + SSE
	// (no RequestGrant because ExecutionFailed path doesn't emit request)
	var renders []RenderCheckRun
	for _, a := range got {
		if r, ok := a.(RenderCheckRun); ok {
			renders = append(renders, r)
		}
	}
	if len(renders) != 1 {
		t.Fatalf("want exactly 1 RenderCheckRun, got %#v", got)
	}
	if !renders[0].Terminal || renders[0].Conclusion != "failure" {
		t.Fatalf("want failure terminal, got %#v", renders[0])
	}
}

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

func TestReactPRClosedSSEOnly(t *testing.T) {
	evs := []Event{
		TargetRevoked{Class: "c", Target: "t", PR: 7, Env: "nonprod"},
		PRClosedRecorded{},
		GateBlocked{Reason: ReasonRevoked},
	}
	got := React(ChangeSet{Gate: Blocked{}}, evs)
	want := []Action{
		RevokeGrant{Class: "c", Target: "t", PR: 7, Environment: "nonprod"},
		PublishSSE{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}
