package statemove

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
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

	// Longest-prefix-wins: if a more specific explicit move exists, the broader wildcard must not expand it.
	srcLongest := AddressSet{
		"module.a[0].res.x": "registry.terraform.io/hashicorp/aws",
		"module.a[0].res.y": "registry.terraform.io/hashicorp/aws",
	}
	pairsLongest := []Move{
		{From: "module.a[0].res.y", To: "module.b.res.z"},
		{From: "module.a[0]", To: "module.b"},
	}
	got = expandPairs(srcLongest, AddressSet{}, pairsLongest)
	wantLongest := []Move{
		{From: "module.a[0].res.y", To: "module.b.res.z"},
		{From: "module.a[0].res.x", To: "module.b.res.x"},
	}
	if len(got) != len(wantLongest) {
		t.Fatalf("longest-prefix-wins: got %d pairs, want %d: %+v", len(got), len(wantLongest), got)
	}
	sort.Slice(got, func(i, j int) bool { return got[i].From < got[j].From })
	sort.Slice(wantLongest, func(i, j int) bool { return wantLongest[i].From < wantLongest[j].From })
	for i := range wantLongest {
		if got[i] != wantLongest[i] {
			t.Errorf("longest-prefix-wins pair %d = %+v, want %+v", i, got[i], wantLongest[i])
		}
	}
}

func TestValidateMovePlan(t *testing.T) {
	src := AddressSet{"google_artifact_registry_repository.main": "registry.terraform.io/hashicorp/google"}
	dst := AddressSet{}
	providers := DestProviders{"google": true}

	// 1. Success case (apply-time)
	m := XMove{Pairs: []Move{{From: "google_artifact_registry_repository.main", To: "google_artifact_registry_repository.main"}}}
	diags := ValidateMovePlan(src, dst, nil, providers, m, true)
	if len(diags) != 0 {
		t.Errorf("expected 0 diags, got: %+v", diags)
	}

	// 2. Missing provider case (apply-time)
	diags = ValidateMovePlan(src, dst, nil, DestProviders{}, m, true)
	if len(diags) != 1 || diags[0].Code != "xmove/provider-missing" {
		t.Errorf("expected provider-missing, got: %+v", diags)
	}

	// 3. Phantom index mismatch case (apply-time)
	mWrong := XMove{Pairs: []Move{{From: "google_artifact_registry_repository.main[0]", To: "google_artifact_registry_repository.main"}}}
	diags = ValidateMovePlan(src, dst, nil, providers, mWrong, true)
	if len(diags) != 1 || diags[0].Code != "xmove/source-missing" {
		t.Errorf("expected source-missing due to phantom index, got: %+v", diags)
	}

	// 4. Occupied destination case (apply-time)
	dstOccupied := AddressSet{"google_artifact_registry_repository.main": "registry.terraform.io/hashicorp/google"}
	diags = ValidateMovePlan(src, dstOccupied, nil, providers, m, true)
	if len(diags) != 1 || diags[0].Code != "xmove/dest-occupied" {
		t.Errorf("expected dest-occupied, got: %+v", diags)
	}

	// 5. Success case (plan-time)
	dstPlan := AddressSet{"google_artifact_registry_repository.main": "registry.terraform.io/hashicorp/google"}
	diagsPlan := ValidateMovePlan(src, dstPlan, nil, providers, m, false)
	if len(diagsPlan) != 0 {
		t.Errorf("expected 0 plan-time diags, got: %+v", diagsPlan)
	}

	// 6. Missing source case (plan-time)
	srcEmpty := AddressSet{}
	diagsPlan = ValidateMovePlan(srcEmpty, dstPlan, nil, providers, m, false)
	if len(diagsPlan) != 1 || diagsPlan[0].Code != "xmove/source-missing" {
		t.Errorf("expected source-missing at plan-time, got: %+v", diagsPlan)
	}
}

func TestValidateMovePlan_SpentEntry(t *testing.T) {
	providers := DestProviders{"google": true}

	// Exact pair already applied: From gone from source, To in dest prior_state
	// → xmove/spent warning, no error, and no dest-missing from the changes set.
	m := XMove{Pairs: []Move{{From: "google_storage_bucket.a", To: "module.dst.google_storage_bucket.a"}}}
	srcEmpty := AddressSet{}
	dstChanges := AddressSet{"google_storage_bucket.other": "registry.terraform.io/hashicorp/google"}
	dstPrior := AddressSet{"module.dst.google_storage_bucket.a": "registry.terraform.io/hashicorp/google"}
	diags := ValidateMovePlan(srcEmpty, dstChanges, dstPrior, providers, m, false)
	if len(diags) != 1 || diags[0].Code != "xmove/spent" || diags[0].Severity != SeverityWarning {
		t.Errorf("expected single xmove/spent warning, got: %+v", diags)
	}

	// Module-level pair already applied: the pair does not expand (nothing under
	// From in source, nothing under To in changes) but dest prior_state holds a
	// child of To → spent.
	mMod := XMove{Pairs: []Move{{From: "module.a", To: "module.b"}}}
	dstPriorMod := AddressSet{"module.b.google_storage_bucket.x": "registry.terraform.io/hashicorp/google"}
	diags = ValidateMovePlan(srcEmpty, AddressSet{}, dstPriorMod, providers, mMod, false)
	if len(diags) != 1 || diags[0].Code != "xmove/spent" {
		t.Errorf("expected xmove/spent for module-level pair, got: %+v", diags)
	}

	// Genuinely stale: From gone from source and dest prior_state does NOT hold
	// To → hard error, unchanged behavior.
	dstPriorUnrelated := AddressSet{"module.other.google_storage_bucket.y": "registry.terraform.io/hashicorp/google"}
	diags = ValidateMovePlan(srcEmpty, AddressSet{}, dstPriorUnrelated, providers, m, false)
	if len(diags) != 1 || diags[0].Code != "xmove/source-missing" || diags[0].Severity != SeverityError {
		t.Errorf("expected xmove/source-missing error, got: %+v", diags)
	}

	// Mixed manifest: one pending pair (still in source) and one spent pair —
	// the spent entry must not fail the pending one.
	mMixed := XMove{Pairs: []Move{
		{From: "google_storage_bucket.pending", To: "module.dst.google_storage_bucket.pending"},
		{From: "google_storage_bucket.a", To: "module.dst.google_storage_bucket.a"},
	}}
	src := AddressSet{"google_storage_bucket.pending": "registry.terraform.io/hashicorp/google"}
	dstPending := AddressSet{"module.dst.google_storage_bucket.pending": "registry.terraform.io/hashicorp/google"}
	diags = ValidateMovePlan(src, dstPending, dstPrior, providers, mMixed, false)
	if len(diags) != 1 || diags[0].Code != "xmove/spent" {
		t.Errorf("expected only xmove/spent for the applied pair, got: %+v", diags)
	}
}

func TestValidateMovePlan_BuiltinTerraformProvider(t *testing.T) {
	// terraform_data is backed by the built-in provider
	// terraform.io/builtin/terraform, which cannot be declared in a
	// required_providers or provider block. A move of such a resource must not
	// trip the destination-provider check even though the destination has no
	// "terraform" provider configured.
	src := AddressSet{"module.analytics.terraform_data.loinc_csv_file": "terraform.io/builtin/terraform"}
	m := XMove{Pairs: []Move{{From: "module.analytics.terraform_data.loinc_csv_file", To: "module.analytics.terraform_data.loinc_csv_file"}}}

	// Apply-time: destination has no providers configured at all.
	dstApply := AddressSet{}
	diags := ValidateMovePlan(src, dstApply, nil, DestProviders{}, m, true)
	if len(diags) != 0 {
		t.Errorf("apply-time: expected 0 diags for builtin terraform provider, got: %+v", diags)
	}

	// Plan-time: destination plan has the resource but no "terraform" provider.
	dstPlan := AddressSet{"module.analytics.terraform_data.loinc_csv_file": "terraform.io/builtin/terraform"}
	diags = ValidateMovePlan(src, dstPlan, nil, DestProviders{}, m, false)
	if len(diags) != 0 {
		t.Errorf("plan-time: expected 0 diags for builtin terraform provider, got: %+v", diags)
	}
}

func TestValidateMovePlan_ProviderMismatch(t *testing.T) {
	src := AddressSet{"module.a.google_project_iam_member.x": "registry.terraform.io/hashicorp/google"}
	dst := AddressSet{"module.b.aws_iam_role.x": "registry.terraform.io/hashicorp/aws"}
	providers := DestProviders{"aws": true}
	xm := XMove{Pairs: []Move{{From: "module.a", To: "module.b"}}}
	diags := ValidateMovePlan(src, dst, nil, providers, xm, false)
	var got []string
	for _, d := range diags {
		got = append(got, d.Code)
	}
	if !slices.Contains(got, "xmove/provider-mismatch") {
		t.Errorf("expected xmove/provider-mismatch, got %v", got)
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

// D3: IsSpent reports true when all declared To-addresses are already present in
// the destination prior_state (move has been applied), false when any are absent.
func TestIsSpent(t *testing.T) {
	pairs := []Move{{From: "module.x", To: "module.y"}}

	dstHas := AddressSet{
		"module.y.google_project.p": "registry.terraform.io/hashicorp/google",
		"module.y.aws_s3_bucket.b":  "registry.terraform.io/hashicorp/aws",
	}
	if !IsSpent(pairs, dstHas) {
		t.Error("IsSpent must be true when dest prior_state has to-addresses")
	}
	if IsSpent(pairs, AddressSet{}) {
		t.Error("IsSpent must be false when dest prior_state is empty")
	}
	if IsSpent(pairs, AddressSet{"module.z.google_project.p": "p"}) {
		t.Error("IsSpent must be false when dest has different module")
	}

	// Exact resource pair.
	if !IsSpent([]Move{{From: "a.x", To: "a.x"}}, AddressSet{"a.x": "p"}) {
		t.Error("IsSpent must be true for exact pair in dest")
	}

	// Multi-pair: all must be present.
	multi := []Move{{From: "module.a", To: "module.b"}, {From: "module.c", To: "module.d"}}
	if IsSpent(multi, AddressSet{"module.b.r.x": "p"}) {
		t.Error("IsSpent must be false when only one of two pairs is in dest")
	}
	if !IsSpent(multi, AddressSet{"module.b.r.x": "p", "module.d.r.y": "p"}) {
		t.Error("IsSpent must be true when all pairs are in dest")
	}
}

// D2: stateAddresses must exclude data sources so module-level wildcards never
// sweep data.* addresses into the move set (failure #1 and #2 from tsp#165).
func TestStateAddressesSkipsDataSources(t *testing.T) {
	s := &tfjson.State{
		Values: &tfjson.StateValues{
			RootModule: &tfjson.StateModule{
				Resources: []*tfjson.StateResource{
					{
						Address:      "google_project.p",
						Mode:         tfjson.ManagedResourceMode,
						ProviderName: "registry.terraform.io/hashicorp/google",
					},
					{
						Address:      "data.google_secret_manager_secret_version.x",
						Mode:         tfjson.DataResourceMode,
						ProviderName: "registry.terraform.io/hashicorp/google",
					},
				},
				ChildModules: []*tfjson.StateModule{
					{
						Resources: []*tfjson.StateResource{
							{
								Address:      "module.m.aws_s3_bucket.b",
								Mode:         tfjson.ManagedResourceMode,
								ProviderName: "registry.terraform.io/hashicorp/aws",
							},
							{
								Address:      "module.m.data.aws_caller_identity.current",
								Mode:         tfjson.DataResourceMode,
								ProviderName: "registry.terraform.io/hashicorp/aws",
							},
						},
					},
				},
			},
		},
	}
	addrs := stateAddresses(s)
	if _, ok := addrs["google_project.p"]; !ok {
		t.Error("managed root resource must be included")
	}
	if _, ok := addrs["module.m.aws_s3_bucket.b"]; !ok {
		t.Error("managed child resource must be included")
	}
	if _, ok := addrs["data.google_secret_manager_secret_version.x"]; ok {
		t.Error("data source in root module must be excluded")
	}
	if _, ok := addrs["module.m.data.aws_caller_identity.current"]; ok {
		t.Error("data source in child module must be excluded")
	}
}
