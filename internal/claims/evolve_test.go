package claims

import (
	"reflect"
	"testing"
)

func TestEvolveAcquireRenewRelease(t *testing.T) {
	s := Empty()
	s = Evolve(s, ClaimAcquired{PR: 7, Stacks: []string{"a", "b"}, ExpiresAt: t0.Add(Lease())})
	if s["a"].PR != 7 || s["b"].PR != 7 {
		t.Fatalf("acquire not folded: %#v", s)
	}
	s = Evolve(s, ClaimRenewed{PR: 7, ExpiresAt: t0.Add(2 * Lease())})
	if !s["a"].ExpiresAt.Equal(t0.Add(2 * Lease())) {
		t.Fatalf("renew not folded: %#v", s)
	}
	s = Evolve(s, ClaimReleased{PR: 7})
	if len(s) != 0 {
		t.Fatalf("release should drop all of PR 7's stacks: %#v", s)
	}
}

func TestEvolveReleaseStack(t *testing.T) {
	s := Evolve(Empty(), ClaimAcquired{PR: 7, Stacks: []string{"a", "b"}, ExpiresAt: t0.Add(Lease())})
	s = Evolve(s, ClaimStackReleased{PR: 7, Stack: "a"})
	if _, ok := s["a"]; ok {
		t.Fatal("stack a should have been dropped")
	}
	if s["b"].PR != 7 {
		t.Fatal("stack b should still be held by PR 7")
	}
}

func TestEvolveReleaseStackWrongPR(t *testing.T) {
	s := Evolve(Empty(), ClaimAcquired{PR: 7, Stacks: []string{"a"}, ExpiresAt: t0.Add(Lease())})
	s = Evolve(s, ClaimStackReleased{PR: 9, Stack: "a"}) // different PR — should be no-op
	if s["a"].PR != 7 {
		t.Fatal("ClaimStackReleased by wrong PR should not drop the stack")
	}
}

func TestEvolvePurity(t *testing.T) {
	// Evolve must NOT mutate its input (ClaimSet is a map — reference type).
	base := Evolve(Empty(), ClaimAcquired{PR: 7, Stacks: []string{"a", "b"}, ExpiresAt: t0.Add(Lease())})
	// Take a snapshot of the input state.
	snapshot := ClaimSet{}
	for k, v := range base {
		snapshot[k] = v
	}

	// Apply operations that would mutate if Evolve is not pure.
	_ = Evolve(base, ClaimRenewed{PR: 7, ExpiresAt: t0.Add(2 * Lease())})
	_ = Evolve(base, ClaimReleased{PR: 7})
	_ = Evolve(base, ClaimStackReleased{PR: 7, Stack: "a"})

	// base must be unchanged.
	if !reflect.DeepEqual(base, snapshot) {
		t.Fatalf("Evolve mutated input: before=%#v after=%#v", snapshot, base)
	}
}
