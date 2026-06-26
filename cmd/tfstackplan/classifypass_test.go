package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// TestRenderClassificationReconcilesXMove proves the gate auto-reconciles a
// pending cross-state move: with an xmove manifest colocated in the destination
// stack, renderClassification must classify BOTH the source move-out (a planned
// IAM delete) and the destination move-in (a planned IAM create) as a relocation
// — so neither trips the IAM gate — without the caller passing --state-moves.
//
// This is the regression the run-consolidation introduced: renderClassification
// built opts{} without stateMoves, so a cross-state move showed up as
// "iam + destructive" instead of a 🚚 move. The whole-module xmove pair covers
// both addresses by prefix.
func TestRenderClassificationReconcilesXMove(t *testing.T) {
	dir := t.TempDir()
	const (
		srcStack = "stacks/nonprod/service-projects/fh-dev-svc"
		dstStack = "stacks/nonprod/workloads/cms/fh-dev-svc"
	)

	write := func(rel, body string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Source: the cms module leaving — an IAM member DELETE (project p1).
	write(srcStack+"/tfplan.json", `{"format_version":"1.2","resource_changes":[
	  {"address":"module.main.module.cms[0].google_project_iam_member.a","type":"google_project_iam_member","name":"a",
	   "change":{"actions":["delete"],"before":{"project":"p1","role":"roles/viewer"},"after":null}}]}`)
	// Destination: the same member arriving — an IAM member CREATE (project p1).
	write(dstStack+"/tfplan.json", `{"format_version":"1.2","resource_changes":[
	  {"address":"module.cms.google_project_iam_member.a","type":"google_project_iam_member","name":"a",
	   "change":{"actions":["create"],"before":null,"after":{"project":"p1","role":"roles/viewer"}}}]}`)
	// Whole-module xmove manifest in the destination stack.
	write(dstStack+"/_tfsp_xmove.PR-1.hcl", `# tfstackplan:key=PR-1
xmove {
  source_stack = "`+srcStack+`"
  moves = {
    "module.main.module.cms[0]" = "module.cms"
  }
}
`)
	cfgPath := filepath.Join(dir, ".tfstackplan.hcl")
	write(".tfstackplan.hcl", `
classification {
  default {
    name = "safe"
    icon = "✅"
  }
  preset "iam" {
    icon            = "🔐"
    emit_attributes = ["project"]
  }
}
`)

	res, err := renderClassification(dir, []string{srcStack, dstStack}, cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	// The move is reconciled on BOTH sides: neither the source move-out nor the
	// destination move-in is classified as iam (so the iam gate never fires).
	for stack, cats := range res.Categories {
		for _, c := range cats {
			if c.Name == "iam" {
				t.Fatalf("a cross-state move must not classify as iam; stack %s got %+v", stack, cats)
			}
		}
	}
	// Both stacks adopted/released via the move → marked moving.
	movingSet := map[string]bool{}
	for _, s := range res.Moving {
		movingSet[s] = true
	}
	if !movingSet[dstStack] {
		t.Fatalf("destination stack must be marked moving; moving=%v", res.Moving)
	}
}

// TestRenderClassificationFailsClosedOnCorruptXMove proves the classify pass is
// genuinely fail-closed: a malformed xmove manifest in our reserved namespace
// makes renderClassification return an error instead of silently dropping the
// move (which would let a relocation classify as a real destroy+create). This is
// the end-to-end guard for the discovery-layer fail-closed fix.
func TestRenderClassificationFailsClosedOnCorruptXMove(t *testing.T) {
	dir := t.TempDir()
	const stack = "stacks/a"
	write := func(rel, body string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A valid (empty) plan for the stack so gatherPlans succeeds and we reach the
	// state-moves reconciliation step.
	write(stack+"/tfplan.json", `{"format_version":"1.2","resource_changes":[]}`)
	// A corrupt xmove manifest in the reserved namespace (valid key header, bad HCL).
	write(stack+"/_tfsp_xmove.PR-1.hcl", "# tfstackplan:key=PR-1\nxmove { not valid hcl\n")
	write(".tfstackplan.hcl", `
classification {
  default {
    name = "safe"
    icon = "✅"
  }
}
`)
	cfgPath := filepath.Join(dir, ".tfstackplan.hcl")

	if _, err := renderClassification(dir, []string{stack}, cfgPath); err == nil {
		t.Fatal("renderClassification must fail closed on a corrupt xmove manifest, got nil error")
	}
}

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

	res, err := classifyForGate(context.Background(), dir, []string{"stacks/a", "stacks/b"}, "", false, "", 0)
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

// TestRenderClassificationCarriesCounts proves renderClassification populates
// Counts from the classification sidecar. A stack with a single "create" resource
// change must produce Counts.Add == 1 in res.Counts[stack], and the map itself
// must be non-nil even before any changes are found.
func TestRenderClassificationCarriesCounts(t *testing.T) {
	dir := t.TempDir()
	const stack = "stacks/a"

	write := func(rel, body string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A single "create" resource change so the sidecar will record Add=1.
	write(stack+"/tfplan.json", `{"format_version":"1.2","resource_changes":[
	  {"address":"null_resource.x","type":"null_resource","name":"x",
	   "change":{"actions":["create"],"before":null,"after":{}}}]}`)

	cfgPath := filepath.Join(dir, ".tfstackplan.hcl")
	write(".tfstackplan.hcl", `
classification {
  default {
    name = "safe"
    icon = "✅"
  }
}
`)

	res, err := renderClassification(dir, []string{stack}, cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	// Counts must be non-nil regardless.
	if res.Counts == nil {
		t.Fatal("renderClassification returned nil Counts map")
	}

	// The single create must be recorded as Add=1 for this stack.
	got, ok := res.Counts[stack]
	if !ok {
		t.Fatalf("res.Counts[%q] missing; got keys: %v", stack, func() []string {
			var ks []string
			for k := range res.Counts {
				ks = append(ks, k)
			}
			return ks
		}())
	}
	want := events.Counts{Add: 1}
	if got != want {
		t.Errorf("res.Counts[%q] = %+v, want %+v", stack, got, want)
	}
}

func TestValidateXMoveManifest_warnsForMissingAddresses(t *testing.T) {
	dir := t.TempDir()
	const (
		srcStack = "stacks/nonprod/service-projects/fh-dev-svc"
		dstStack = "stacks/nonprod/workloads/cms/fh-dev-svc"
	)

	write := func(rel, body string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Source plan does NOT contain the address we move.
	write(srcStack+"/tfplan.json", `{"format_version":"1.2","resource_changes":[
	  {"address":"module.other.r","type":"google_project_iam_member","name":"a",
	   "change":{"actions":["delete"],"before":{"project":"p1","role":"roles/viewer"},"after":null}}]}`)

	// XMove manifest references a From address not found in the plan.
	write(dstStack+"/_tfsp_xmove.PR-1.hcl", `# tfstackplan:key=PR-1
xmove {
  source_stack = "`+srcStack+`"
  moves = {
    "module.main.module.cms[0]" = "module.cms"
  }
}
`)

	// Capture Stderr
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	err := validateXMoveManifest(dir, dir)

	w.Close()
	os.Stderr = oldStderr

	if err == nil {
	        t.Fatal("expected error from validateXMoveManifest, got nil")
	}

	var buf [1024]byte
	n, _ := r.Read(buf[:])
	out := string(buf[:n])

	expected := "❌ xmove PR-1: address \"module.main.module.cms[0]\" not found in source plan (manifest stale or address renamed)"
	if !strings.Contains(out, "❌") || !strings.Contains(out, "not found in source plan") {
	        t.Fatalf("expected warning message to contain %q, got: %q", expected, out)
	}
}

func TestValidateXMoveManifest_warnsForMissingProviders(t *testing.T) {
	dir := t.TempDir()
	const (
		srcStack = "stacks/nonprod/service-projects/fh-dev-svc"
		dstStack = "stacks/nonprod/workloads/cms/fh-dev-svc"
	)

	write := func(rel, body string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Source plan has valid resource delete.
	write(srcStack+"/tfplan.json", `{"format_version":"1.2","resource_changes":[
	  {"address":"module.main.module.cms[0].postgresql_grant.a","type":"postgresql_grant","name":"a","provider_name":"registry.terraform.io/cyrilgdn/postgresql",
	   "change":{"actions":["delete"],"before":{"id":"abc"},"after":null}}]}`)

	// XMove manifest moves "postgresql_grant" to dstStack.
	write(dstStack+"/_tfsp_xmove.PR-1.hcl", `# tfstackplan:key=PR-1
xmove {
  source_stack = "`+srcStack+`"
  moves = {
    "module.main.module.cms[0].postgresql_grant.a" = "postgresql_grant.a"
  }
}
`)

	// Destination stack has NO provider "postgresql" block!
	write(dstStack+"/main.tf", `resource "postgresql_grant" "a" {}`)

	// Capture Stderr
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	err := validateXMoveManifest(dir, dir)

	w.Close()
	os.Stderr = oldStderr

	if err == nil {
	        t.Fatal("expected error from validateXMoveManifest, got nil")
	}

	var buf [1024]byte
	n, _ := r.Read(buf[:])
	out := string(buf[:n])

	if !strings.Contains(out, "❌") || !strings.Contains(out, "provider") {
	        t.Fatalf("expected error message to contain '❌' and 'provider', got: %q", out)
	}

	// Now write provider block and verify NO warning!
	write(dstStack+"/providers.tf", `
	provider "postgresql" {
	host = "localhost"
	}
	`)

	// Capture Stderr again
	r2, w2, _ := os.Pipe()
	os.Stderr = w2

	err2 := validateXMoveManifest(dir, dir)

	w2.Close()
	os.Stderr = oldStderr

	if err2 != nil {
	        t.Fatalf("expected no validation error after adding provider block, got: %v", err2)
	}

	var buf2 [1024]byte
	n2, _ := r2.Read(buf2[:])
	out2 := string(buf2[:n2])

	if strings.Contains(out2, "provider") {
	        t.Fatalf("expected NO provider config warning when postgresql provider block is configured, got: %q", out2)
	}
}
