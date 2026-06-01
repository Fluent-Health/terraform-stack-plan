package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const planJSON = `{
  "format_version": "1.2",
  "resource_changes": [
    {"address":"google_project_iam_member.editor","type":"google_project_iam_member","name":"editor",
     "change":{"actions":["update"],"before":{"role":"roles/viewer"},"after":{"role":"roles/editor"},
       "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}}
  ]
}`

const cfgHCL = `
classification {
  default {
    name = "safe"
    icon = "✅"
  }
  preset "iam" {
    icon = "🔐"
  }
}
`

// writePlan creates <root>/<name>/tfplan.json (name may contain forward slashes).
func writePlan(t *testing.T, root, name, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name), "tfplan.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunPlansDir(t *testing.T) {
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "out")
	writePlan(t, plansDir, "platform/nonprod", planJSON)
	cfgPath := filepath.Join(dir, "cfg.hcl")
	if err := os.WriteFile(cfgPath, []byte(cfgHCL), 0o644); err != nil {
		t.Fatal(err)
	}
	classOut := filepath.Join(dir, "classes.json")

	out, _, err := run(opts{
		plansDir:  plansDir,
		title:     "Terraform plan — nonprod",
		marker:    "tfstackplan:nonprod",
		config:    cfgPath,
		maxBytes:  60000,
		classJSON: classOut,
		details:   "closed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "<!-- tfstackplan:nonprod -->") {
		t.Fatalf("missing marker:\n%s", out)
	}
	if !strings.Contains(out, "🔐 iam") {
		t.Fatalf("iam classification not applied:\n%s", out)
	}
	data, err := os.ReadFile(classOut)
	if err != nil {
		t.Fatal(err)
	}
	var got sidecarDoc
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	cats := got.Stacks["platform/nonprod"].Categories
	if len(cats) != 1 || cats[0].Category != "iam" {
		t.Fatalf("platform/nonprod categories = %+v, want one iam", cats)
	}
}

func TestRunRedactsNestedSensitiveInStructuralDiff(t *testing.T) {
	// The user's scenario: a kubernetes_deployment_v1 where a non-sensitive cpu
	// request changed and a sibling env value is sensitive (nested marker). The
	// rendered comment must show the cpu change and redact only the secret — not
	// smear "(sensitive value)" across the whole spec.
	const deployPlan = `{
	  "format_version":"1.2",
	  "resource_changes":[
	    {"address":"module.cms.kubernetes_deployment_v1.app","type":"kubernetes_deployment_v1","name":"app",
	     "change":{"actions":["update"],
	       "before":{"spec":{"cpu":"334m","secret_env":"s3cr3t-old"}},
	       "after": {"spec":{"cpu":"300m","secret_env":"s3cr3t-new"}},
	       "after_unknown":{},
	       "before_sensitive":{"spec":{"secret_env":true}},
	       "after_sensitive": {"spec":{"secret_env":true}}}}
	  ]}`
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "out")
	writePlan(t, plansDir, "service-projects/prod", deployPlan)

	out, _, err := run(opts{
		plansDir: plansDir,
		title:    "T",
		marker:   "m",
		maxBytes: 60000,
		details:  "open",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "s3cr3t-old") || strings.Contains(out, "s3cr3t-new") {
		t.Fatalf("sensitive value leaked in rendered comment:\n%s", out)
	}
	if !strings.Contains(out, "334m") || !strings.Contains(out, "300m") {
		t.Fatalf("non-sensitive cpu change must be visible:\n%s", out)
	}
	if !strings.Contains(out, "(sensitive value)") {
		t.Fatalf("expected (sensitive value) marker:\n%s", out)
	}
}

func TestRunMissingPlansDirFlag(t *testing.T) {
	_, _, err := run(opts{maxBytes: 60000})
	if err == nil || !strings.Contains(err.Error(), "--plans-dir") {
		t.Fatalf("expected --plans-dir hint, got %v", err)
	}
}

func TestRunNonexistentPlansDir(t *testing.T) {
	_, _, err := run(opts{plansDir: filepath.Join(t.TempDir(), "nope"), maxBytes: 60000})
	if err == nil {
		t.Fatal("expected error for nonexistent plans-dir")
	}
}

func TestRunNoConfigNoClassColumn(t *testing.T) {
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "out")
	writePlan(t, plansDir, "s", planJSON)
	out, _, err := run(opts{
		plansDir: plansDir,
		title:    "T",
		marker:   "m",
		maxBytes: 60000,
		details:  "closed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Categories") {
		t.Fatalf("no config → no Categories column:\n%s", out)
	}
}

func TestRunEmitsLinks(t *testing.T) {
	dir := t.TempDir()
	// Source tree lives at the repo root, under the stack's path.
	srcDir := filepath.Join(dir, "platform", "nonprod")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "main.tf"),
		[]byte("resource \"google_project_iam_member\" \"editor\" {\n  role = \"x\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Plans live in a separate out/ tree.
	plansDir := filepath.Join(dir, "out")
	writePlan(t, plansDir, "platform/nonprod", planJSON)
	cfgPath := filepath.Join(dir, "cfg.hcl")
	if err := os.WriteFile(cfgPath, []byte(`links {
  resource = "https://gh/o/r/blob/{sha}/{file}#L{line}"
  stack    = "https://gh/o/r/tree/{sha}/{stack_dir}"
  header {
    label = "PR #{pr}"
    url   = "https://gh/o/r/pull/{pr}"
  }
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, err := run(opts{
		plansDir: plansDir,
		config:   cfgPath,
		maxBytes: 60000,
		details:  "open",
		repoRoot: dir,
		linkVars: []string{"sha=abc1234", "pr=42"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[PR #42](https://gh/o/r/pull/42)") {
		t.Fatalf("header link missing:\n%s", out)
	}
	// Source dir is join(repoRoot, name), so the file path is name-relative.
	if !strings.Contains(out, "https://gh/o/r/blob/abc1234/platform/nonprod/main.tf#L1") {
		t.Fatalf("resource link missing:\n%s", out)
	}
	if !strings.Contains(out, "tree/abc1234/platform/nonprod") {
		t.Fatalf("stack link missing:\n%s", out)
	}
}

func TestRunEmitsClassificationAttributes(t *testing.T) {
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "out")

	// platform/nonprod: deleted IAM members → match BOTH iam and destructive.
	iamPlan := `{
	  "format_version": "1.2",
	  "resource_changes": [
	    {"address":"google_project_iam_member.a","type":"google_project_iam_member","name":"a",
	     "change":{"actions":["delete"],"before":{"role":"roles/x","project":"fh-host-nonprod"},"after":null,
	       "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}},
	    {"address":"google_project_iam_member.b","type":"google_project_iam_member","name":"b",
	     "change":{"actions":["delete"],"before":{"role":"roles/y","project":"fh-svc-dev"},"after":null,
	       "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}}
	  ]
	}`
	// data/warehouse: a single bucket delete → destructive only.
	safePlan := `{
	  "format_version": "1.2",
	  "resource_changes": [
	    {"address":"google_storage_bucket.b","type":"google_storage_bucket","name":"b",
	     "change":{"actions":["delete"],"before":{"name":"bkt"},"after":null,
	       "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}}
	  ]
	}`
	cfg := `
classification {
  default {
    name = "safe"
    icon = "✅"
  }
  preset "iam" {
    icon            = "🔐"
    emit_attributes = ["project"]
  }
  rule "destructive" {
    icon      = "💣"
    actions   = ["delete"]
    min_count = 1
  }
}
`
	safeCreate := `{
	  "format_version": "1.2",
	  "resource_changes": [
	    {"address":"google_storage_bucket.new","type":"google_storage_bucket","name":"new",
	     "change":{"actions":["create"],"before":null,"after":{"name":"newbkt"},
	       "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}}
	  ]
	}`
	writePlan(t, plansDir, "platform/nonprod", iamPlan)
	writePlan(t, plansDir, "data/warehouse", safePlan)
	writePlan(t, plansDir, "service-projects/app-dev", safeCreate)
	cfgPath := filepath.Join(dir, "cfg.hcl")
	classOut := filepath.Join(dir, "classes.json")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := run(opts{
		plansDir:  plansDir,
		config:    cfgPath,
		maxBytes:  60000,
		classJSON: classOut,
		details:   "closed",
	})
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(classOut)
	if err != nil {
		t.Fatal(err)
	}
	var got sidecarDoc
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	// platform/nonprod carries both categories, in rule order (iam, destructive).
	pcats := got.Stacks["platform/nonprod"].Categories
	if len(pcats) != 2 || pcats[0].Category != "iam" || pcats[1].Category != "destructive" {
		t.Fatalf("platform/nonprod categories = %+v, want [iam destructive]", pcats)
	}
	if iam := pcats[0]; len(iam.Attributes["project"]) != 2 ||
		iam.Attributes["project"][0] != "fh-host-nonprod" || iam.Attributes["project"][1] != "fh-svc-dev" {
		t.Fatalf("iam project = %v, want [fh-host-nonprod fh-svc-dev]", iam.Attributes["project"])
	}

	// data/warehouse is destructive only.
	dcats := got.Stacks["data/warehouse"].Categories
	if len(dcats) != 1 || dcats[0].Category != "destructive" {
		t.Fatalf("data/warehouse categories = %+v, want [destructive]", dcats)
	}

	// A stack matching no rule serializes an empty categories list (not null),
	// and contributes nothing to the summary.
	scats := got.Stacks["service-projects/app-dev"].Categories
	if scats == nil || len(scats) != 0 {
		t.Fatalf("service-projects/app-dev categories = %v, want non-nil empty []", scats)
	}

	// Summary unions both categories present, in rule order.
	if len(got.Summary.Categories) != 2 ||
		got.Summary.Categories[0].Category != "iam" || got.Summary.Categories[1].Category != "destructive" {
		t.Fatalf("summary categories = %+v, want [iam destructive]", got.Summary.Categories)
	}
	if proj := got.Summary.Categories[0].Attributes["project"]; len(proj) != 2 {
		t.Fatalf("summary iam project union = %v, want 2 values", proj)
	}
	// destructive declares no emit_attributes → its attributes field is omitted.
	if got.Summary.Categories[1].Attributes != nil {
		t.Fatalf("destructive must have no attributes, got %v", got.Summary.Categories[1].Attributes)
	}
}

func TestRunEmptyPlansDir(t *testing.T) {
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(plansDir, 0o755); err != nil { // exists, no plans
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "cfg.hcl")
	if err := os.WriteFile(cfgPath, []byte(cfgHCL), 0o644); err != nil {
		t.Fatal(err)
	}
	classOut := filepath.Join(dir, "classes.json")

	out, fits, err := run(opts{
		plansDir:  plansDir,
		config:    cfgPath,
		maxBytes:  60000,
		classJSON: classOut,
		details:   "closed",
		title:     "Terraform plan — nonprod",
		marker:    "tf-plan:nonprod",
	})
	if err != nil {
		t.Fatalf("empty plans-dir run should not error: %v", err)
	}
	if !fits {
		t.Fatal("empty report should fit the budget")
	}
	if !strings.HasPrefix(out, "<!-- tf-plan:nonprod -->") {
		t.Fatalf("missing marker:\n%s", out)
	}
	if !strings.Contains(out, "(0 stacks changed)") {
		t.Fatalf("missing 0-stacks heading:\n%s", out)
	}
	if strings.Contains(out, "| Stack |") {
		t.Fatalf("empty report must not render a table:\n%s", out)
	}
	data, err := os.ReadFile(classOut)
	if err != nil {
		t.Fatal(err)
	}
	var got sidecarDoc
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Stacks) != 0 || len(got.Summary.Categories) != 0 {
		t.Fatalf("empty run should have no stacks and no summary categories, got: %s", data)
	}
}
