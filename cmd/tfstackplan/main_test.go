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
