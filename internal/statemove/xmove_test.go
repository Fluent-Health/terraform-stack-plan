package statemove

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestXMoveRenderParse(t *testing.T) {
	xm := XMove{SourceStack: "stacks/a", Pairs: []Move{
		{From: "aws_s3_bucket.x", To: "aws_s3_bucket.x"},
		{From: `aws_iam_member.m["a"]`, To: `aws_iam_member.m["a"]`},
	}}
	content := RenderXMove("PR-5", xm)
	for _, want := range []string{"do not edit", "tfstackplan:key=PR-5", `source_stack = "stacks/a"`, "xmove {", `"aws_s3_bucket.x" = "aws_s3_bucket.x"`} {
		if !strings.Contains(content, want) {
			t.Errorf("missing %q\n---\n%s", want, content)
		}
	}
	key, got, err := ParseXMove(content)
	if err != nil {
		t.Fatal(err)
	}
	if key != "PR-5" || got.SourceStack != "stacks/a" || len(got.Pairs) != 2 {
		t.Fatalf("key=%q xm=%+v", key, got)
	}
}

func TestDiscoverXMoves(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "stacks/b")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	xm := XMove{SourceStack: "stacks/a", Pairs: []Move{{From: "x.y", To: "x.y"}}}
	if err := os.WriteFile(filepath.Join(dir, XMoveFileName("PR-5")), []byte(RenderXMove("PR-5", xm)), 0o644); err != nil {
		t.Fatal(err)
	}
	found, err := DiscoverXMoves(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Key != "PR-5" || found[0].DestStack != "stacks/b" || found[0].XMove.SourceStack != "stacks/a" {
		t.Fatalf("found = %+v", found)
	}
}
