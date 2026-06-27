//go:build integration

package statemove

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"
)

// resolveTerraform returns the path to the terraform binary. If the PATH entry
// is an asdf shim (which fails outside a directory with a .tool-versions file),
// it queries "asdf which terraform" to get the concrete install path.
func resolveTerraform(t *testing.T) string {
	t.Helper()
	tfPath, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("terraform not on PATH")
	}
	// If this is an asdf shim, resolve to the concrete binary so that terraform
	// works from temp directories that have no .tool-versions.
	if strings.Contains(tfPath, "asdf") {
		var out bytes.Buffer
		cmd := exec.Command("asdf", "which", "terraform")
		cmd.Stdout = &out
		if err := cmd.Run(); err == nil {
			if resolved := strings.TrimSpace(out.String()); resolved != "" {
				return resolved
			}
		}
	}
	return tfPath
}

// tfStack writes a local-backend stack with the given resource config, inits it,
// and returns a tfexec handle. Uses the built-in terraform_data resource so init
// needs no provider download (offline-friendly).
func tfStack(t *testing.T, tfPath, dir, config string) *tfexec.Terraform {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	main := "terraform {\n  backend \"local\" {}\n}\n" + config
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	tf, err := tfexec.NewTerraform(dir, tfPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := tf.Init(context.Background()); err != nil {
		t.Fatalf("init %s: %v", dir, err)
	}
	return tf
}

func addrSet(t *testing.T, tf *tfexec.Terraform) AddressSet {
	t.Helper()
	st, err := tf.Show(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return stateAddresses(st)
}

// hasAddr reports whether addr is present in the address set.
func hasAddr(t *testing.T, tf *tfexec.Terraform, addr string) bool {
	t.Helper()
	_, ok := addrSet(t, tf)[addr]
	return ok
}

func TestExecuteIntegration(t *testing.T) {
	tfPath := resolveTerraform(t)
	ctx := context.Background()
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")

	srcTF := tfStack(t, tfPath, srcDir, "resource \"terraform_data\" \"thing\" {}\n")
	// Dest is a brand-new stack: init-only (NOT applied). Its StatePull returns
	// 0 bytes — the realistic cross-state-move target. Execute must treat that
	// empty pull as an empty address set and let `state mv -state-out` create
	// the dest state file. The terraform provider declaration is required so that
	// ValidateMovePlan accepts the incoming terraform_data resource.
	dstTF := tfStack(t, tfPath, dstDir, "provider \"terraform\" {}\n")
	if err := srcTF.Apply(ctx); err != nil { // create terraform_data.thing in source state
		t.Fatalf("apply src: %v", err)
	}

	if !hasAddr(t, srcTF, "terraform_data.thing") {
		t.Fatal("setup: source state missing the resource")
	}
	if hasAddr(t, dstTF, "terraform_data.thing") {
		t.Fatal("setup: dest state unexpectedly has the resource")
	}

	deps := ExecDeps{
		NewTF:     func(wd string) (Runner, error) { return NewTerraform(tfPath, wd) },
		BackupDir: t.TempDir(),
	}
	xm := XMove{SourceStack: "src", Pairs: []Move{{From: "terraform_data.thing", To: "terraform_data.thing"}}}

	acts, err := Execute(ctx, deps, root, "dst", xm, false)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(acts) != 1 || acts[0].Decision != DecisionMove {
		t.Fatalf("actions = %+v, want one DecisionMove", acts)
	}

	if hasAddr(t, srcTF, "terraform_data.thing") {
		t.Error("resource still in SOURCE state after the move")
	}
	if !hasAddr(t, dstTF, "terraform_data.thing") {
		t.Error("resource NOT in DEST state after the move")
	}

	// Idempotent re-run: nothing in source now → skip.
	acts2, err := Execute(ctx, deps, root, "dst", xm, false)
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if len(acts2) != 1 || acts2[0].Decision != DecisionSkip {
		t.Errorf("re-run actions = %+v, want one DecisionSkip", acts2)
	}
}

// TestExecuteIntegration_ModuleLevelIntent verifies that an intent-level manifest
// pair ("module.perms" → "module.dest") correctly fans out to concrete resources
// via expandPairs against live state. This is the canonical --via mv pattern:
// the manifest stores a module-level from/to, and the executor resolves concrete
// per-resource addresses at apply time.
func TestExecuteIntegration_ModuleLevelIntent(t *testing.T) {
	tfPath := resolveTerraform(t)
	ctx := context.Background()
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")

	// Build source stack: module "perms" containing terraform_data.x
	// The module lives at srcDir/perms_module/main.tf.
	if err := os.MkdirAll(filepath.Join(srcDir, "perms_module"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "main.tf"), []byte(`
terraform {
  backend "local" {}
}
module "perms" {
  source = "./perms_module"
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "perms_module", "main.tf"), []byte(`
resource "terraform_data" "x" { input = "hello" }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	srcTF, err := tfexec.NewTerraform(srcDir, tfPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := srcTF.Init(ctx); err != nil {
		t.Fatalf("src init: %v", err)
	}
	if err := srcTF.Apply(ctx); err != nil {
		t.Fatalf("src apply: %v", err)
	}

	// Verify source state has the module-prefixed address.
	if !hasAddr(t, srcTF, "module.perms.terraform_data.x") {
		t.Fatal("setup: expected module.perms.terraform_data.x in source state")
	}

	// Destination stack: empty (never applied), but declares the terraform provider
	// so that ValidateMovePlan accepts the incoming terraform_data resource.
	dstTF := tfStack(t, tfPath, dstDir, "provider \"terraform\" {}\n")

	deps := ExecDeps{
		NewTF:     func(wd string) (Runner, error) { return NewTerraform(tfPath, wd) },
		BackupDir: t.TempDir(),
	}

	// Intent manifest: module-level pair, not concrete per-resource.
	xm := XMove{
		SourceStack: "src",
		Pairs:       []Move{{From: "module.perms", To: "module.dest"}},
	}

	acts, err := Execute(ctx, deps, root, "dst", xm, false)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(acts) != 1 {
		t.Fatalf("expected 1 action, got %d: %+v", len(acts), acts)
	}
	if acts[0].Decision != DecisionMove {
		t.Fatalf("expected DecisionMove, got %v", acts[0].Decision)
	}
	// The executor must have resolved the intent to the concrete address.
	if acts[0].From != "module.perms.terraform_data.x" {
		t.Errorf("action From = %q, want module.perms.terraform_data.x", acts[0].From)
	}
	if acts[0].To != "module.dest.terraform_data.x" {
		t.Errorf("action To = %q, want module.dest.terraform_data.x", acts[0].To)
	}

	if hasAddr(t, srcTF, "module.perms.terraform_data.x") {
		t.Error("resource still in SOURCE state after move")
	}
	if !hasAddr(t, dstTF, "module.dest.terraform_data.x") {
		t.Error("resource NOT in DEST state after move")
	}

	// Idempotent re-run: source is empty, dest has it → skip.
	acts2, err := Execute(ctx, deps, root, "dst", xm, false)
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if len(acts2) != 1 || acts2[0].Decision != DecisionSkip {
		t.Errorf("re-run: expected 1 DecisionSkip, got %+v", acts2)
	}
}
