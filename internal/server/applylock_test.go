package server

import "testing"

func TestOverlap(t *testing.T) {
	claimed := map[string]int{"a": 5, "b": 5, "c": 9}
	// PR 7 touching b,d → b is claimed by another PR (5) ⇒ blocking.
	got := overlap(claimed, []string{"b", "d"}, 7)
	if len(got) != 1 || got[0] != "b" {
		t.Fatalf("overlap = %v, want [b]", got)
	}
	// A PR's own claim does not block itself.
	if g := overlap(claimed, []string{"a"}, 5); len(g) != 0 {
		t.Fatalf("self-claim blocked: %v", g)
	}
	// Disjoint ⇒ no overlap.
	if g := overlap(claimed, []string{"d", "e"}, 7); len(g) != 0 {
		t.Fatalf("disjoint blocked: %v", g)
	}
}
