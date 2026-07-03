package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/statemove"
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
	// prior_state carries the unprocessed live-state addresses; resource_changes
	// would have post-moved{} addresses and must NOT be used as the source set.
	write(srcStack+"/tfplan.json", `{"format_version":"1.2",`+
		`"prior_state":{"format_version":"0.1","values":{"root_module":{"child_modules":[`+
		`{"address":"module.main","child_modules":[`+
		`{"address":"module.main.module.cms[0]","resources":[`+
		`{"address":"module.main.module.cms[0].google_project_iam_member.a",`+
		`"type":"google_project_iam_member","provider_name":"registry.terraform.io/hashicorp/google"}]}]}]}}},`+
		`"resource_changes":[`+
		`{"address":"module.main.module.cms[0].google_project_iam_member.a","type":"google_project_iam_member","name":"a",`+
		`"change":{"actions":["delete"],"before":{"project":"p1","role":"roles/viewer"},"after":null}}]}`)
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
	// Provider block so DiscoverDestProviders finds "google" in the dest stack dir.
	write(dstStack+"/providers.tf", `provider "google" {}`)
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

	// Source plan does NOT contain the xmove From address in prior_state.
	// prior_state has module.other.r (not module.main.module.cms[0]), so the
	// validator must report "not found in source plan" rather than falling through
	// to ResourceChanges.
	write(srcStack+"/tfplan.json", `{"format_version":"1.2",`+
		`"prior_state":{"values":{"root_module":{"child_modules":[`+
		`{"address":"module.other","resources":[`+
		`{"address":"module.other.r","type":"google_project_iam_member",`+
		`"provider_name":"registry.terraform.io/hashicorp/google"}]}]}}},`+
		`"resource_changes":[`+
		`{"address":"module.other.r","type":"google_project_iam_member","name":"a",`+
		`"change":{"actions":["delete"],"before":{"project":"p1","role":"roles/viewer"},"after":null}}]}`)

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

// A spent manifest — its move already applied by an earlier PR, awaiting its GC
// PR — must not fail run plan on unrelated PRs (issue #180). The source stack
// is planned (so the source-absent whole-manifest spent path does not apply)
// but no longer holds the From address; the destination's prior_state holds the
// moved resource. Expect a per-entry xmove/spent info line and no error.
func TestValidateXMoveManifest_spentManifestIsNoOp(t *testing.T) {
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

	// Source plan exists but its prior_state no longer holds the From address —
	// the resource moved away in a previous apply.
	write(srcStack+"/tfplan.json", `{"format_version":"1.2",`+
		`"prior_state":{"values":{"root_module":{"child_modules":[`+
		`{"address":"module.other","resources":[`+
		`{"address":"module.other.r","type":"google_project_iam_member",`+
		`"provider_name":"registry.terraform.io/hashicorp/google"}]}]}}},`+
		`"resource_changes":[]}`)

	// Destination plan's prior_state holds the moved resource; no changes.
	write(dstStack+"/tfplan.json", `{"format_version":"1.2",`+
		`"prior_state":{"values":{"root_module":{"child_modules":[`+
		`{"address":"module.cms","resources":[`+
		`{"address":"module.cms.google_sql_database.a","type":"google_sql_database",`+
		`"provider_name":"registry.terraform.io/hashicorp/google"}]}]}}},`+
		`"resource_changes":[]}`)

	write(dstStack+"/_tfsp_xmove.PR-1.hcl", `# tfstackplan:key=PR-1
xmove {
  source_stack = "`+srcStack+`"
  moves = {
    "module.main.module.cms[0]" = "module.cms"
  }
}
`)

	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	err := validateXMoveManifest(dir, dir)

	w.Close()
	os.Stderr = oldStderr

	if err != nil {
		t.Fatalf("expected spent manifest to validate clean, got error: %v", err)
	}

	var buf [2048]byte
	n, _ := r.Read(buf[:])
	out := string(buf[:n])

	if !strings.Contains(out, "xmove/spent") || !strings.Contains(out, "already applied") {
		t.Fatalf("expected per-entry xmove/spent info line, got: %q", out)
	}
	if strings.Contains(out, "❌") {
		t.Fatalf("expected no error output for spent manifest, got: %q", out)
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

	// Source plan has valid resource delete. prior_state carries the live-state
	// address so prior_state-based validation finds it; resource_changes is
	// supplementary only and must NOT be used as the source address set.
	write(srcStack+"/tfplan.json", `{"format_version":"1.2",`+
		`"prior_state":{"values":{"root_module":{"child_modules":[`+
		`{"address":"module.main","child_modules":[`+
		`{"address":"module.main.module.cms[0]","resources":[`+
		`{"address":"module.main.module.cms[0].postgresql_grant.a",`+
		`"type":"postgresql_grant","provider_name":"registry.terraform.io/cyrilgdn/postgresql"}]}]}]}}},`+
		`"resource_changes":[`+
		`{"address":"module.main.module.cms[0].postgresql_grant.a","type":"postgresql_grant","name":"a","provider_name":"registry.terraform.io/cyrilgdn/postgresql",`+
		`"change":{"actions":["delete"],"before":{"id":"abc"},"after":null}}]}`)

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

// D3: when the source plan is absent AND the dest prior_state already contains
// the to-addresses, validateXMoveManifest must emit xmove/spent (info, no error)
// instead of xmove/source-not-planned (error). A spent manifest is a green no-op.
func TestValidateXMoveManifest_SpentIsGreen(t *testing.T) {
	root := t.TempDir()
	plansDir := t.TempDir()

	const srcStack = "stacks/src"
	const dstStack = "stacks/dst"

	dstDir := filepath.Join(root, dstStack)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := statemove.RenderXMove("PR-20", statemove.XMove{
		SourceStack: srcStack,
		Pairs:       []statemove.Move{{From: "module.x", To: "module.y"}},
	})
	if err := os.WriteFile(filepath.Join(dstDir, statemove.XMoveFileName("PR-20")), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	// No source plan → source not in changed set.
	// Dest plan has prior_state containing module.y.* → move already applied.
	dstPlanDir := filepath.Join(plansDir, dstStack)
	if err := os.MkdirAll(dstPlanDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dstPlan := `{"format_version":"1.2","prior_state":{"values":{"root_module":{"child_modules":[` +
		`{"address":"module.y","resources":[` +
		`{"address":"module.y.google_project.p","type":"google_project","mode":"managed","provider_name":"registry.terraform.io/hashicorp/google"}` +
		`]}]}}}}`
	if err := os.WriteFile(filepath.Join(dstPlanDir, "tfplan.json"), []byte(dstPlan), 0o644); err != nil {
		t.Fatal(err)
	}

	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	err := validateXMoveManifest(root, plansDir)

	w.Close()
	os.Stderr = oldStderr

	// Spent manifest must NOT error — it plans green.
	if err != nil {
		t.Fatalf("spent manifest must not error validateXMoveManifest, got: %v", err)
	}
	var buf [2048]byte
	n, _ := r.Read(buf[:])
	out := string(buf[:n])
	if !strings.Contains(out, "spent") {
		t.Errorf("output must mention 'spent'; got:\n%s", out)
	}
}

// TestValidateXMoveManifest_HardErrorWhenNoPriorState verifies that
// validateXMoveManifest hard-errors rather than silently falling back to
// ResourceChanges when the source plan has no prior_state. A plan without
// prior_state means the source stack is new — there is nothing to move out.
// The old fallback produced post-moved{}-processing addresses, which diverge
// from the live-state addresses apply-time validation sees, causing false-safe
// classifications on plan while failing on apply.
// D1: when the source plan file is absent (source stack not in the changed set),
// the error message must name the diagnostic code xmove/source-not-planned and
// suggest the fix — not emit a raw file-not-found OS error.
func TestValidateXMoveManifest_SourceNotPlanned(t *testing.T) {
	root := t.TempDir()
	plansDir := t.TempDir()

	const srcStack = "stacks/src"
	const dstStack = "stacks/dst"

	dstDir := filepath.Join(root, dstStack)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := statemove.RenderXMove("PR-42", statemove.XMove{
		SourceStack: srcStack,
		Pairs:       []statemove.Move{{From: "module.x", To: "module.x"}},
	})
	if err := os.WriteFile(filepath.Join(dstDir, statemove.XMoveFileName("PR-42")), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	// No source plan written — simulates source stack absent from changed set.

	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	err := validateXMoveManifest(root, plansDir)

	w.Close()
	os.Stderr = oldStderr

	if err == nil {
		t.Fatal("expected error when source plan is absent, got nil")
	}
	var buf [2048]byte
	n, _ := r.Read(buf[:])
	out := string(buf[:n])

	// Must mention the diagnostic and suggest the fix — not dump a raw OS error.
	if strings.Contains(out, "no such file or directory") {
		t.Errorf("message must not expose raw OS error; got:\n%s", out)
	}
	if !strings.Contains(out, "source-not-planned") {
		t.Errorf("message must mention source-not-planned; got:\n%s", out)
	}
}

// D4/Phase 3: when the source prior_state contains data sources under the From
// prefix, validateXMoveManifest must emit a warning (xmove/data-source-orphan)
// but NOT return an error — the classify pass continues green, only the warning
// tells the operator those data sources will remain in the source stack.
func TestValidateXMoveManifest_DataSourceOrphan(t *testing.T) {
	root := t.TempDir()
	plansDir := t.TempDir()

	const srcStack = "stacks/src"
	const dstStack = "stacks/dst"

	dstDir := filepath.Join(root, dstStack)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := statemove.RenderXMove("PR-30", statemove.XMove{
		SourceStack: srcStack,
		Pairs:       []statemove.Move{{From: "module.x", To: "module.y"}},
	})
	if err := os.WriteFile(filepath.Join(dstDir, statemove.XMoveFileName("PR-30")), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	// Provider block so DiscoverDestProviders does not emit xmove/provider-missing.
	if err := os.WriteFile(filepath.Join(dstDir, "providers.tf"), []byte(`provider "google" {}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Source plan: one managed resource AND one data source under module.x.
	// The managed resource is the one being moved; the data source will be stranded.
	srcPlanDir := filepath.Join(plansDir, srcStack)
	if err := os.MkdirAll(srcPlanDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcPlan := `{"format_version":"1.2","prior_state":{"values":{"root_module":{"child_modules":[{"address":"module.x","resources":[` +
		`{"address":"module.x.google_project.p","mode":"managed","type":"google_project","provider_name":"registry.terraform.io/hashicorp/google","values":{}},` +
		`{"address":"module.x.data.google_secret_manager_secret_version.v","mode":"data","type":"google_secret_manager_secret_version","provider_name":"registry.terraform.io/hashicorp/google","values":{}}` +
		`]}]}}}}`
	if err := os.WriteFile(filepath.Join(srcPlanDir, "tfplan.json"), []byte(srcPlan), 0o644); err != nil {
		t.Fatal(err)
	}

	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	err := validateXMoveManifest(root, plansDir)

	w.Close()
	os.Stderr = oldStderr

	// Data-source orphan is a WARNING, not an error — classify pass must succeed.
	if err != nil {
		t.Fatalf("data-source orphan must be a warning, not an error; got: %v", err)
	}
	var buf [2048]byte
	n, _ := r.Read(buf[:])
	out := string(buf[:n])
	// Warning must name the diagnostic and the orphaned address.
	if !strings.Contains(out, "data-source-orphan") {
		t.Errorf("warning must mention data-source-orphan; got:\n%s", out)
	}
	if !strings.Contains(out, "module.x.data.google_secret_manager_secret_version.v") {
		t.Errorf("warning must name the orphaned data source; got:\n%s", out)
	}
}

func TestValidateXMoveManifest_HardErrorWhenNoPriorState(t *testing.T) {
	// Create a dest stack with an xmove manifest and a source plan that has
	// no prior_state (only ResourceChanges). The validator must hard-error
	// rather than silently falling back to ResourceChanges.
	root := t.TempDir()
	plansDir := t.TempDir()

	const srcStack = "src"
	const dstStack = "dst"
	const key = "PR-99"

	// Write xmove manifest in dest stack (under root)
	dstDir := filepath.Join(root, dstStack)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := statemove.RenderXMove(key, statemove.XMove{
		SourceStack: srcStack,
		Pairs:       []statemove.Move{{From: "module.perms", To: "module.dest"}},
	})
	if err := os.WriteFile(filepath.Join(dstDir, statemove.XMoveFileName(key)), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write source plan with ResourceChanges only (no prior_state)
	srcPlanDir := filepath.Join(plansDir, srcStack)
	if err := os.MkdirAll(srcPlanDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcPlan := `{"format_version":"1.2","resource_changes":[{"address":"module.perms.google_project_iam_member.x","type":"google_project_iam_member","change":{"actions":["delete"]}}]}`
	if err := os.WriteFile(filepath.Join(srcPlanDir, "tfplan.json"), []byte(srcPlan), 0o644); err != nil {
		t.Fatal(err)
	}

	err := validateXMoveManifest(root, plansDir)
	if err == nil {
		t.Fatal("expected error when source plan has no prior_state, got nil")
	}
}
