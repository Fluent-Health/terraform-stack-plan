package statemove

import (
	"encoding/json"
	"strings"
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

func TestCheckXMoveSource_OkWhenFromPresentInPriorState(t *testing.T) {
	plan := &tfjson.Plan{
		PriorState: &tfjson.State{Values: &tfjson.StateValues{RootModule: &tfjson.StateModule{
			Resources: []*tfjson.StateResource{
				stateResource("module.perms.google_project_iam_member.x", "google_project_iam_member", "registry.terraform.io/hashicorp/google"),
			},
		}}},
	}
	if err := CheckXMoveSource(plan, "module.perms"); err != nil {
		t.Errorf("expected nil, got: %v", err)
	}
}

func TestCheckXMoveSource_ErrorWhenNoPriorState(t *testing.T) {
	plan := &tfjson.Plan{} // no prior_state
	err := CheckXMoveSource(plan, "module.perms")
	if err == nil {
		t.Fatal("expected error for missing prior_state, got nil")
	}
	if !strings.Contains(err.Error(), "no prior state") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestCheckXMoveSource_ErrorWhenFromNotInPriorState(t *testing.T) {
	plan := &tfjson.Plan{
		PriorState: &tfjson.State{Values: &tfjson.StateValues{RootModule: &tfjson.StateModule{
			Resources: []*tfjson.StateResource{
				stateResource("module.other.google_project_iam_member.x", "google_project_iam_member", "registry.terraform.io/hashicorp/google"),
			},
		}}},
	}
	err := CheckXMoveSource(plan, "module.perms")
	if err == nil {
		t.Fatal("expected error when from address not in prior_state, got nil")
	}
}
