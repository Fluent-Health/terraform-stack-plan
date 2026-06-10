package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeMinimalPlan creates a plans-dir with one stack whose plan has no changes,
// enough for run() to succeed end-to-end.
func writeMinimalPlan(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	stack := filepath.Join(dir, "stacks", "demo")
	if err := os.MkdirAll(stack, 0o755); err != nil {
		t.Fatal(err)
	}
	plan := `{"format_version":"1.2","resource_changes":[]}`
	if err := os.WriteFile(filepath.Join(stack, "tfplan.json"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDispatchRenderSubcommand(t *testing.T) {
	plans := writeMinimalPlan(t)
	out := filepath.Join(t.TempDir(), "comment.md")
	if code := dispatch([]string{"render", "--plans-dir", plans, "--output", out}); code != 0 {
		t.Fatalf("render exit = %d, want 0", code)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("expected output file: %v", err)
	}
}

func TestDispatchBackCompatFlagsFirst(t *testing.T) {
	plans := writeMinimalPlan(t)
	out := filepath.Join(t.TempDir(), "comment.md")
	// No "render" token — flags-first invocation must still render.
	if code := dispatch([]string{"--plans-dir", plans, "--output", out}); code != 0 {
		t.Fatalf("back-compat exit = %d, want 0", code)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("expected output file: %v", err)
	}
}

func TestDispatchVersion(t *testing.T) {
	if code := dispatch([]string{"--version"}); code != 0 {
		t.Fatalf("--version exit = %d, want 0", code)
	}
}

func TestDispatchUnknownSubcommand(t *testing.T) {
	if code := dispatch([]string{"bogus"}); code != 2 {
		t.Fatalf("unknown subcommand exit = %d, want 2", code)
	}
}

func TestDispatchHelp(t *testing.T) {
	if code := dispatch([]string{"--help"}); code != 0 {
		t.Fatalf("--help exit = %d, want 0", code)
	}
}
