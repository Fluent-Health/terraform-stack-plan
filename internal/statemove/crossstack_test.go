package statemove

import (
	"encoding/json"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

// stateResource builds a *tfjson.StateResource for use in test PriorState values.
func stateResource(addr, typ, provider string) *tfjson.StateResource {
	return &tfjson.StateResource{
		Address:      addr,
		Mode:         tfjson.ManagedResourceMode,
		Type:         typ,
		ProviderName: provider,
	}
}

func delWithID(addr, typ, id string) *tfjson.ResourceChange {
	return &tfjson.ResourceChange{Address: addr, Type: typ, Change: &tfjson.Change{
		Actions: tfjson.Actions{tfjson.ActionDelete},
		Before:  map[string]any{"id": id},
	}}
}
func create(addr, typ string) *tfjson.ResourceChange {
	return &tfjson.ResourceChange{Address: addr, Type: typ, Change: &tfjson.Change{
		Actions: tfjson.Actions{tfjson.ActionCreate},
	}}
}

func TestClassifyCrossStack(t *testing.T) {
	src := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		delWithID("aws_s3_bucket.old", "aws_s3_bucket", "my-bucket"),
	}}
	dst := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		create("module.d.aws_s3_bucket.new", "aws_s3_bucket"),
	}}
	srcOps, dstOps, err := ClassifyCrossStack(src, dst, "aws_s3_bucket.old", "module.d.aws_s3_bucket.new")
	if err != nil {
		t.Fatal(err)
	}
	if len(srcOps) != 1 || srcOps[0] != (Op{Kind: "removed", From: "aws_s3_bucket.old"}) {
		t.Errorf("srcOps = %+v", srcOps)
	}
	if len(dstOps) != 1 || dstOps[0] != (Op{Kind: "import", To: "module.d.aws_s3_bucket.new", ID: "my-bucket"}) {
		t.Errorf("dstOps = %+v", dstOps)
	}
}

// TestCrossStackPairsFromState_priorStatePreventsModuleLevelRenameAddresses tests
// the core bug: a module-level moved{} block renames resources in ResourceChanges
// (module.console → module.console[0]) but does NOT set PreviousAddress per
// resource. CrossStackPairsFromState must use prior_state instead, producing
// manifests with the unindexed source addresses that apply-time validation expects.
func TestCrossStackPairsFromState_priorStatePreventsModuleLevelRenameAddresses(t *testing.T) {
	const p = "registry.terraform.io/hashicorp/google"
	src := &tfjson.Plan{
		// prior_state has the raw (unindexed) addresses from live state.
		PriorState: &tfjson.State{Values: &tfjson.StateValues{RootModule: &tfjson.StateModule{
			Resources: []*tfjson.StateResource{
				stateResource("module.main.module.console.google_service_account.runner", "google_service_account", p),
				stateResource("module.main.module.console.google_project_iam_member.runner_ai", "google_project_iam_member", p),
			},
		}}},
		// ResourceChanges has [0] addresses due to moved{} block — should NOT be used.
		ResourceChanges: []*tfjson.ResourceChange{
			delWithID("module.main.module.console[0].google_service_account.runner", "google_service_account", "sa-id"),
			delWithID("module.main.module.console[0].google_project_iam_member.runner_ai", "google_project_iam_member", "proj/roles/AI"),
		},
	}
	dst := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		create("module.console.google_service_account.runner", "google_service_account"),
		create("module.console.google_project_iam_member.runner_ai", "google_project_iam_member"),
	}}

	pairs, err := CrossStackPairsFromState(src, dst, "module.main.module.console", "module.console")
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 2 {
		t.Fatalf("want 2 pairs, got %d: %+v", len(pairs), pairs)
	}
	want := []Move{
		{From: "module.main.module.console.google_project_iam_member.runner_ai", To: "module.console.google_project_iam_member.runner_ai"},
		{From: "module.main.module.console.google_service_account.runner", To: "module.console.google_service_account.runner"},
	}
	for i, p := range pairs {
		if p != want[i] {
			t.Errorf("pair[%d] = %+v, want %+v", i, p, want[i])
		}
	}
}

func TestCrossStackPairsFromState_fallsBackWhenNoPriorState(t *testing.T) {
	// Without prior_state, behaves identically to CrossStackPairs.
	src := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		delWithID("aws_s3_bucket.old", "aws_s3_bucket", "my-bucket"),
	}}
	dst := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		create("module.d.aws_s3_bucket.new", "aws_s3_bucket"),
	}}
	pairs, err := CrossStackPairsFromState(src, dst, "aws_s3_bucket.old", "module.d.aws_s3_bucket.new")
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 1 || pairs[0] != (Move{From: "aws_s3_bucket.old", To: "module.d.aws_s3_bucket.new"}) {
		t.Errorf("pairs = %+v", pairs)
	}
}

func TestPriorStateAddrs_extractsAddresses(t *testing.T) {
	const p = "registry.terraform.io/hashicorp/google"
	state := &tfjson.State{Values: &tfjson.StateValues{RootModule: &tfjson.StateModule{
		Resources: []*tfjson.StateResource{
			stateResource("google_project_iam_member.x", "google_project_iam_member", p),
		},
		ChildModules: []*tfjson.StateModule{{
			Address: "module.m",
			Resources: []*tfjson.StateResource{
				stateResource("module.m.google_service_account.y", "google_service_account", p),
			},
		}},
	}}}
	plan := &tfjson.Plan{PriorState: state}
	b, _ := json.Marshal(plan)

	addrs := PriorStateAddrs(b)
	if addrs["google_project_iam_member.x"] != p {
		t.Errorf("missing root resource")
	}
	if addrs["module.m.google_service_account.y"] != p {
		t.Errorf("missing child-module resource")
	}
	if len(addrs) != 2 {
		t.Errorf("want 2 addresses, got %d: %v", len(addrs), addrs)
	}
}

func TestPriorStateAddrs_nilWhenNoPriorState(t *testing.T) {
	plan := &tfjson.Plan{}
	b, _ := json.Marshal(plan)
	if got := PriorStateAddrs(b); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestClassifyCrossStackFailsClosed(t *testing.T) {
	src := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{delWithID("a.x", "a", "i")}}
	dst := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{create("b.y", "b")}}
	if _, _, err := ClassifyCrossStack(src, dst, "a.x", "b.y"); err == nil {
		t.Error("expected type-mismatch error")
	}
	src2 := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		{Address: "a.x", Type: "a", Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.ActionDelete}, Before: map[string]any{}}},
	}}
	dst2 := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{create("a.y", "a")}}
	if _, _, err := ClassifyCrossStack(src2, dst2, "a.x", "a.y"); err == nil {
		t.Error("expected missing-id error")
	}
	src3 := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{delWithID("a.x", "a", "i")}}
	dst3 := &tfjson.Plan{ResourceChanges: nil}
	if _, _, err := ClassifyCrossStack(src3, dst3, "a.x", "a.y"); err == nil {
		t.Error("expected not-created-at-dest error")
	}
}
