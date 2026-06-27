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

func TestDiscoverXMovesErrorsOnCorruptManifest(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "stacks/dst")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, XMoveFileName("PR-1")), []byte("# tfstackplan:key=PR-1\nxmove { not valid hcl\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverXMoves(root); err == nil {
		t.Fatal("DiscoverXMoves must fail-closed on a corrupt _tfsp_xmove file, got nil")
	}
}

func TestDiscoverXMovesErrorsOnKeyMismatch(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "stacks/dst")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	xm := XMove{SourceStack: "src", Pairs: []Move{{From: "a.b", To: "a.b"}}}
	if err := os.WriteFile(filepath.Join(dir, XMoveFileName("PR-1")), []byte(RenderXMove("PR-2", xm)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverXMoves(root); err == nil {
		t.Fatal("DiscoverXMoves must error on filename/header key mismatch, got nil")
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

// D4: a hand-written manifest without the "# tfstackplan:key=" comment must
// parse successfully — ParseXMove returns key="" and DiscoverXMoves fills it
// from the filename, so the operator doesn't have to craft the header by hand.
func TestParseXMoveMissingKeyHeaderReturnsEmpty(t *testing.T) {
	content := "xmove {\n  source_stack = \"stacks/a\"\n  moves = {\n    \"aws_s3_bucket.x\" = \"aws_s3_bucket.x\"\n  }\n}\n"
	key, xm, err := ParseXMove(content)
	if err != nil {
		t.Fatalf("ParseXMove without key header must not error: %v", err)
	}
	if key != "" {
		t.Errorf("key must be empty when header is absent, got %q", key)
	}
	if xm.SourceStack != "stacks/a" {
		t.Errorf("source_stack = %q, want stacks/a", xm.SourceStack)
	}
}

func TestDiscoverXMovesAcceptsManifestWithoutKeyHeader(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "stacks/dst")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "xmove {\n  source_stack = \"stacks/a\"\n  moves = {\n    \"aws_s3_bucket.x\" = \"aws_s3_bucket.x\"\n  }\n}\n"
	if err := os.WriteFile(filepath.Join(dir, XMoveFileName("PR-7")), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	found, err := DiscoverXMoves(root)
	if err != nil {
		t.Fatalf("DiscoverXMoves must accept manifests without key header: %v", err)
	}
	if len(found) != 1 || found[0].Key != "PR-7" || found[0].XMove.SourceStack != "stacks/a" {
		t.Fatalf("found = %+v, want key=PR-7 source=stacks/a", found)
	}
}
