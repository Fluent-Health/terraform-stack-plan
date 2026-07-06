package claims

import (
	"reflect"
	"testing"
)

func TestHeldOverlapAndExpiry(t *testing.T) {
	s := Evolve(Empty(), ClaimAcquired{PR: 9, Stacks: []string{"a"}, ExpiresAt: t0.Add(Lease())})
	// PR 7 wants "a","c": "a" is held by PR 9 (unexpired at t0).
	v := Held(s, 7, []string{"a", "c"}, t0)
	if !v.Held || !reflect.DeepEqual(v.Blocking, []string{"a"}) {
		t.Fatalf("want held on [a], got %#v", v)
	}
	// Same PR's own claim never blocks itself.
	if Held(s, 9, []string{"a"}, t0).Held {
		t.Fatal("a PR must not be blocked by its own claim")
	}
	// After expiry, the claim no longer holds (read-time, no event).
	if Held(s, 7, []string{"a"}, t0.Add(2*Lease())).Held {
		t.Fatal("expired claim must not hold")
	}
}

func TestHeldMultipleBlockers(t *testing.T) {
	s := Empty()
	s = Evolve(s, ClaimAcquired{PR: 9, Stacks: []string{"a", "b"}, ExpiresAt: t0.Add(Lease())})
	v := Held(s, 7, []string{"b", "a", "c"}, t0)
	if !v.Held {
		t.Fatal("should be held")
	}
	// Blocking must be sorted for determinism.
	want := []string{"a", "b"}
	if !reflect.DeepEqual(v.Blocking, want) {
		t.Fatalf("blocking not sorted: got %v want %v", v.Blocking, want)
	}
}

func TestHeldNilBlockingWhenClear(t *testing.T) {
	s := Empty()
	v := Held(s, 7, []string{"a"}, t0)
	if v.Held {
		t.Fatal("empty set should not be held")
	}
	if v.Blocking != nil {
		t.Fatalf("Blocking should be nil when not held, got %v", v.Blocking)
	}
}

func TestHeldReportsBlockingPRs(t *testing.T) {
	now := t0
	s := ClaimSet{
		"stacks/a": {PR: 9, ExpiresAt: now.Add(Lease())},
		"stacks/b": {PR: 3, ExpiresAt: now.Add(Lease())},
		"stacks/c": {PR: 9, ExpiresAt: now.Add(Lease())},
	}
	v := Held(s, 7, []string{"stacks/a", "stacks/b", "stacks/c"}, now)
	if !v.Held {
		t.Fatal("want held")
	}
	if !reflect.DeepEqual(v.BlockingPRs, []int{3, 9}) {
		t.Errorf("BlockingPRs = %v, want [3 9] (sorted, de-duplicated)", v.BlockingPRs)
	}
}
