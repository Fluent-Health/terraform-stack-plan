package claims

import (
	"reflect"
	"testing"
)

func TestClaimCodecRoundTrip(t *testing.T) {
	for _, e := range []Event{
		ClaimAcquired{PR: 7, Stacks: []string{"a"}, ExpiresAt: t0},
		ClaimRenewed{PR: 7, ExpiresAt: t0},
		ClaimReleased{PR: 7},
		ClaimStackReleased{PR: 7, Stack: "a"},
	} {
		tag, data, err := MarshalEvent(e)
		if err != nil {
			t.Fatal(err)
		}
		got, err := UnmarshalEvent(tag, data)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, e) {
			t.Fatalf("%T round-trip: got %#v", e, got)
		}
	}
}

func TestClaimSnapshotRoundTrip(t *testing.T) {
	cs := Evolve(Empty(), ClaimAcquired{PR: 7, Stacks: []string{"a", "b"}, ExpiresAt: t0})
	b, err := MarshalSnapshot(cs)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnmarshalSnapshot(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, cs) {
		t.Fatalf("snapshot round-trip: got %#v want %#v", got, cs)
	}
}

func TestClaimCodecUnknownTag(t *testing.T) {
	_, err := UnmarshalEvent("NoSuchEvent", []byte(`{}`))
	if err == nil {
		t.Fatal("expected error for unknown event tag")
	}
}

func TestClaimCodecReplayDeterminism(t *testing.T) {
	// Replay the same event sequence twice; both folds must produce equal state.
	events := []Event{
		ClaimAcquired{PR: 7, Stacks: []string{"a", "b"}, ExpiresAt: t0.Add(Lease())},
		ClaimRenewed{PR: 7, ExpiresAt: t0.Add(2 * Lease())},
		ClaimStackReleased{PR: 7, Stack: "b"},
	}
	fold := func() ClaimSet {
		s := Empty()
		for _, e := range events {
			s = Evolve(s, e)
		}
		return s
	}
	s1, s2 := fold(), fold()
	if !reflect.DeepEqual(s1, s2) {
		t.Fatalf("replay non-deterministic: %#v vs %#v", s1, s2)
	}
}

func TestClaimSnapshotEqualsDirectFold(t *testing.T) {
	// MarshalSnapshot(fold(events)) round-trips to the same state as fold(events).
	s := Empty()
	s = Evolve(s, ClaimAcquired{PR: 7, Stacks: []string{"x"}, ExpiresAt: t0.Add(Lease())})
	s = Evolve(s, ClaimRenewed{PR: 7, ExpiresAt: t0.Add(2 * Lease())})

	b, err := MarshalSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnmarshalSnapshot(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, s) {
		t.Fatalf("snapshot != fold: got %#v want %#v", got, s)
	}
}
