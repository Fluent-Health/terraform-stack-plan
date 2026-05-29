package model

import "testing"

func TestCountsTotal(t *testing.T) {
	c := Counts{Add: 1, Change: 2, Destroy: 3, Replace: 4}
	if got := c.Total(); got != 10 {
		t.Fatalf("Total() = %d, want 10", got)
	}
}

func TestCountsAnyChange(t *testing.T) {
	if (Counts{}).AnyChange() {
		t.Fatal("empty Counts should report no change")
	}
	if !(Counts{Change: 1}).AnyChange() {
		t.Fatal("Counts with a change should report AnyChange")
	}
}
