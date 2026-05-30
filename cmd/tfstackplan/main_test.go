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

func TestRunEndToEnd(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(planPath, []byte(planJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "cfg.hcl")
	if err := os.WriteFile(cfgPath, []byte(cfgHCL), 0o644); err != nil {
		t.Fatal(err)
	}
	classOut := filepath.Join(dir, "classes.json")

	out, _, err := run(opts{
		stacks:    []string{"platform/nonprod:" + planPath},
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

func TestRunBothInputsError(t *testing.T) {
	_, _, err := run(opts{manifestPath: "x.yaml", stacks: []string{"a:b"}, maxBytes: 60000})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutually-exclusive error, got %v", err)
	}
}

func TestRunNeitherInputError(t *testing.T) {
	_, _, err := run(opts{maxBytes: 60000})
	if err == nil {
		t.Fatal("expected error when no stacks given")
	}
}

func TestRunManifestTitleMarkerOverride(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(planPath, []byte(planJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	manPath := filepath.Join(dir, "m.yaml")
	man := "title: \"From Manifest\"\nmarker: \"mk\"\nstacks:\n  - name: s\n    plan: " + planPath + "\n"
	if err := os.WriteFile(manPath, []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
	// CLI title/marker left at defaults → manifest values fill in.
	out, _, err := run(opts{manifestPath: manPath, title: "Terraform plan", marker: "tfstackplan", maxBytes: 60000, details: "closed"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "From Manifest") || !strings.Contains(out, "<!-- mk -->") {
		t.Fatalf("manifest title/marker should fill defaults:\n%s", out)
	}
}

func TestRunNoConfigNoClassColumn(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	os.WriteFile(planPath, []byte(planJSON), 0o644)
	out, _, err := run(opts{
		stacks:   []string{"s:" + planPath},
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
	if err := os.WriteFile(filepath.Join(dir, "main.tf"),
		[]byte("resource \"google_project_iam_member\" \"editor\" {\n  role = \"x\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(planPath, []byte(planJSON), 0o644); err != nil {
		t.Fatal(err)
	}
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
		stacks:   []string{"platform/nonprod:" + planPath},
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
	if !strings.Contains(out, "https://gh/o/r/blob/abc1234/main.tf#L1") {
		t.Fatalf("resource link missing:\n%s", out)
	}
	if !strings.Contains(out, "tree/abc1234/") {
		t.Fatalf("stack link missing:\n%s", out)
	}
}

func TestRunEmitsClassificationAttributes(t *testing.T) {
	dir := t.TempDir()

	// IAM stack: two members across two projects; one safe stack with none.
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
	iamPath := filepath.Join(dir, "iam.json")
	safePath := filepath.Join(dir, "safe.json")
	cfgPath := filepath.Join(dir, "cfg.hcl")
	classOut := filepath.Join(dir, "classes.json")
	for p, c := range map[string]string{iamPath: iamPlan, safePath: safePlan, cfgPath: cfg} {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	_, _, err := run(opts{
		stacks:    []string{"platform/nonprod:" + iamPath, "data/warehouse:" + safePath},
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

	// Safe stack: the "attributes" key must be absent entirely (omitempty),
	// even though its bucket carries a "project" attribute.
	if safe := got["data/warehouse"]; safe.Attributes != nil {
		t.Fatalf("safe stack must not emit attributes, got %v", safe.Attributes)
	}
	if !strings.Contains(string(data), "platform/nonprod") {
		t.Fatal("sidecar missing iam stack")
	}
	if strings.Contains(string(data), "fh-data") {
		t.Fatal("safe stack must not emit attributes (omitempty); found its project in raw JSON")
	}
}

func TestRunEmptyManifest(t *testing.T) {
	dir := t.TempDir()
	manPath := filepath.Join(dir, "empty.yaml")
	if err := os.WriteFile(manPath,
		[]byte("title: \"Terraform plan — nonprod\"\nmarker: \"tf-plan:nonprod\"\nstacks: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "cfg.hcl")
	if err := os.WriteFile(cfgPath, []byte(cfgHCL), 0o644); err != nil {
		t.Fatal(err)
	}
	classOut := filepath.Join(dir, "classes.json")

	out, fits, err := run(opts{
		manifestPath: manPath,
		config:       cfgPath,
		maxBytes:     60000,
		classJSON:    classOut,
		details:      "closed",
		marker:       "tfstackplan",
	})
	if err != nil {
		t.Fatalf("empty manifest run should not error: %v", err)
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
