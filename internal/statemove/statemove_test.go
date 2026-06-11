package statemove

import (
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

// planFrom builds a *tfjson.Plan from (address, type, action) triples.
func planFrom(rows ...[3]string) *tfjson.Plan {
	p := &tfjson.Plan{}
	for _, r := range rows {
		p.ResourceChanges = append(p.ResourceChanges, &tfjson.ResourceChange{
			Address: r[0], Type: r[1],
			Change: &tfjson.Change{Actions: tfjson.Actions{tfjson.Action(r[2])}},
		})
	}
	return p
}

func TestValidateMoveResource(t *testing.T) {
	p := planFrom(
		[3]string{"aws_s3_bucket.old", "aws_s3_bucket", "delete"},
		[3]string{"aws_s3_bucket.new", "aws_s3_bucket", "create"},
	)
	if err := ValidateMove(p, "aws_s3_bucket.old", "aws_s3_bucket.new"); err != nil {
		t.Fatalf("valid rename rejected: %v", err)
	}
}

func TestValidateMoveModulePrefix(t *testing.T) {
	p := planFrom(
		[3]string{"module.a.aws_s3_bucket.x", "aws_s3_bucket", "delete"},
		[3]string{"module.a.aws_iam_role.y", "aws_iam_role", "delete"},
		[3]string{"module.b.aws_s3_bucket.x", "aws_s3_bucket", "create"},
		[3]string{"module.b.aws_iam_role.y", "aws_iam_role", "create"},
	)
	if err := ValidateMove(p, "module.a", "module.b"); err != nil {
		t.Fatalf("valid module move rejected: %v", err)
	}
}

func TestValidateMoveForEachAndCount(t *testing.T) {
	p := planFrom(
		[3]string{"aws_iam_member.m", "aws_iam_member", "delete"},
		[3]string{`aws_iam_member.m["admin"]`, "aws_iam_member", "create"},
	)
	if err := ValidateMove(p, "aws_iam_member.m", `aws_iam_member.m["admin"]`); err != nil {
		t.Fatalf("valid for_each move rejected: %v", err)
	}
}

func TestValidateMoveFailsClosed(t *testing.T) {
	p1 := planFrom([3]string{"aws_s3_bucket.old", "aws_s3_bucket", "delete"})
	if err := ValidateMove(p1, "aws_s3_bucket.old", "aws_s3_bucket.new"); err == nil {
		t.Error("expected error: destination not created")
	}
	p2 := planFrom(
		[3]string{"aws_s3_bucket.old", "aws_s3_bucket", "delete"},
		[3]string{"aws_iam_role.new", "aws_iam_role", "create"},
	)
	if err := ValidateMove(p2, "aws_s3_bucket.old", "aws_iam_role.new"); err == nil {
		t.Error("expected error: type mismatch")
	}
	p3 := planFrom(
		[3]string{"module.a.aws_s3_bucket.x", "aws_s3_bucket", "delete"},
		[3]string{"module.b.aws_s3_bucket.x", "aws_s3_bucket", "create"},
		[3]string{"module.b.aws_s3_bucket.z", "aws_s3_bucket", "create"},
	)
	if err := ValidateMove(p3, "module.a", "module.b"); err == nil {
		t.Error("expected error: unmatched create under destination")
	}
	p4 := planFrom([3]string{"aws_s3_bucket.new", "aws_s3_bucket", "create"})
	if err := ValidateMove(p4, "aws_s3_bucket.old", "aws_s3_bucket.new"); err == nil {
		t.Error("expected error: nothing destroyed under source")
	}
}
