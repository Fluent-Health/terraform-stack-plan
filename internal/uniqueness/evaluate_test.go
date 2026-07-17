package uniqueness

import (
	"strings"
	"testing"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/config"
)

// evalTestNow is a fixed "now" for evaluate tests so allow expiry checks are
// deterministic.
var evalTestNow = time.Date(2026, time.July, 17, 0, 0, 0, 0, time.UTC)

// declaredTierConfig builds an EnvUniquenessConfig with declared environment
// tiers (no Source.TierInput), one justified allow (svc/api-a's client_id
// cross-boundary dup) and one stale allow (matches nothing), for the main
// synthetic evaluate scenario.
func declaredTierConfig() *config.EnvUniquenessConfig {
	return &config.EnvUniquenessConfig{
		ProtectedTier: "prod",
		Environments: []config.EnvBlock{
			{Name: "dev", Tier: "nonprod"},
			{Name: "staging", Tier: "nonprod"},
			{Name: "prod", Tier: "prod"},
		},
		Allows: []config.AllowBlock{
			{
				Unit:   "svc/api-a",
				Key:    "client_id",
				Envs:   []string{"dev", "prod"},
				Reason: "shared sandbox project by design",
			},
			{
				Unit:   "svc/api-a",
				Key:    "nonexistent_id",
				Envs:   []string{"dev", "prod"},
				Reason: "no longer applies to anything",
			},
		},
	}
}

// evalTestUnits builds the 2-unit synthetic fixture: svc/api-a has a
// cross-boundary client_id dup (justified by the allow above) plus a
// within-nonprod app_id dup (report-only, dev+staging); svc/api-b has a
// cross-boundary account_id dup with no allow (unjustified).
func evalTestUnits() []Unit {
	return []Unit{
		{
			ID:   "svc/api-a",
			Envs: []string{"dev", "staging", "prod"},
			Inputs: map[string]map[string]any{
				"dev":     {"client_id": "acme-shared-client", "app_id": "acme-nonprod-app"},
				"staging": {"app_id": "acme-nonprod-app"},
				"prod":    {"client_id": "acme-shared-client"},
			},
		},
		{
			ID:   "svc/api-b",
			Envs: []string{"dev", "staging", "prod"},
			Inputs: map[string]map[string]any{
				"dev":     {"account_id": "acme-shared-account"},
				"staging": {},
				"prod":    {"account_id": "acme-shared-account"},
			},
		},
	}
}

// TestEvaluateJustifiedUnjustifiedStaleReportOnly is the main synthetic
// scenario from the task brief: a justified cross-boundary dup produces no
// unjustified entry, an unjustified cross-boundary dup lands in Unjustified,
// an allow matching nothing lands in Stale, and a within-nonprod dup shows up
// in ReportOnly without failing (i.e. without appearing in Unjustified).
func TestEvaluateJustifiedUnjustifiedStaleReportOnly(t *testing.T) {
	report, err := Evaluate(declaredTierConfig(), evalTestUnits(), evalTestNow)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	for _, v := range report.Unjustified {
		if v.Unit == "svc/api-a" && v.Key == "client_id" {
			t.Errorf("justified client_id dup should not be unjustified, got %+v", v)
		}
	}

	foundUnjustified := false
	for _, v := range report.Unjustified {
		if v.Unit == "svc/api-b" && v.Key == "account_id" {
			foundUnjustified = true
		}
	}
	if !foundUnjustified {
		t.Errorf("expected svc/api-b account_id dup in Unjustified, got %+v", report.Unjustified)
	}
	if len(report.Unjustified) != 1 {
		t.Errorf("Unjustified = %+v, want exactly 1 entry", report.Unjustified)
	}

	if len(report.Stale) != 1 || report.Stale[0].Key != "nonexistent_id" {
		t.Fatalf("Stale = %+v, want [nonexistent_id]", report.Stale)
	}

	foundReportOnly := false
	for _, v := range report.ReportOnly {
		if v.Unit == "svc/api-a" && v.Key == "app_id" {
			foundReportOnly = true
		}
	}
	if !foundReportOnly {
		t.Errorf("expected svc/api-a app_id dup in ReportOnly, got %+v", report.ReportOnly)
	}
	for _, v := range report.Unjustified {
		if v.Key == "app_id" {
			t.Errorf("report-only app_id dup must never appear in Unjustified, got %+v", v)
		}
	}
}

// TestEvaluateTierFromSourceInput verifies the Source.TierInput branch:
// tiers are resolved per-env from the flattened leaf across units instead of
// from declared Environments blocks.
func TestEvaluateTierFromSourceInput(t *testing.T) {
	cfg := &config.EnvUniquenessConfig{
		ProtectedTier: "prod",
		Source:        &config.SourceBlock{TierInput: "tier_class"},
	}
	units := []Unit{
		{
			ID:   "svc/api-a",
			Envs: []string{"dev", "prod"},
			Inputs: map[string]map[string]any{
				"dev":  {"tier_class": "nonprod", "client_id": "acme-shared-client"},
				"prod": {"tier_class": "prod", "client_id": "acme-shared-client"},
			},
		},
	}

	report, err := Evaluate(cfg, units, evalTestNow)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if len(report.Unjustified) != 1 || report.Unjustified[0].Key != "client_id" {
		t.Fatalf("Unjustified = %+v, want the unallowed client_id cross-boundary dup", report.Unjustified)
	}
}

// TestEvaluateErrorsOnUndeclaredEnv verifies that, with no Source.TierInput,
// an env present in the unit data but absent from cfg.Environments is a hard
// error (an undeclared environment can't be tier-classified at all).
func TestEvaluateErrorsOnUndeclaredEnv(t *testing.T) {
	cfg := &config.EnvUniquenessConfig{
		ProtectedTier: "prod",
		Environments: []config.EnvBlock{
			{Name: "prod", Tier: "prod"},
		},
	}
	units := []Unit{
		{
			ID:   "svc/api-a",
			Envs: []string{"dev", "prod"},
			Inputs: map[string]map[string]any{
				"dev":  {"client_id": "acme-shared-client"},
				"prod": {"client_id": "acme-shared-client"},
			},
		},
	}

	_, err := Evaluate(cfg, units, evalTestNow)
	if err == nil {
		t.Fatal("expected error for undeclared env \"dev\"")
	}
	if !strings.Contains(err.Error(), "dev") {
		t.Errorf("error = %v, want it to mention the undeclared env", err)
	}
}

// TestEvaluateStripsTierInputLeafBeforeDetection verifies parity with the
// Python prototype's load_bundles (which pops tier_class before flattening):
// when cfg.Source.TierInput is set, that leaf is removed from the per-env
// input maps before running the duplicate/env-token detectors, so it can
// never itself surface as a Violation — while detection of every other key
// proceeds unaffected. It also verifies the caller's units are never
// mutated: the tier leaf must still be present on the original maps after
// Evaluate returns.
func TestEvaluateStripsTierInputLeafBeforeDetection(t *testing.T) {
	cfg := &config.EnvUniquenessConfig{
		ProtectedTier: "prod",
		// "tier_id" (rather than e.g. "tier_class") is deliberately named to
		// end in "_id", so it is identifier-shaped by DefaultKeyPatterns
		// regardless of its value — letting it double as this test's "would
		// be flagged if not stripped" leaf while still carrying real,
		// distinct tier values.
		Source: &config.SourceBlock{TierInput: "tier_id"},
	}
	units := []Unit{
		{
			ID:   "svc/api-a",
			Envs: []string{"dev", "staging", "prod"},
			Inputs: map[string]map[string]any{
				// dev and staging share the identical (real) "nonprod" tier
				// value: if tier_id were not stripped before detection, this
				// pair would itself surface as a within-tier duplicate.
				"dev":     {"tier_id": "nonprod", "account_id": "acme-shared-account"},
				"staging": {"tier_id": "nonprod"},
				"prod":    {"tier_id": "prod", "account_id": "acme-shared-account"},
			},
		},
	}

	report, err := Evaluate(cfg, units, evalTestNow)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	for _, v := range append(append([]Violation{}, report.Unjustified...), report.ReportOnly...) {
		if v.Key == cfg.Source.TierInput {
			t.Errorf("tier_input leaf %q must be stripped before detection, got violation %+v", cfg.Source.TierInput, v)
		}
	}

	foundAccountID := false
	for _, v := range report.Unjustified {
		if v.Key == "account_id" {
			foundAccountID = true
		}
	}
	if !foundAccountID {
		t.Errorf("expected account_id cross-boundary dup still detected (unaffected by stripping), got %+v", report.Unjustified)
	}

	// Evaluate must not mutate the caller's units: the tier leaf should
	// still be present on the original input maps.
	if _, ok := units[0].Inputs["dev"]["tier_id"]; !ok {
		t.Error("caller's dev inputs must still contain tier_id after Evaluate (no mutation)")
	}
	if _, ok := units[0].Inputs["staging"]["tier_id"]; !ok {
		t.Error("caller's staging inputs must still contain tier_id after Evaluate (no mutation)")
	}
	if _, ok := units[0].Inputs["prod"]["tier_id"]; !ok {
		t.Error("caller's prod inputs must still contain tier_id after Evaluate (no mutation)")
	}
}

// TestEvaluateErrorsOnInvalidExtraKeyPattern verifies a malformed
// extra_key_patterns entry produces a plain error (and does not panic)
// rather than propagating a regexp.Compile panic.
func TestEvaluateErrorsOnInvalidExtraKeyPattern(t *testing.T) {
	cfg := &config.EnvUniquenessConfig{
		ProtectedTier: "prod",
		Environments: []config.EnvBlock{
			{Name: "prod", Tier: "prod"},
		},
		ExtraKeyPats: []string{"(unterminated["},
	}
	units := []Unit{
		{
			ID:     "svc/api-a",
			Envs:   []string{"prod"},
			Inputs: map[string]map[string]any{"prod": {"client_id": "acme-client"}},
		},
	}

	_, err := Evaluate(cfg, units, evalTestNow)
	if err == nil {
		t.Fatal("expected error for malformed extra_key_patterns entry")
	}
}

// TestEvaluateErrorsOnInconsistentTierInput verifies that, with
// Source.TierInput set, two units disagreeing about the same env's tier is a
// hard error rather than silently picking one.
func TestEvaluateErrorsOnInconsistentTierInput(t *testing.T) {
	cfg := &config.EnvUniquenessConfig{
		ProtectedTier: "prod",
		Source:        &config.SourceBlock{TierInput: "tier_class"},
	}
	units := []Unit{
		{
			ID:     "svc/api-a",
			Envs:   []string{"dev"},
			Inputs: map[string]map[string]any{"dev": {"tier_class": "nonprod"}},
		},
		{
			ID:     "svc/api-b",
			Envs:   []string{"dev"},
			Inputs: map[string]map[string]any{"dev": {"tier_class": "prod"}},
		},
	}

	_, err := Evaluate(cfg, units, evalTestNow)
	if err == nil {
		t.Fatal("expected error for inconsistent tier_class across units for env \"dev\"")
	}
}
