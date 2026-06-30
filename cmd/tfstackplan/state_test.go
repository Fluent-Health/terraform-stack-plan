package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/statemove"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
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

// D3: state cleanup --applied removes all xmove manifests (the operator
// has verified the apply succeeded). Same-stack shims are left intact.
func TestStateCleanupApplied(t *testing.T) {
	root := t.TempDir()

	// Write a same-stack shim (must NOT be removed by --applied).
	shimDir := filepath.Join(root, "stacks/a")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	shimFile := filepath.Join(shimDir, statemove.ShimFileName("PR-5"))
	if err := os.WriteFile(shimFile, []byte(`# tfstackplan:key=PR-5`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write two xmove manifests (must be removed by --applied).
	for _, dst := range []string{"stacks/b", "stacks/c"} {
		d := filepath.Join(root, dst)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		xm := statemove.XMove{SourceStack: "stacks/a", Pairs: []statemove.Move{{From: "r.x", To: "r.x"}}}
		content := statemove.RenderXMove("PR-5", xm)
		if err := os.WriteFile(filepath.Join(d, statemove.XMoveFileName("PR-5")), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if code := runState([]string{"cleanup", "--dir", root, "--applied"}); code != 0 {
		t.Fatalf("cleanup --applied = %d, want 0", code)
	}
	// Shim untouched.
	if _, err := os.Stat(shimFile); err != nil {
		t.Error("cleanup --applied must not remove same-stack shims")
	}
	// Both xmove manifests gone.
	for _, dst := range []string{"stacks/b", "stacks/c"} {
		f := filepath.Join(root, dst, statemove.XMoveFileName("PR-5"))
		if _, err := os.Stat(f); err == nil {
			t.Errorf("cleanup --applied must remove xmove manifest in %s", dst)
		}
	}
}

// writeSrcPlanWithPriorState writes <root>/<stack>/tfplan.json with prior_state
// containing one resource at addr of the given type.
func writeSrcPlanWithPriorState(t *testing.T, root, stack, addr, resourceType string) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(stack))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	plan := `{"format_version":"1.2","prior_state":{"format_version":"0.1","values":{"root_module":{"resources":[{"address":"` +
		addr + `","type":"` + resourceType + `","provider_name":"registry.terraform.io/hashicorp/google"}]}}}}`
	if err := os.WriteFile(filepath.Join(dir, "tfplan.json"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStateMoveViaMv(t *testing.T) {
	root := t.TempDir()
	writeSrcPlanWithPriorState(t, root, "stacks/a", "module.main.module.perms.google_project_iam_member.x", "google_project_iam_member")
	// Dest plan: create under module.perms (intent to= prefix)
	dstDir := filepath.Join(root, "stacks/b")
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstDir, "tfplan.json"), []byte(`{"format_version":"1.2","resource_changes":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	code := runState([]string{"move", "--dir", root, "--pr", "5", "--via", "mv",
		"stacks/a:module.main.module.perms", "stacks/b:module.perms"})
	if code != 0 {
		t.Fatalf("state move --via mv = %d, want 0", code)
	}
	data, err := os.ReadFile(filepath.Join(root, "stacks/b", statemove.XMoveFileName("PR-5")))
	if err != nil {
		t.Fatalf("xmove manifest missing: %v", err)
	}
	// Manifest must store the intent pair exactly — NOT concrete per-resource pairs.
	if !strings.Contains(string(data), `"module.main.module.perms" = "module.perms"`) {
		t.Errorf("manifest must store intent pair, got:\n%s", data)
	}
	if strings.Contains(string(data), "google_project_iam_member") {
		t.Errorf("manifest must NOT contain concrete resource addresses, got:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(root, "stacks/b", statemove.ShimFileName("PR-5"))); err == nil {
		t.Error("--via mv should not write a native import/removed shim")
	}
}

func TestStateMoveViaMvFailsWhenNoPriorState(t *testing.T) {
	root := t.TempDir()
	// Source plan has NO prior_state (only ResourceChanges) — must fail.
	srcDir := filepath.Join(root, "stacks/a")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcPlan := `{"format_version":"1.2","resource_changes":[{"address":"module.perms.google_project_iam_member.x","change":{"actions":["delete"]}}]}`
	if err := os.WriteFile(filepath.Join(srcDir, "tfplan.json"), []byte(srcPlan), 0o644); err != nil {
		t.Fatal(err)
	}
	code := runState([]string{"move", "--dir", root, "--pr", "5", "--via", "mv",
		"stacks/a:module.perms", "stacks/b:module.dest"})
	if code == 0 {
		t.Fatal("expected non-zero exit when source has no prior_state")
	}
}

func TestStateMoveViaMvFailsWhenFromNotInPriorState(t *testing.T) {
	root := t.TempDir()
	// Source prior_state has "module.other.*" — "module.perms" is not present.
	writeSrcPlanWithPriorState(t, root, "stacks/a", "module.other.google_project_iam_member.x", "google_project_iam_member")
	code := runState([]string{"move", "--dir", root, "--pr", "5", "--via", "mv",
		"stacks/a:module.perms", "stacks/b:module.dest"})
	if code == 0 {
		t.Fatal("expected non-zero exit when from address not in prior_state")
	}
}

// fakeStateRunner is a minimal statemove.Runner for unit tests: Init is a no-op,
// StatePull returns a non-empty state JSON, and ShowStateFile returns a state
// with the preconfigured addresses.
type fakeStateRunner struct {
	addrs []string // resource addresses present in this runner's state
}

func (f *fakeStateRunner) Init(context.Context, ...tfexec.InitOption) error { return nil }
func (f *fakeStateRunner) StatePull(context.Context, ...tfexec.StatePullOption) (string, error) {
	if len(f.addrs) == 0 {
		return "", nil // empty state
	}
	return `{"format_version":"0.1","values":{"root_module":{}}}`, nil
}
func (f *fakeStateRunner) ShowStateFile(_ context.Context, _ string, _ ...tfexec.ShowOption) (*tfjson.State, error) {
	m := &tfjson.StateModule{}
	for _, a := range f.addrs {
		m.Resources = append(m.Resources, &tfjson.StateResource{Address: a})
	}
	return &tfjson.State{Values: &tfjson.StateValues{RootModule: m}}, nil
}
func (f *fakeStateRunner) StateMv(context.Context, string, string, ...tfexec.StateMvCmdOption) error {
	return nil
}
func (f *fakeStateRunner) StatePush(context.Context, string, ...tfexec.StatePushCmdOption) error {
	return nil
}

// TestApplyPendingMovesLogsPerStack asserts that each dry-run move line is
// delivered to the per-stack sink under the destination stack key. It overrides
// buildExecDeps with a fake runner so no real terraform binary or GCS backend is
// needed.
func TestApplyPendingMovesLogsPerStack(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "stacks/b")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	xm := statemove.XMove{SourceStack: "stacks/a", Pairs: []statemove.Move{{From: "x.y", To: "x.y"}}}
	if err := os.WriteFile(filepath.Join(dir, statemove.XMoveFileName("PR-1")), []byte(statemove.RenderXMove("PR-1", xm)), 0o644); err != nil {
		t.Fatal(err)
	}

	// Override buildExecDeps with a fake that supplies a source runner holding
	// "x.y" (so decide → DecisionMove) and an empty dest runner.
	orig := buildExecDeps
	buildExecDeps = func(_ string, _ statemove.Locker) (statemove.ExecDeps, error) {
		src := &fakeStateRunner{addrs: []string{"x.y"}}
		dst := &fakeStateRunner{}
		return statemove.ExecDeps{
			NewTF: func(wd string) (statemove.Runner, error) {
				if strings.HasSuffix(filepath.ToSlash(wd), "/stacks/a") {
					return src, nil
				}
				return dst, nil
			},
		}, nil
	}
	t.Cleanup(func() { buildExecDeps = orig })

	var got []string
	if err := applyPendingMoves(context.Background(), root, false, nil, io.Discard,
		func(stack, line string) { got = append(got, stack+"|"+line) }); err != nil {
		t.Fatalf("applyPendingMoves: %v", err)
	}

	// dry-run (execute=false): expect at least one "would move" line for stacks/b.
	found := false
	for _, g := range got {
		if strings.Contains(g, "stacks/b|") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no per-stack move log captured for stacks/b: %v", got)
	}
}

// D4/Phase 3: state check validates xmove manifests against local plan files
// without running terraform. It replaces the classify-time xmove validation
// as a standalone operator diagnostic command.
func TestStateCheck_NoManifests(t *testing.T) {
	dir := t.TempDir()
	code := runState([]string{"check", "--dir", dir})
	if code != 0 {
		t.Errorf("state check with no manifests = %d, want 0", code)
	}
}

func TestStateCheck_ValidManifest(t *testing.T) {
	root := t.TempDir()
	writeSrcPlanWithPriorState(t, root, "stacks/src", "module.x.google_project.p", "google_project")
	destDir := filepath.Join(root, "stacks/dest")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Provider block so DiscoverDestProviders finds "google" and xmove/provider-missing is not emitted.
	if err := os.WriteFile(filepath.Join(destDir, "providers.tf"), []byte(`provider "google" {}`), 0o644); err != nil {
		t.Fatal(err)
	}
	xm := statemove.XMove{SourceStack: "stacks/src", Pairs: []statemove.Move{{From: "module.x", To: "module.y"}}}
	if err := os.WriteFile(filepath.Join(destDir, statemove.XMoveFileName("test")), []byte(statemove.RenderXMove("test", xm)), 0o644); err != nil {
		t.Fatal(err)
	}
	code := runState([]string{"check", "--dir", root})
	if code != 0 {
		t.Errorf("state check with valid manifest = %d, want 0", code)
	}
}

func TestStateCheck_SpentManifest(t *testing.T) {
	root := t.TempDir()
	// Dest plan has prior_state with the To-address (move already applied to dest).
	destDir := filepath.Join(root, "stacks/dest")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	destPlan := `{"format_version":"1.2","prior_state":{"format_version":"0.1","values":{"root_module":{"resources":[` +
		`{"address":"module.y.google_project.p","type":"google_project","provider_name":"registry.terraform.io/hashicorp/google","values":{}}` +
		`]}}}}`
	if err := os.WriteFile(filepath.Join(destDir, "tfplan.json"), []byte(destPlan), 0o644); err != nil {
		t.Fatal(err)
	}
	xm := statemove.XMove{SourceStack: "stacks/src", Pairs: []statemove.Move{{From: "module.x", To: "module.y"}}}
	if err := os.WriteFile(filepath.Join(destDir, statemove.XMoveFileName("test")), []byte(statemove.RenderXMove("test", xm)), 0o644); err != nil {
		t.Fatal(err)
	}
	code := runState([]string{"check", "--dir", root})
	if code != 0 {
		t.Errorf("state check with spent manifest = %d, want 0 (spent is not an error)", code)
	}
}

func TestStateCheck_SourceNotPlanned(t *testing.T) {
	root := t.TempDir()
	// No source plan, no dest prior_state → source-not-planned error.
	destDir := filepath.Join(root, "stacks/dest")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	xm := statemove.XMove{SourceStack: "stacks/src", Pairs: []statemove.Move{{From: "module.x", To: "module.y"}}}
	if err := os.WriteFile(filepath.Join(destDir, statemove.XMoveFileName("test")), []byte(statemove.RenderXMove("test", xm)), 0o644); err != nil {
		t.Fatal(err)
	}
	code := runState([]string{"check", "--dir", root})
	if code == 0 {
		t.Error("state check must return non-zero when source stack is not planned and move is not spent")
	}
}

func TestStateApplyDiscoversManifests(t *testing.T) {
	if _, err := exec.LookPath("terraform"); err != nil {
		t.Skip("terraform not on PATH")
	}
	// terraform present but no real backends → Execute fails to pull; this only
	// asserts discovery + wiring (it finds the manifest and attempts it). A full
	// move is covered by the fake-runner tests in internal/statemove.
	root := t.TempDir()
	dir := filepath.Join(root, "stacks/b")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	xm := statemove.XMove{SourceStack: "stacks/a", Pairs: []statemove.Move{{From: "x.y", To: "x.y"}}}
	if err := os.WriteFile(filepath.Join(dir, statemove.XMoveFileName("PR-5")), []byte(statemove.RenderXMove("PR-5", xm)), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = runState([]string{"apply", "--dir", root}) // must not panic; exit code not asserted (no real backend)
}

func TestStateImportAndRemove(t *testing.T) {
	root := t.TempDir()
	err := os.MkdirAll(filepath.Join(root, "stacks/a"), 0o755)
	if err != nil {
		t.Fatal(err)
	}
	// 1. Run import command
	args := []string{"import", "--dir", root, "--stack", "stacks/a", "--pr", "10", "aws_s3_bucket.main", "my-bucket"}
	if code := runState(args); code != 0 {
		t.Fatalf("import failed with code %d", code)
	}
	// Verify import file written
	shimFile := filepath.Join(root, "stacks/a", statemove.ShimFileName("PR-10"))
	data, err := os.ReadFile(shimFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "import {\n  to = aws_s3_bucket.main\n  id = \"my-bucket\"") {
		t.Errorf("missing import block: %s", string(data))
	}

	// 2. Run remove command
	args = []string{"remove", "--dir", root, "--stack", "stacks/a", "--pr", "10", "aws_s3_bucket.main"}
	if code := runState(args); code != 0 {
		t.Fatalf("remove failed with code %d", code)
	}
	data, err = os.ReadFile(shimFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "removed {\n  from = aws_s3_bucket.main") {
		t.Errorf("missing removed block: %s", string(data))
	}
}

