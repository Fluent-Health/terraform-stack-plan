package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestClassifyForGateReturnsGates runs the shared classify pass over the plan
// fixture (whose stacks carry an IAM create on project "proj-a") and asserts it
// returns the IAM gate target, the rendered report, and per-stack categories.
// This is the same machinery run apply submits as Finalize{Gates} at apply time.
func TestClassifyForGateReturnsGates(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("testdata/planfixture")); err != nil {
		t.Fatal(err)
	}
	probe := exec.Command("terramate", "version")
	probe.Dir = dir
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("terramate not runnable: %v: %s", err, out)
	}
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"config", "commit.gpgsign", "false"}, {"add", "-A"}, {"commit", "-qm", "init"}} {
		c := exec.Command("git", a...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", a, err, out)
		}
	}
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	planJSON := `{"format_version":"1.2","resource_changes":[{"address":"google_project_iam_member.x","type":"google_project_iam_member","name":"x","change":{"actions":["create"],"before":null,"after":{"project":"proj-a"}}}]}`
	stub := "#!/bin/sh\ncase \"$1 $2\" in\n  \"show -json\") cat <<'J'\n" + planJSON + "\nJ\n  ;;\n  *) : ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "terraform"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	res, err := classifyForGate(context.Background(), dir, []string{"stacks/a", "stacks/b"}, "", false, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, g := range res.Gates {
		if g.Class == "iam" && g.Target == "proj-a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("classifyForGate gates = %+v, want iam/proj-a", res.Gates)
	}
	if res.Report == "" {
		t.Error("classifyForGate returned an empty report")
	}
	if len(res.Categories) == 0 {
		t.Error("classifyForGate returned no per-stack categories")
	}
}
