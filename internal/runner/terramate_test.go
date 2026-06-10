package runner

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// fixtureRepo copies the vendored terramate fixture into a temp dir, initializes
// a git repo with one commit, and returns the dir. It skips when terramate
// cannot run.
func fixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("testdata/fixture")); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	probe := exec.Command("terramate", "version")
	probe.Dir = dir
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("terramate not runnable, skipping: %v: %s", err, out)
	}
	gitInit(t, dir)
	return dir
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"config", "commit.gpgsign", "false"},
		{"add", "-A"},
		{"commit", "-qm", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func TestTerramateList(t *testing.T) {
	dir := fixtureRepo(t)
	tm := &Terramate{Dir: dir}
	stacks, err := tm.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"stacks/a": true, "stacks/b": true, "stacks/c": true}
	if len(stacks) != 3 {
		t.Fatalf("stacks = %v, want 3", stacks)
	}
	for _, s := range stacks {
		if !want[s] {
			t.Errorf("unexpected stack %q", s)
		}
	}
}

func TestTerramateChangedStacks(t *testing.T) {
	dir := fixtureRepo(t)
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("checkout", "-qb", "feature")
	if err := os.WriteFile(filepath.Join(dir, "stacks", "b", "extra.tm.hcl"), []byte("globals {\n  x = 1\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "touch b")

	tm := &Terramate{Dir: dir}
	changed, err := tm.ChangedStacks(context.Background(), "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0] != "stacks/b" {
		t.Fatalf("changed = %v, want [stacks/b]", changed)
	}
}

func TestTerramateRunGraph(t *testing.T) {
	dir := fixtureRepo(t)
	tm := &Terramate{Dir: dir}
	edges, err := tm.RunGraph(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[events.Edge]bool{
		{From: "stacks/a", To: "stacks/b"}: false,
		{From: "stacks/a", To: "stacks/c"}: false,
	}
	for _, e := range edges {
		if _, ok := want[e]; !ok {
			t.Errorf("unexpected edge %+v", e)
		}
		want[e] = true
	}
	for e, seen := range want {
		if !seen {
			t.Errorf("missing edge %+v", e)
		}
	}
}

func TestTerramateScriptRun(t *testing.T) {
	dir := fixtureRepo(t)
	tm := &Terramate{Dir: dir}
	var buf bytes.Buffer
	err := tm.ScriptRun(context.Background(), &buf, ScriptRunOptions{Script: "noop"})
	if err != nil {
		t.Fatalf("script run: %v\n%s", err, buf.String())
	}
	if n := strings.Count(buf.String(), "ran"); n < 3 {
		t.Errorf("expected the noop script to run on all 3 stacks, output:\n%s", buf.String())
	}
}
