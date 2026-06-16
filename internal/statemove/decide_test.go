package statemove

import "testing"

func TestDecide(t *testing.T) {
	if d, err := decide(map[string]bool{"aws_s3_bucket.x": true}, map[string]bool{}, "aws_s3_bucket.x", "aws_s3_bucket.x"); err != nil || d != DecisionMove {
		t.Errorf("source-only => move; got %v %v", d, err)
	}
	if d, err := decide(map[string]bool{}, map[string]bool{"a.b": true}, "a.b", "a.b"); err != nil || d != DecisionSkip {
		t.Errorf("dest-only => skip; got %v %v", d, err)
	}
	if _, err := decide(map[string]bool{"a.b": true}, map[string]bool{"a.b": true}, "a.b", "a.b"); err == nil {
		t.Error("both => error (ambiguous)")
	}
	if _, err := decide(map[string]bool{}, map[string]bool{}, "a.b", "a.b"); err == nil {
		t.Error("neither => error")
	}
}

func TestExpandPairs(t *testing.T) {
	// Whole-module pair fans out to the children present in the source state.
	src := map[string]bool{
		"module.a[0].res.x":            true,
		"module.a[0].res.y[\"k\"]":     true,
		"module.a[0].module.sub.res.z": true,
	}
	got := expandPairs(src, map[string]bool{}, []Move{{From: "module.a[0]", To: "module.b"}})
	want := []Move{
		{From: "module.a[0].module.sub.res.z", To: "module.b.module.sub.res.z"},
		{From: "module.a[0].res.x", To: "module.b.res.x"},
		{From: "module.a[0].res.y[\"k\"]", To: "module.b.res.y[\"k\"]"},
	}
	if len(got) != len(want) {
		t.Fatalf("expand module pair: got %d pairs, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pair %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	// Already-moved (source empty, children under `to` in dest): re-keyed so decide -> Skip.
	dst := map[string]bool{"module.b.res.x": true}
	got = expandPairs(map[string]bool{}, dst, []Move{{From: "module.a[0]", To: "module.b"}})
	if len(got) != 1 || got[0] != (Move{From: "module.a[0].res.x", To: "module.b.res.x"}) {
		t.Errorf("already-moved expand = %+v", got)
	}

	// Exact resource pair resolves to itself.
	got = expandPairs(map[string]bool{"aws_s3_bucket.x": true}, map[string]bool{}, []Move{{From: "aws_s3_bucket.x", To: "aws_s3_bucket.x"}})
	if len(got) != 1 || got[0] != (Move{From: "aws_s3_bucket.x", To: "aws_s3_bucket.x"}) {
		t.Errorf("exact pair = %+v", got)
	}

	// Neither side present: kept verbatim so decide() fails closed.
	got = expandPairs(map[string]bool{}, map[string]bool{}, []Move{{From: "module.a[0]", To: "module.b"}})
	if len(got) != 1 || got[0] != (Move{From: "module.a[0]", To: "module.b"}) {
		t.Errorf("missing pair = %+v", got)
	}
}
