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

func TestActionMutates(t *testing.T) {
	cases := []struct {
		action Action
		want   bool
	}{
		{ActionAdd, true},
		{ActionChange, true},
		{ActionDestroy, true},
		{ActionReplace, true},
		{ActionForget, false},
		{ActionNoop, false},
		{Action(""), false}, // zero value
	}
	for _, c := range cases {
		if got := c.action.Mutates(); got != c.want {
			t.Errorf("Action(%q).Mutates() = %v, want %v", c.action, got, c.want)
		}
	}
}

func TestLinkFields(t *testing.T) {
	r := Report{HeaderLinks: []Link{{Label: "PR #1", URL: "https://x/1"}}}
	r.Stacks = []Stack{{Name: "s", URL: "https://x/s"}}
	r.Stacks[0].Changes = []Change{{Address: "a.b", URL: "https://x/a"}}
	if r.HeaderLinks[0].URL != "https://x/1" || r.Stacks[0].URL == "" || r.Stacks[0].Changes[0].URL == "" {
		t.Fatal("link fields not wired")
	}
}

func TestLeafOpSym(t *testing.T) {
	cases := []struct {
		op   LeafOp
		want string
	}{
		{OpAdd, "+"},
		{OpRemove, "-"},
		{OpChange, "~"},
	}
	for _, c := range cases {
		if got := c.op.Sym(); got != c.want {
			t.Errorf("LeafOp(%d).Sym() = %q, want %q", c.op, got, c.want)
		}
	}
}

func TestFieldSelAndAtLast(t *testing.T) {
	v1 := Variant{Level: LevelStructural, Content: "short"}
	v2 := Variant{Level: LevelLineDiff, Content: "long"}
	f := Field{
		Name:     "data",
		Variants: []Variant{v1, v2},
		Selected: 0,
	}
	if got := f.Sel(); got.Content != "short" {
		t.Errorf("Sel() = %v, want %v", got, v1)
	}
	if f.AtLast() {
		t.Errorf("AtLast() = true, want false (selected 0 out of 2)")
	}
	f.Selected = 1
	if !f.AtLast() {
		t.Errorf("AtLast() = false, want true (selected 1 out of 2)")
	}
}
