package execution

import (
	"reflect"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func intptr(i int) *int { return &i }

// allEventVariants lists every Event variant; the round-trip test below fails if
// a new variant is added without a codec entry.
func allEventVariants() []Event {
	return []Event{
		Started{Exec: State{ID: "e1", Repo: "r", SHA: "abc", Stacks: []Stack{{Path: "a", RunStatus: events.StatusRunning}}}},
		PhaseChanged{Phase: events.PhaseApplying, Label: "applying", Pct: intptr(50), ID: "e1", PR: 7, Environment: "nonprod", Context: "terraform/nonprod", Repo: "r", SHA: "abc"},
		StackStatusChanged{Stack: "a", Status: events.StatusFailed, Detail: "boom"},
		Failed{},
		Succeeded{},
	}
}

func TestCodecRoundTripsEveryVariant(t *testing.T) {
	for _, ev := range allEventVariants() {
		tag, data, err := MarshalEvent(ev)
		if err != nil {
			t.Fatalf("marshal %T: %v", ev, err)
		}
		got, err := UnmarshalEvent(tag, data)
		if err != nil {
			t.Fatalf("unmarshal %q: %v", tag, err)
		}
		if !reflect.DeepEqual(got, ev) {
			t.Fatalf("round-trip %q mismatch:\n got  %#v\n want %#v", tag, got, ev)
		}
	}
}

func TestCodecUnknownTagErrors(t *testing.T) {
	if _, err := UnmarshalEvent("Nope", []byte(`{}`)); err == nil {
		t.Fatal("want error for unknown tag")
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	s := State{ID: "e1", Phase: events.PhaseApplying, Stacks: []Stack{{Path: "a", RunStatus: events.StatusPlanned}}}
	b, err := MarshalSnapshot(s)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	got, err := UnmarshalSnapshot(b)
	if err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if !reflect.DeepEqual(got, s) {
		t.Fatalf("snapshot round-trip mismatch:\n got  %#v\n want %#v", got, s)
	}
}
