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
