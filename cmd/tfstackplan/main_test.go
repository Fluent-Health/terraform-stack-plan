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
	var got map[string]struct {
		Class string `json:"class"`
		Icon  string `json:"icon"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["platform/nonprod"].Class != "iam" {
		t.Fatalf("sidecar class = %q, want iam", got["platform/nonprod"].Class)
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
	if strings.Contains(out, "Class") {
		t.Fatalf("no config → no Class column:\n%s", out)
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

	iamPlan := `{
	  "format_version": "1.2",
	  "resource_changes": [
	    {"address":"google_project_iam_member.a","type":"google_project_iam_member","name":"a",
	     "change":{"actions":["create"],"after":{"role":"roles/x","project":"fh-host-nonprod"},
	       "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}},
	    {"address":"google_project_iam_member.b","type":"google_project_iam_member","name":"b",
	     "change":{"actions":["create"],"after":{"role":"roles/y","project":"fh-svc-dev"},
	       "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}}
	  ]
	}`
	safePlan := `{
	  "format_version": "1.2",
	  "resource_changes": [
	    {"address":"google_storage_bucket.b","type":"google_storage_bucket","name":"b",
	     "change":{"actions":["create"],"after":{"name":"bkt","project":"fh-data"},
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
}
`
	writePlan(t, plansDir, "platform/nonprod", iamPlan)
	writePlan(t, plansDir, "data/warehouse", safePlan)
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
	var got map[string]struct {
		Class      string              `json:"class"`
		Attributes map[string][]string `json:"attributes"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	iam := got["platform/nonprod"]
	want := []string{"fh-host-nonprod", "fh-svc-dev"}
	if len(iam.Attributes["project"]) != 2 || iam.Attributes["project"][0] != want[0] || iam.Attributes["project"][1] != want[1] {
		t.Fatalf("iam attributes.project = %v, want %v", iam.Attributes["project"], want)
	}
	if safe := got["data/warehouse"]; safe.Attributes != nil {
		t.Fatalf("safe stack must not emit attributes, got %v", safe.Attributes)
	}
	if strings.Contains(string(data), "fh-data") {
		t.Fatal("safe stack must not emit attributes (omitempty); found its project in raw JSON")
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
	if strings.TrimSpace(string(data)) != "{}" {
		t.Fatalf("sidecar should be empty object, got: %s", data)
	}
}
