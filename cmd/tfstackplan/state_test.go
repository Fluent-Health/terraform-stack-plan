package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/statemove"
)

// writeStackPlan writes a tfplan.json with (address,type,action) rows into
// <root>/<stack>/tfplan.json and returns the root.
func writeStackPlan(t *testing.T, stack string, rows ...[3]string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, filepath.FromSlash(stack))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var rc []string
	for _, r := range rows {
		rc = append(rc, `{"address":"`+r[0]+`","type":"`+r[1]+`","change":{"actions":["`+r[2]+`"]}}`)
	}
	js := `{"format_version":"1.2","resource_changes":[` + strings.Join(rc, ",") + `]}`
	if err := os.WriteFile(filepath.Join(dir, "tfplan.json"), []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestStateMoveGeneratesShim(t *testing.T) {
	root := writeStackPlan(t, "stacks/a",
		[3]string{"aws_s3_bucket.old", "aws_s3_bucket", "delete"},
		[3]string{"aws_s3_bucket.new", "aws_s3_bucket", "create"},
	)
	code := runState([]string{"move", "--dir", root, "--stack", "stacks/a", "--pr", "123",
		"aws_s3_bucket.old", "aws_s3_bucket.new"})
	if code != 0 {
		t.Fatalf("state move = %d, want 0", code)
	}
	shim := filepath.Join(root, "stacks/a", statemove.ShimFileName("PR-123"))
	data, err := os.ReadFile(shim)
	if err != nil {
		t.Fatalf("shim not written: %v", err)
	}
	if !strings.Contains(string(data), "from = aws_s3_bucket.old") {
		t.Errorf("shim missing moved block:\n%s", data)
	}
}

func TestStateMoveFailsClosed(t *testing.T) {
	root := writeStackPlan(t, "stacks/a",
		[3]string{"aws_s3_bucket.old", "aws_s3_bucket", "delete"},
	)
	code := runState([]string{"move", "--dir", root, "--stack", "stacks/a", "--pr", "123",
		"aws_s3_bucket.old", "aws_s3_bucket.new"})
	if code == 0 {
		t.Error("state move should fail closed when the plan isn't in order")
	}
	if _, err := os.Stat(filepath.Join(root, "stacks/a", statemove.ShimFileName("PR-123"))); err == nil {
		t.Error("no shim should be written on a failed-closed move")
	}
}

// writeTwoStackPlans seeds <root>/<src>/tfplan.json (a delete w/ before.id) and
// <root>/<dst>/tfplan.json (a create), returning root.
func writeTwoStackPlans(t *testing.T, srcStack, srcAddr, srcType, id, dstStack, dstAddr, dstType string) string {
	t.Helper()
	root := t.TempDir()
	mk := func(stack, body string) {
		dir := filepath.Join(root, filepath.FromSlash(stack))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "tfplan.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk(srcStack, `{"format_version":"1.2","resource_changes":[{"address":"`+srcAddr+`","type":"`+srcType+`","change":{"actions":["delete"],"before":{"id":"`+id+`"}}}]}`)
	mk(dstStack, `{"format_version":"1.2","resource_changes":[{"address":"`+dstAddr+`","type":"`+dstType+`","change":{"actions":["create"]}}]}`)
	return root
}

func TestStateMoveCrossStack(t *testing.T) {
	root := writeTwoStackPlans(t, "stacks/a", "aws_s3_bucket.x", "aws_s3_bucket", "my-bucket",
		"stacks/b", "aws_s3_bucket.x", "aws_s3_bucket")
	code := runState([]string{"move", "--dir", root, "--pr", "5",
		"stacks/a:aws_s3_bucket.x", "stacks/b:aws_s3_bucket.x"})
	if code != 0 {
		t.Fatalf("cross-stack move = %d, want 0", code)
	}
	srcData, err := os.ReadFile(filepath.Join(root, "stacks/a", statemove.ShimFileName("PR-5")))
	if err != nil {
		t.Fatalf("source shim missing: %v", err)
	}
	if !strings.Contains(string(srcData), "removed {") || !strings.Contains(string(srcData), "destroy = false") {
		t.Errorf("source shim missing removed block:\n%s", srcData)
	}
	dstData, err := os.ReadFile(filepath.Join(root, "stacks/b", statemove.ShimFileName("PR-5")))
	if err != nil {
		t.Fatalf("dest shim missing: %v", err)
	}
	if !strings.Contains(string(dstData), "import {") || !strings.Contains(string(dstData), `id = "my-bucket"`) {
		t.Errorf("dest shim missing import block:\n%s", dstData)
	}
}

func TestStateMoveCrossStackFailsClosed(t *testing.T) {
	root := writeTwoStackPlans(t, "stacks/a", "aws_s3_bucket.x", "aws_s3_bucket", "id",
		"stacks/b", "aws_s3_bucket.x", "aws_s3_bucket")
	_ = os.WriteFile(filepath.Join(root, "stacks/b", "tfplan.json"), []byte(`{"format_version":"1.2","resource_changes":[]}`), 0o644)
	code := runState([]string{"move", "--dir", root, "--pr", "5",
		"stacks/a:aws_s3_bucket.x", "stacks/b:aws_s3_bucket.x"})
	if code == 0 {
		t.Error("expected fail-closed on a cross-stack move with no dest create")
	}
	if _, err := os.Stat(filepath.Join(root, "stacks/a", statemove.ShimFileName("PR-5"))); err == nil {
		t.Error("no source shim should be written on failure")
	}
}

func TestStateListAndCleanup(t *testing.T) {
	root := writeStackPlan(t, "stacks/a",
		[3]string{"aws_s3_bucket.old", "aws_s3_bucket", "delete"},
		[3]string{"aws_s3_bucket.new", "aws_s3_bucket", "create"},
	)
	if code := runState([]string{"move", "--dir", root, "--stack", "stacks/a", "--pr", "7",
		"aws_s3_bucket.old", "aws_s3_bucket.new"}); code != 0 {
		t.Fatal("move failed")
	}
	if code := runState([]string{"list", "--dir", root}); code != 0 {
		t.Error("list failed")
	}
	if code := runState([]string{"cleanup", "--dir", root, "--all"}); code != 0 {
		t.Error("cleanup failed")
	}
	if _, err := os.Stat(filepath.Join(root, "stacks/a", statemove.ShimFileName("PR-7"))); err == nil {
		t.Error("cleanup --all should have removed the shim")
	}
}

func TestStateMoveViaMv(t *testing.T) {
	root := writeTwoStackPlans(t, "stacks/a", "aws_s3_bucket.x", "aws_s3_bucket", "id",
		"stacks/b", "aws_s3_bucket.x", "aws_s3_bucket")
	code := runState([]string{"move", "--dir", root, "--pr", "5", "--via", "mv",
		"stacks/a:aws_s3_bucket.x", "stacks/b:aws_s3_bucket.x"})
	if code != 0 {
		t.Fatalf("state move --via mv = %d, want 0", code)
	}
	data, err := os.ReadFile(filepath.Join(root, "stacks/b", statemove.XMoveFileName("PR-5")))
	if err != nil {
		t.Fatalf("xmove manifest missing: %v", err)
	}
	if !strings.Contains(string(data), `source_stack = "stacks/a"`) || !strings.Contains(string(data), "xmove {") {
		t.Errorf("manifest content:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(root, "stacks/b", statemove.ShimFileName("PR-5"))); err == nil {
		t.Error("--via mv should not write a native import/removed shim")
	}
}
