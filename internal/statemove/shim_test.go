package statemove

import (
	"strings"
	"testing"
)

func TestRenderParseRoundTrip(t *testing.T) {
	moves := []Move{
		{From: "aws_s3_bucket.old", To: "aws_s3_bucket.new"},
		{From: `aws_iam_member.m`, To: `aws_iam_member.m["admin"]`},
	}
	content := RenderShim("PR-123", moves)

	for _, want := range []string{
		"do not edit", "tfstackplan:key=PR-123",
		"moved {", "from = aws_s3_bucket.old", "to   = aws_s3_bucket.new",
		`to   = aws_iam_member.m["admin"]`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("rendered shim missing %q\n---\n%s", want, content)
		}
	}

	key, got, err := ParseShim(content)
	if err != nil {
		t.Fatal(err)
	}
	if key != "PR-123" {
		t.Errorf("key = %q, want PR-123", key)
	}
	if len(got) != 2 || got[0] != moves[0] || got[1] != moves[1] {
		t.Errorf("parsed moves = %+v, want %+v", got, moves)
	}
}

func TestParseShimRejectsForeign(t *testing.T) {
	if _, _, err := ParseShim("# just some terraform\nresource \"x\" \"y\" {}\n"); err == nil {
		t.Error("ParseShim should reject a non-tfstackplan file")
	}
}

func TestMergeMovesDedup(t *testing.T) {
	a := []Move{{From: "x.a", To: "x.b"}}
	b := []Move{{From: "x.a", To: "x.b"}, {From: "y.a", To: "y.b"}}
	got := MergeMoves(a, b)
	if len(got) != 2 {
		t.Errorf("MergeMoves = %+v, want 2 unique", got)
	}
}
