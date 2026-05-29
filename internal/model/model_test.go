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

func TestLeafValue(t *testing.T) {
	cases := []struct {
		name string
		leaf Leaf
		want string
	}{
		{"add", Leaf{Op: OpAdd, Path: "team", New: `"platform"`}, `"platform"`},
		{"remove", Leaf{Op: OpRemove, Path: "id", Old: `"x"`}, `"x"`},
		{"change", Leaf{Op: OpChange, Path: "n", Old: "7", New: "30"}, "7 → 30"},
		{"inline override", Leaf{Op: OpChange, Path: "pw", Old: "a", New: "b", Inline: "(sensitive value)"}, "(sensitive value)"},
	}
	for _, c := range cases {
		if got := c.leaf.Value(); got != c.want {
			t.Errorf("%s: Value() = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestFieldIsBlock(t *testing.T) {
	leafField := Field{Name: "labels", Leaves: []Leaf{{Op: OpAdd, Path: "labels.team", New: `"x"`}}}
	if leafField.IsBlock() {
		t.Errorf("leaf field should not be a block")
	}
	blockField := Field{Name: "data", Variants: []Variant{{Level: LevelLineDiff, Content: "x"}}}
	if !blockField.IsBlock() {
		t.Errorf("variant field should be a block")
	}
}
