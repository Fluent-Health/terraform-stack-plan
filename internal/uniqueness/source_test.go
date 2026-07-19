package uniqueness

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/config"
)

// TestLoadUnits verifies the happy path against testdata/repo: one instance
// manifest under the default glob (components/**/instances/*.tm.yml) yields
// one Unit whose ID strips the components/ prefix and .tm.yml suffix, whose
// Envs are the sorted environment names found under the default
// "environments" path, and whose Inputs are each env's "inputs" flattened
// (including the tier leaf as an ordinary leaf — tier-stripping is
// Evaluate's concern, not LoadUnits').
func TestLoadUnits(t *testing.T) {
	units, err := LoadUnits("testdata/repo", config.SourceBlock{})
	if err != nil {
		t.Fatalf("LoadUnits() error = %v", err)
	}

	if len(units) != 1 {
		t.Fatalf("LoadUnits() returned %d units, want 1: %#v", len(units), units)
	}

	u := units[0]
	if u.ID != "x/api/instances/api" {
		t.Errorf("Unit.ID = %q, want %q", u.ID, "x/api/instances/api")
	}
	if !reflect.DeepEqual(u.Envs, []string{"dev", "prod"}) {
		t.Errorf("Unit.Envs = %v, want [dev prod]", u.Envs)
	}
	if got := u.Inputs["prod"]["project_id"]; got != "app-prod" {
		t.Errorf(`Inputs["prod"]["project_id"] = %#v, want "app-prod"`, got)
	}
	if got := u.Inputs["dev"]["project_id"]; got != "app-dev" {
		t.Errorf(`Inputs["dev"]["project_id"] = %#v, want "app-dev"`, got)
	}
	if got := u.Inputs["prod"]["tier_class"]; got != "prod" {
		t.Errorf(`Inputs["prod"]["tier_class"] = %#v, want "prod" (tier leaf must survive as an ordinary leaf)`, got)
	}
	if got := u.Inputs["dev"]["tier_class"]; got != "nonprod" {
		t.Errorf(`Inputs["dev"]["tier_class"] = %#v, want "nonprod"`, got)
	}
}

// TestLoadUnitsCustomGlobAndPaths verifies LoadUnits honors a non-default
// SourceBlock: a custom <prefix>/**/<suffix-glob> shape, and custom
// environments/inputs paths, still discover and parse correctly. Also
// verifies files outside the glob (wrong suffix, wrong prefix) are ignored.
func TestLoadUnitsCustomGlobAndPaths(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "stacks", "y", "manifests", "svc.tm.yml"), `
envs:
  stage:
    config:
      region: eu-west-1
`)
	// Should NOT match: wrong suffix directory name.
	writeFile(t, filepath.Join(root, "stacks", "y", "other", "svc.tm.yml"), `
envs: {}
`)
	// Should NOT match: outside the fixed prefix.
	writeFile(t, filepath.Join(root, "elsewhere", "manifests", "svc.tm.yml"), `
envs: {}
`)

	units, err := LoadUnits(root, config.SourceBlock{
		Glob:             "stacks/**/manifests/*.tm.yml",
		EnvironmentsPath: "envs",
		InputsPath:       "config",
	})
	if err != nil {
		t.Fatalf("LoadUnits() error = %v", err)
	}
	if len(units) != 1 {
		t.Fatalf("LoadUnits() returned %d units, want 1: %#v", len(units), units)
	}
	u := units[0]
	if u.ID != "y/manifests/svc" {
		t.Errorf("Unit.ID = %q, want %q", u.ID, "y/manifests/svc")
	}
	if !reflect.DeepEqual(u.Envs, []string{"stage"}) {
		t.Errorf("Unit.Envs = %v, want [stage]", u.Envs)
	}
	if got := u.Inputs["stage"]["region"]; got != "eu-west-1" {
		t.Errorf(`Inputs["stage"]["region"] = %#v, want "eu-west-1"`, got)
	}
}

// TestLoadUnitsSkipsNoEnvironmentsPath verifies LoadUnits SKIPS a matched file
// that lacks the environments block entirely — a single-env / non-comparable
// instance (e.g. a per-env-file BundleInstance with only top-level inputs) has
// no cross-env values to compare — rather than failing loud. Comparable
// instances discovered alongside it still load.
func TestLoadUnitsSkipsNoEnvironmentsPath(t *testing.T) {
	root := t.TempDir()
	// A valid, comparable instance.
	writeFile(t, filepath.Join(root, "components", "a", "instances", "a.tm.yml"), `
environments:
  dev:
    inputs:
      x: 1
`)
	// A per-env-file instance with NO environments block — must be skipped.
	writeFile(t, filepath.Join(root, "components", "b", "instances", "fh-dev-svc.tm.yml"), `
metadata:
  name: fh-dev-svc
spec:
  inputs:
    project_id: fh-dev-svc
    tier_class: nonprod
`)

	units, err := LoadUnits(root, config.SourceBlock{})
	if err != nil {
		t.Fatalf("LoadUnits() error = %v, want nil (a no-environments file should be skipped)", err)
	}
	if len(units) != 1 || units[0].ID != "a/instances/a" {
		t.Fatalf("LoadUnits() = %#v, want exactly the one comparable unit a/instances/a", units)
	}
}

// TestLoadUnitsEmptyEnvironmentsMapLoads pins the skip boundary: a PRESENT but
// empty `environments: {}` map is loaded as a (zero-env) unit, NOT skipped —
// only a TOTALLY ABSENT environments path triggers the skip sentinel.
func TestLoadUnitsEmptyEnvironmentsMapLoads(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "components", "z", "instances", "empty-envs.tm.yml"), `
environments: {}
`)
	units, err := LoadUnits(root, config.SourceBlock{})
	if err != nil {
		t.Fatalf("LoadUnits() error = %v, want nil (empty environments map should load, not skip)", err)
	}
	if len(units) != 1 || len(units[0].Envs) != 0 {
		t.Fatalf("LoadUnits() = %#v, want one unit with zero envs", units)
	}
}

// TestLoadUnitsMalformedEnvironmentsStillErrors verifies fail-loud is preserved
// for a file that HAS an environments block but of the wrong shape (not a map):
// only a TOTAL ABSENCE of the environments path is a skip, not a malformed one.
func TestLoadUnitsMalformedEnvironmentsStillErrors(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "components", "z", "instances", "bad.tm.yml"), `
environments: "not a map"
`)
	if _, err := LoadUnits(root, config.SourceBlock{}); err == nil {
		t.Fatal("LoadUnits() error = nil, want an error for a malformed (non-map) environments block")
	}
}

// TestLoadUnitsEmptyInputs verifies an environment with no inputs at all
// yields an empty (not nil-panicking) flattened map for that env.
func TestLoadUnitsEmptyInputs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "components", "z", "instances", "empty.tm.yml"), `
environments:
  dev: {}
`)

	units, err := LoadUnits(root, config.SourceBlock{})
	if err != nil {
		t.Fatalf("LoadUnits() error = %v", err)
	}
	if len(units) != 1 {
		t.Fatalf("LoadUnits() returned %d units, want 1: %#v", len(units), units)
	}
	if got := units[0].Inputs["dev"]; len(got) != 0 {
		t.Errorf(`Inputs["dev"] = %#v, want empty map`, got)
	}
}

// TestLoadUnitsDeepNesting verifies the "**" matcher handles multiple
// directories between the fixed prefix and the suffix glob — the real
// target tree nests components 2-3 levels deep (components/a/b/c/instances,
// not just components/x/instances) — using a separate testdata root
// (testdata/deep) so its unit count doesn't interact with TestLoadUnits'
// testdata/repo fixture.
func TestLoadUnitsDeepNesting(t *testing.T) {
	units, err := LoadUnits("testdata/deep", config.SourceBlock{})
	if err != nil {
		t.Fatalf("LoadUnits() error = %v", err)
	}
	if len(units) != 1 {
		t.Fatalf("LoadUnits() returned %d units, want 1: %#v", len(units), units)
	}

	u := units[0]
	if u.ID != "a/b/c/instances/deep" {
		t.Errorf("Unit.ID = %q, want %q", u.ID, "a/b/c/instances/deep")
	}
	if !reflect.DeepEqual(u.Envs, []string{"dev", "prod"}) {
		t.Errorf("Unit.Envs = %v, want [dev prod]", u.Envs)
	}
	if got := u.Inputs["prod"]["project_id"]; got != "app-prod" {
		t.Errorf(`Inputs["prod"]["project_id"] = %#v, want "app-prod"`, got)
	}
}

// TestLoadUnitsUnparseableYAML verifies a matched file with malformed YAML
// is a hard error (fail loud), not a silent skip.
func TestLoadUnitsUnparseableYAML(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "components", "z", "instances", "bad.tm.yml"), `
environments: [dev
    inputs: {project_id: app-dev}
`)

	_, err := LoadUnits(root, config.SourceBlock{})
	if err == nil {
		t.Fatal("LoadUnits() error = nil, want an error for malformed YAML")
	}
}

// TestLoadUnitsMalformedGlob verifies a SourceBlock.Glob that lacks the
// "<dir>/**/<pattern>" shape is a hard error from splitGlob, not silently
// treated as matching nothing.
func TestLoadUnitsMalformedGlob(t *testing.T) {
	root := t.TempDir()

	_, err := LoadUnits(root, config.SourceBlock{Glob: "components/instances/*.tm.yml"})
	if err == nil {
		t.Fatal("LoadUnits() error = nil, want an error for a glob missing the <dir>/**/<pattern> shape")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for fixture %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture %s: %v", path, err)
	}
}
