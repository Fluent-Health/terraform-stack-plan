package reconcile

import (
	"reflect"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestEventCodecRoundTripsEveryVariant(t *testing.T) {
	for _, e := range corpus() { // corpus() from evolve_test.go: one of every Event variant
		tag, data, err := MarshalEvent(e)
		if err != nil {
			t.Fatalf("marshal %T: %v", e, err)
		}
		got, err := UnmarshalEvent(tag, data)
		if err != nil {
			t.Fatalf("unmarshal %T (tag %q): %v", e, tag, err)
		}
		if !reflect.DeepEqual(got, e) {
			t.Fatalf("round-trip %T: got %#v want %#v", e, got, e)
		}
	}
}

func TestUnmarshalEventUnknownTagErrors(t *testing.T) {
	if _, err := UnmarshalEvent("NoSuchEvent", []byte(`{}`)); err == nil {
		t.Fatal("expected error for unknown event tag")
	}
}

func TestSnapshotRoundTripsEveryGateVariant(t *testing.T) {
	gates := []GateState{
		NotClassified{}, Clean{},
		Pending{Targets: []Target{{Class: "c", Target: "t", GrantName: "g1", Grant: approval.StateAwaiting}}, Lease: Lease{Requester: "sa"}},
		Satisfied{Targets: []Target{{Class: "c", Target: "t", GrantName: "g1", Grant: approval.StateActive}}, Lease: Lease{Requester: "sa"}},
		Blocked{Targets: []Target{{Class: "c", Target: "t", Grant: approval.StateRevoked}}, Lease: Lease{Requester: "sa"}, By: Blocker{Reason: ReasonSlotForeign, ByPR: 9, ByEnv: "prod"}},
	}
	for _, g := range gates {
		cs := ChangeSet{PR: 7, Environment: "nonprod", Gate: g}
		b, err := MarshalSnapshot(cs)
		if err != nil {
			t.Fatalf("marshal snapshot %T: %v", g, err)
		}
		got, err := UnmarshalSnapshot(b)
		if err != nil {
			t.Fatalf("unmarshal snapshot %T: %v", g, err)
		}
		if !reflect.DeepEqual(got, cs) {
			t.Fatalf("snapshot round-trip %T: got %#v want %#v", g, got, cs)
		}
	}
}

// Replay determinism + snapshot(fold)==fold: fold a sequence, snapshot it, reload,
// and confirm equality; fold twice and confirm equality.
func TestReplayDeterminismAndSnapshotEqualsFold(t *testing.T) {
	evs := []Event{
		Classified{Gates: []events.GateTarget{{Class: "iam", Target: "proj-a"}}},
		GateTargetRequested{Class: "iam", Target: "proj-a", Requester: "sa"},
		GrantObserved{Class: "iam", Target: "proj-a", Name: "g1", State: approval.StateActive, Requester: "sa"},
		GateSatisfied{},
	}
	fold := func() ChangeSet {
		st := ChangeSet{PR: 7, Environment: "nonprod"}
		for _, e := range evs {
			st = Evolve(st, e)
		}
		return st
	}
	a, b := fold(), fold()
	if !reflect.DeepEqual(a, b) {
		t.Fatal("fold not deterministic")
	}
	blob, err := MarshalSnapshot(a)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := UnmarshalSnapshot(blob)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded, a) {
		t.Fatalf("snapshot(fold) != fold:\n got %#v\nwant %#v", reloaded, a)
	}
}
