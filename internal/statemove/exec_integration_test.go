//go:build integration

package statemove

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"
)

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

func addrSet(t *testing.T, tf *tfexec.Terraform) map[string]bool {
	t.Helper()
	st, err := tf.Show(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return stateAddresses(st)
}

func TestExecuteIntegration(t *testing.T) {
	tfPath, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("terraform not on PATH")
	}
	ctx := context.Background()
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")

	srcTF := tfStack(t, tfPath, srcDir, "resource \"terraform_data\" \"thing\" {}\n")
	// Dest is a brand-new stack: empty config, init-only (NOT applied). Its
	// StatePull returns 0 bytes — the realistic cross-state-move target. Execute
	// must treat that empty pull as an empty address set and let `state mv
	// -state-out` create the dest state file.
	dstTF := tfStack(t, tfPath, dstDir, "")
	if err := srcTF.Apply(ctx); err != nil { // create terraform_data.thing in source state
		t.Fatalf("apply src: %v", err)
	}

	if !addrSet(t, srcTF)["terraform_data.thing"] {
		t.Fatal("setup: source state missing the resource")
	}
	if addrSet(t, dstTF)["terraform_data.thing"] {
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

	if addrSet(t, srcTF)["terraform_data.thing"] {
		t.Error("resource still in SOURCE state after the move")
	}
	if !addrSet(t, dstTF)["terraform_data.thing"] {
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
