package statemove

import (
	"os"
	"path/filepath"
	"testing"
)

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

func TestValidateMovePlan(t *testing.T) {
	src := AddressSet{"google_artifact_registry_repository.main": "registry.terraform.io/hashicorp/google"}
	dst := AddressSet{}
	providers := DestProviders{"google": true}

	// 1. Success case (apply-time)
	m := XMove{Pairs: []Move{{From: "google_artifact_registry_repository.main", To: "google_artifact_registry_repository.main"}}}
	diags := ValidateMovePlan(src, dst, providers, m, true)
	if len(diags) != 0 {
		t.Errorf("expected 0 diags, got: %+v", diags)
	}

	// 2. Missing provider case (apply-time)
	diags = ValidateMovePlan(src, dst, DestProviders{}, m, true)
	if len(diags) != 1 || diags[0].Code != "xmove/provider-missing" {
		t.Errorf("expected provider-missing, got: %+v", diags)
	}

	// 3. Phantom index mismatch case (apply-time)
	mWrong := XMove{Pairs: []Move{{From: "google_artifact_registry_repository.main[0]", To: "google_artifact_registry_repository.main"}}}
	diags = ValidateMovePlan(src, dst, providers, mWrong, true)
	if len(diags) != 1 || diags[0].Code != "xmove/source-missing" {
		t.Errorf("expected source-missing due to phantom index, got: %+v", diags)
	}

	// 4. Occupied destination case (apply-time)
	dstOccupied := AddressSet{"google_artifact_registry_repository.main": "registry.terraform.io/hashicorp/google"}
	diags = ValidateMovePlan(src, dstOccupied, providers, m, true)
	if len(diags) != 1 || diags[0].Code != "xmove/dest-occupied" {
		t.Errorf("expected dest-occupied, got: %+v", diags)
	}

	// 5. Success case (plan-time)
	dstPlan := AddressSet{"google_artifact_registry_repository.main": "registry.terraform.io/hashicorp/google"}
	diagsPlan := ValidateMovePlan(src, dstPlan, providers, m, false)
	if len(diagsPlan) != 0 {
		t.Errorf("expected 0 plan-time diags, got: %+v", diagsPlan)
	}

	// 6. Missing source case (plan-time)
	srcEmpty := AddressSet{}
	diagsPlan = ValidateMovePlan(srcEmpty, dstPlan, providers, m, false)
	if len(diagsPlan) != 1 || diagsPlan[0].Code != "xmove/source-missing" {
		t.Errorf("expected source-missing at plan-time, got: %+v", diagsPlan)
	}
}

func TestDiscoverDestProviders(t *testing.T) {
	tmp := t.TempDir()
	tfFile := filepath.Join(tmp, "main.tf")
	hclContent := `
provider "google" {
  project = "my-project"
}
provider "postgresql" {}
`
	if err := os.WriteFile(tfFile, []byte(hclContent), 0644); err != nil {
		t.Fatalf("failed to write tf file: %v", err)
	}

	providers := DiscoverDestProviders(tmp)
	if !providers["google"] || !providers["postgresql"] || len(providers) != 2 {
		t.Errorf("expected google and postgresql, got: %+v", providers)
	}
}
