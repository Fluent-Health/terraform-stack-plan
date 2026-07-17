package uniqueness

import (
	"reflect"
	"testing"
)

// TestFindDuplicatesCrossBoundaryIdentifierViolation verifies an identical
// identifier-shaped value (matches the "_id$" key pattern) present in two
// envs whose tiers span both the protected tier and a non-protected tier
// produces exactly one blocking SeverityViolation.
func TestFindDuplicatesCrossBoundaryIdentifierViolation(t *testing.T) {
	u := Unit{
		ID:   "app-dev",
		Envs: []string{"dev", "prod"},
		Inputs: map[string]map[string]any{
			"dev":  {"project_id": "acme-shared-project"},
			"prod": {"project_id": "acme-shared-project"},
		},
	}
	tierOf := map[string]Tier{"dev": "nonprod", "prod": "prod"}

	got := FindDuplicates(u, tierOf, "prod", newDefaultClassifier())

	want := []Violation{{
		Unit:     "app-dev",
		Key:      "project_id",
		Value:    "acme-shared-project",
		Envs:     []string{"dev", "prod"},
		Kind:     KindDuplicate,
		Severity: SeverityViolation,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FindDuplicates() = %#v, want %#v", got, want)
	}
}

// TestFindDuplicatesWithinUnprotectedIsReportOnly verifies an identical
// identifier-shaped value present only across non-protected-tier envs is
// SeverityReportOnly, not a blocking violation.
func TestFindDuplicatesWithinUnprotectedIsReportOnly(t *testing.T) {
	u := Unit{
		ID:   "app-dev",
		Envs: []string{"dev1", "dev2"},
		Inputs: map[string]map[string]any{
			"dev1": {"client_id": "acme-client-1234"},
			"dev2": {"client_id": "acme-client-1234"},
		},
	}
	tierOf := map[string]Tier{"dev1": "nonprod", "dev2": "nonprod"}

	got := FindDuplicates(u, tierOf, "prod", newDefaultClassifier())

	want := []Violation{{
		Unit:     "app-dev",
		Key:      "client_id",
		Value:    "acme-client-1234",
		Envs:     []string{"dev1", "dev2"},
		Kind:     KindDuplicate,
		Severity: SeverityReportOnly,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FindDuplicates() = %#v, want %#v", got, want)
	}
}

// TestFindDuplicatesBoolNeverFlagged verifies an identical bool value across
// protected/non-protected envs is never a duplicate finding, since
// Classifier.IsIdentifier never classifies a bool as an identifier.
func TestFindDuplicatesBoolNeverFlagged(t *testing.T) {
	u := Unit{
		ID:   "app-dev",
		Envs: []string{"dev", "prod"},
		Inputs: map[string]map[string]any{
			"dev":  {"enabled": true},
			"prod": {"enabled": true},
		},
	}
	tierOf := map[string]Tier{"dev": "nonprod", "prod": "prod"}

	got := FindDuplicates(u, tierOf, "prod", newDefaultClassifier())

	if len(got) != 0 {
		t.Fatalf("FindDuplicates() = %#v, want no violations for a duplicated bool", got)
	}
}

// TestFindDuplicatesListOfUUIDsCrossBoundary verifies a List value (e.g. a
// list of UUIDs) identical across a protected/non-protected env pair is
// still detected as a single duplicate violation — IsIdentifier flags a List
// if any element matches an identifier pattern.
func TestFindDuplicatesListOfUUIDsCrossBoundary(t *testing.T) {
	list := List{Elems: []string{
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
	}}
	u := Unit{
		ID:   "app-dev",
		Envs: []string{"dev", "prod"},
		Inputs: map[string]map[string]any{
			"dev":  {"allowed_ids": list},
			"prod": {"allowed_ids": list},
		},
	}
	tierOf := map[string]Tier{"dev": "nonprod", "prod": "prod"}

	got := FindDuplicates(u, tierOf, "prod", newDefaultClassifier())

	want := []Violation{{
		Unit:     "app-dev",
		Key:      "allowed_ids",
		Value:    list,
		Envs:     []string{"dev", "prod"},
		Kind:     KindDuplicate,
		Severity: SeverityViolation,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FindDuplicates() = %#v, want %#v", got, want)
	}
}

// TestFindDuplicatesMissingTierFailsClosedToProtected verifies an env absent
// from tierOf is treated as the protected tier (fail-closed), so a duplicate
// spanning a known non-protected env and an env with no tier entry is still
// a blocking violation.
func TestFindDuplicatesMissingTierFailsClosedToProtected(t *testing.T) {
	u := Unit{
		ID:   "app-dev",
		Envs: []string{"dev", "unknown-env"},
		Inputs: map[string]map[string]any{
			"dev":         {"project_id": "acme-shared-project"},
			"unknown-env": {"project_id": "acme-shared-project"},
		},
	}
	// "unknown-env" deliberately has no entry in tierOf.
	tierOf := map[string]Tier{"dev": "nonprod"}

	got := FindDuplicates(u, tierOf, "prod", newDefaultClassifier())

	want := []Violation{{
		Unit:     "app-dev",
		Key:      "project_id",
		Value:    "acme-shared-project",
		Envs:     []string{"dev", "unknown-env"},
		Kind:     KindDuplicate,
		Severity: SeverityViolation,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FindDuplicates() = %#v, want %#v", got, want)
	}
}

// TestFindDuplicatesSkipsEnvScopedKeys verifies a key that is env-scoped
// (per Classifier.IsEnvScoped) is skipped entirely, even if its value is
// identical and identifier-shaped across protected/non-protected envs.
func TestFindDuplicatesSkipsEnvScopedKeys(t *testing.T) {
	u := Unit{
		ID:   "app-dev",
		Envs: []string{"dev", "prod"},
		Inputs: map[string]map[string]any{
			"dev":  {"environments.dev.project_id": "acme-shared-project"},
			"prod": {"environments.dev.project_id": "acme-shared-project"},
		},
	}
	tierOf := map[string]Tier{"dev": "nonprod", "prod": "prod"}
	c := newDefaultClassifier()
	c.ScopedSegs = map[string]bool{"dev": true}

	got := FindDuplicates(u, tierOf, "prod", c)

	if len(got) != 0 {
		t.Fatalf("FindDuplicates() = %#v, want no violations for an env-scoped key", got)
	}
}
