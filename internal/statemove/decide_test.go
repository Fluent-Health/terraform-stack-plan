package statemove

import "testing"

func TestDecide(t *testing.T) {
	if d, err := decide(AddressSet{"aws_s3_bucket.x": "registry.terraform.io/hashicorp/aws"}, AddressSet{}, "aws_s3_bucket.x", "aws_s3_bucket.x"); err != nil || d != DecisionMove {
		t.Errorf("source-only => move; got %v %v", d, err)
	}
	if d, err := decide(AddressSet{}, AddressSet{"a.b": "registry.terraform.io/hashicorp/aws"}, "a.b", "a.b"); err != nil || d != DecisionSkip {
		t.Errorf("dest-only => skip; got %v %v", d, err)
	}
	if _, err := decide(AddressSet{"a.b": "registry.terraform.io/hashicorp/aws"}, AddressSet{"a.b": "registry.terraform.io/hashicorp/aws"}, "a.b", "a.b"); err == nil {
		t.Error("both => error (ambiguous)")
	}
	if _, err := decide(AddressSet{}, AddressSet{}, "a.b", "a.b"); err == nil {
		t.Error("neither => error")
	}
}

func TestExpandPairs(t *testing.T) {
	// Whole-module pair fans out to the children present in the source state.
	src := AddressSet{
		"module.a[0].res.x":            "registry.terraform.io/hashicorp/aws",
		"module.a[0].res.y[\"k\"]":     "registry.terraform.io/hashicorp/aws",
		"module.a[0].module.sub.res.z": "registry.terraform.io/hashicorp/aws",
	}
	got := expandPairs(src, AddressSet{}, []Move{{From: "module.a[0]", To: "module.b"}})
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
	dst := AddressSet{"module.b.res.x": "registry.terraform.io/hashicorp/aws"}
	got = expandPairs(AddressSet{}, dst, []Move{{From: "module.a[0]", To: "module.b"}})
	if len(got) != 1 || got[0] != (Move{From: "module.a[0].res.x", To: "module.b.res.x"}) {
		t.Errorf("already-moved expand = %+v", got)
	}

	// Exact resource pair resolves to itself.
	got = expandPairs(AddressSet{"aws_s3_bucket.x": "registry.terraform.io/hashicorp/aws"}, AddressSet{}, []Move{{From: "aws_s3_bucket.x", To: "aws_s3_bucket.x"}})
	if len(got) != 1 || got[0] != (Move{From: "aws_s3_bucket.x", To: "aws_s3_bucket.x"}) {
		t.Errorf("exact pair = %+v", got)
	}

	// Neither side present: kept verbatim so decide() fails closed.
	got = expandPairs(AddressSet{}, AddressSet{}, []Move{{From: "module.a[0]", To: "module.b"}})
	if len(got) != 1 || got[0] != (Move{From: "module.a[0]", To: "module.b"}) {
		t.Errorf("missing pair = %+v", got)
	}
}
