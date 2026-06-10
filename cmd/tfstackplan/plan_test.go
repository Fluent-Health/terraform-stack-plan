package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunPlanRequiresDir(t *testing.T) {
	if code := runPlan([]string{}); code == 0 {
		t.Error("run plan with no --dir should error")
	}
}

func TestDispatchRoutesRunPlan(t *testing.T) {
	if code := dispatch([]string{"run", "plan", "--dir", filepath.Join(t.TempDir(), "nope")}); code == 0 {
		t.Error("run plan on a missing dir should be non-zero")
	}
}

func TestGatherPlans(t *testing.T) {
	root := t.TempDir()
	for _, s := range []string{"stacks/a", "stacks/b"} {
		dir := filepath.Join(root, s)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "tfplan.json"), []byte(`{"format_version":"1.2"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	plansDir, err := gatherPlans(root, []string{"stacks/a", "stacks/b", "stacks/c"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(plansDir) })
	for _, s := range []string{"stacks/a", "stacks/b"} {
		if _, err := os.Stat(filepath.Join(plansDir, s, "tfplan.json")); err != nil {
			t.Errorf("missing gathered plan for %s: %v", s, err)
		}
	}
	if _, err := os.Stat(filepath.Join(plansDir, "stacks/c", "tfplan.json")); err == nil {
		t.Error("stacks/c had no plan; should not be gathered")
	}
}
