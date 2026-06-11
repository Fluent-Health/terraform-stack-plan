package statemove

import (
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

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
