package statemove

import (
	"strings"
	"testing"
)

func TestRenderParseOps(t *testing.T) {
	ops := []Op{
		{Kind: "moved", From: "aws_s3_bucket.old", To: "aws_s3_bucket.new"},
		{Kind: "import", To: `module.d.aws_s3_bucket.x["k"]`, ID: "the-bucket"},
		{Kind: "removed", From: "module.s.aws_s3_bucket.x"},
	}
	content := RenderShim("PR-123", ops)
	for _, want := range []string{
		"do not edit", "tfstackplan:key=PR-123",
		"moved {", "from = aws_s3_bucket.old", "to   = aws_s3_bucket.new",
		"import {", `to = module.d.aws_s3_bucket.x["k"]`, `id = "the-bucket"`,
		"removed {", "from = module.s.aws_s3_bucket.x", "destroy = false",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("missing %q\n---\n%s", want, content)
		}
	}
	key, got, err := ParseShim(content)
	if err != nil {
		t.Fatal(err)
	}
	if key != "PR-123" || len(got) != 3 {
		t.Fatalf("key=%q ops=%+v", key, got)
	}
	for i := range ops {
		if got[i] != ops[i] {
			t.Errorf("op %d = %+v, want %+v", i, got[i], ops[i])
		}
	}
}

func TestParseShimRejectsForeign(t *testing.T) {
	if _, _, err := ParseShim("resource \"x\" \"y\" {}\n"); err == nil {
		t.Error("ParseShim should reject a non-tfstackplan file")
	}
}

func TestMergeOpsDedup(t *testing.T) {
	a := []Op{{Kind: "moved", From: "x.a", To: "x.b"}}
	b := []Op{{Kind: "moved", From: "x.a", To: "x.b"}, {Kind: "removed", From: "y.a"}}
	if got := MergeOps(a, b); len(got) != 2 {
		t.Errorf("MergeOps = %+v, want 2 unique", got)
	}
}
