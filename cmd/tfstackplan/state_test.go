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

func TestStateMoveRejectsCrossStack(t *testing.T) {
	root := writeStackPlan(t, "stacks/a", [3]string{"x.a", "x", "delete"})
	code := runState([]string{"move", "--dir", root, "--stack", "stacks/a", "--pr", "1",
		"stacks/a:x.a", "stacks/b:x.b"})
	if code == 0 {
		t.Error("SP1 should reject a cross-stack move")
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
