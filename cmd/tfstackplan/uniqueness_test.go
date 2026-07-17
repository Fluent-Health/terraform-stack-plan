package main

import (
	"os"
	"path/filepath"
	"testing"
)

// uniquenessHCLBase is a generic env_uniqueness{} block: tiers are resolved
// from data (source.tier_input = "tier_class"), a project token template
// derives per-env tokens, and one allow{} justifies the fixture's only
// cross-boundary duplicate (svc/api's shared_secret, dev+prod).
const uniquenessHCLBase = `
env_uniqueness {
  project_token_template = "acme-{env}"

  source {
    tier_input = "tier_class"
  }

  allow {
    unit   = "svc/api/instances/api"
    key    = "shared_secret"
    envs   = ["dev", "prod"]
    reason = "shared sandbox credential by design"
  }
}
`

// uniquenessUnitClean has one cross-boundary duplicate (shared_secret,
// identical dev/prod) that the allow above justifies, and a project_id that
// differs per env — so the unit is clean overall.
const uniquenessUnitClean = `
environments:
  dev:
    inputs:
      tier_class: nonprod
      shared_secret: "12345678-1234-1234-1234-123456789012"
      project_id: app-dev
  prod:
    inputs:
      tier_class: prod
      shared_secret: "12345678-1234-1234-1234-123456789012"
      project_id: app-prod
`

// uniquenessUnitWithUnjustifiedDup adds a second cross-boundary duplicate
// (account_id, identical dev/prod) that no allow{} covers.
const uniquenessUnitWithUnjustifiedDup = `
environments:
  dev:
    inputs:
      tier_class: nonprod
      shared_secret: "12345678-1234-1234-1234-123456789012"
      project_id: app-dev
      account_id: "999999999999"
  prod:
    inputs:
      tier_class: prod
      shared_secret: "12345678-1234-1234-1234-123456789012"
      project_id: app-prod
      account_id: "999999999999"
`

// writeUniquenessRepo builds a temp repo dir with a .tfstackplan.hcl
// (hclBody, which may be empty for the no-block case) and one Catalyst
// instance manifest (unitYAML) under the default source glob. Returns the
// repo root.
func writeUniquenessRepo(t *testing.T, hclBody, unitYAML string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".tfstackplan.hcl"), []byte(hclBody), 0o644); err != nil {
		t.Fatal(err)
	}
	instDir := filepath.Join(dir, "components", "svc", "api", "instances")
	if err := os.MkdirAll(instDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(instDir, "api.tm.yml"), []byte(unitYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestRunUniquenessClean: a unit whose only cross-boundary duplicate is
// covered by an allow{} is clean — exit 0.
func TestRunUniquenessClean(t *testing.T) {
	dir := writeUniquenessRepo(t, uniquenessHCLBase, uniquenessUnitClean)
	if code := runUniqueness([]string{"--dir", dir}); code != 0 {
		t.Fatalf("runUniqueness() = %d, want 0 (clean)", code)
	}
}

// TestRunUniquenessUnjustifiedViolation: introducing an unjustified
// cross-boundary duplicate fails the command — exit 1.
func TestRunUniquenessUnjustifiedViolation(t *testing.T) {
	dir := writeUniquenessRepo(t, uniquenessHCLBase, uniquenessUnitWithUnjustifiedDup)
	if code := runUniqueness([]string{"--dir", dir}); code != 1 {
		t.Fatalf("runUniqueness() = %d, want 1 (unjustified violation)", code)
	}
}

// TestRunUniquenessNoBlock: a .tfstackplan.hcl with no env_uniqueness{} block
// is a usage error — exit 2.
func TestRunUniquenessNoBlock(t *testing.T) {
	dir := writeUniquenessRepo(t, "", uniquenessUnitClean)
	if code := runUniqueness([]string{"--dir", dir}); code != 2 {
		t.Fatalf("runUniqueness() = %d, want 2 (no env_uniqueness block)", code)
	}
}

// TestRunUniquenessFormatJSON: --format json still exits 0 on the clean
// fixture (exercises the JSON render path without asserting exact shape).
func TestRunUniquenessFormatJSON(t *testing.T) {
	dir := writeUniquenessRepo(t, uniquenessHCLBase, uniquenessUnitClean)
	if code := runUniqueness([]string{"--dir", dir, "--format", "json"}); code != 0 {
		t.Fatalf("runUniqueness() = %d, want 0 (clean, json format)", code)
	}
}

// TestRunUniquenessBadFormat: an unrecognized --format value is a usage
// error — exit 2.
func TestRunUniquenessBadFormat(t *testing.T) {
	dir := writeUniquenessRepo(t, uniquenessHCLBase, uniquenessUnitClean)
	if code := runUniqueness([]string{"--dir", dir, "--format", "xml"}); code != 2 {
		t.Fatalf("runUniqueness() = %d, want 2 (bad --format)", code)
	}
}

// TestDispatchRoutesUniqueness proves the "uniqueness" case in dispatch
// actually routes to runUniqueness, mirroring the dispatch-routing tests for
// sibling subcommands (e.g. TestDispatchWhoamiAnonymous in whoami_test.go).
func TestDispatchRoutesUniqueness(t *testing.T) {
	dir := writeUniquenessRepo(t, uniquenessHCLBase, uniquenessUnitClean)
	if code := dispatch([]string{"uniqueness", "--dir", dir}); code != 0 {
		t.Fatalf("dispatch uniqueness exit = %d, want 0", code)
	}
}
