package config

import (
	"strings"
	"testing"
)

// TestEnvUniquenessBlock parses an env_uniqueness block with an explicit
// protected_tier, environment labels, a source block that only overrides
// tier_input, extra token/segment/pattern lists, and an allow block with a
// reason. It asserts the decoded fields and that the omitted source fields
// (glob/environments_path/inputs_path) fall back to the Catalyst defaults.
func TestEnvUniquenessBlock(t *testing.T) {
	cfg, err := loadHCL(t, `
env_uniqueness {
  protected_tier          = "prod"
  project_token_template  = "acme-{env}"

  environment "dev"  { tier = "nonprod" }
  environment "prod" { tier = "prod" }

  source {
    tier_input = "tier_class"
  }

  extra_env_tokens = {
    dev = ["development"]
  }
  extra_scoped_segments = ["svc/api"]
  extra_key_patterns    = ["*_url"]

  allow {
    unit   = "svc/api"
    key    = "app-dev"
    envs   = ["dev", "test"]
    reason = "shared sandbox project by design"
  }
}
`)
	if err != nil {
		t.Fatalf("expected env_uniqueness block to parse, got error: %v", err)
	}
	if cfg.EnvUniqueness == nil {
		t.Fatal("expected EnvUniqueness to be parsed")
	}
	eu := cfg.EnvUniqueness

	if eu.ProtectedTier != "prod" {
		t.Errorf("ProtectedTier = %q, want prod", eu.ProtectedTier)
	}
	if eu.ProjectTemplate != "acme-{env}" {
		t.Errorf("ProjectTemplate = %q, want acme-{env}", eu.ProjectTemplate)
	}
	if len(eu.Environments) != 2 || eu.Environments[0].Name != "dev" || eu.Environments[0].Tier != "nonprod" {
		t.Fatalf("Environments = %+v", eu.Environments)
	}
	if eu.Environments[1].Name != "prod" || eu.Environments[1].Tier != "prod" {
		t.Fatalf("Environments[1] = %+v", eu.Environments[1])
	}

	if eu.Source == nil {
		t.Fatal("expected Source to be populated")
	}
	if eu.Source.TierInput != "tier_class" {
		t.Errorf("Source.TierInput = %q, want tier_class", eu.Source.TierInput)
	}
	// defaults applied for the fields the fixture didn't set
	if eu.Source.Glob != "components/**/instances/*.tm.yml" {
		t.Errorf("Source.Glob default = %q", eu.Source.Glob)
	}
	if eu.Source.EnvironmentsPath != "environments" {
		t.Errorf("Source.EnvironmentsPath default = %q", eu.Source.EnvironmentsPath)
	}
	if eu.Source.InputsPath != "inputs" {
		t.Errorf("Source.InputsPath default = %q", eu.Source.InputsPath)
	}

	if len(eu.ExtraEnvTokens["dev"]) != 1 || eu.ExtraEnvTokens["dev"][0] != "development" {
		t.Errorf("ExtraEnvTokens = %+v", eu.ExtraEnvTokens)
	}
	if len(eu.ExtraScopedSegs) != 1 || eu.ExtraScopedSegs[0] != "svc/api" {
		t.Errorf("ExtraScopedSegs = %+v", eu.ExtraScopedSegs)
	}
	if len(eu.ExtraKeyPats) != 1 || eu.ExtraKeyPats[0] != "*_url" {
		t.Errorf("ExtraKeyPats = %+v", eu.ExtraKeyPats)
	}

	if len(eu.Allows) != 1 {
		t.Fatalf("Allows = %+v, want 1", eu.Allows)
	}
	a := eu.Allows[0]
	if a.Unit != "svc/api" || a.Key != "app-dev" || a.Reason != "shared sandbox project by design" {
		t.Errorf("Allows[0] = %+v", a)
	}
	if len(a.Envs) != 2 || a.Envs[0] != "dev" || a.Envs[1] != "test" {
		t.Errorf("Allows[0].Envs = %+v", a.Envs)
	}
}

// TestEnvUniquenessDefaultsNoSource covers the fully-empty block: protected_tier
// defaults to "prod" and an absent source block still gets Catalyst defaults.
func TestEnvUniquenessDefaultsNoSource(t *testing.T) {
	cfg, err := loadHCL(t, `
env_uniqueness {}
`)
	if err != nil {
		t.Fatalf("expected empty env_uniqueness block to parse, got error: %v", err)
	}
	eu := cfg.EnvUniqueness
	if eu == nil {
		t.Fatal("expected EnvUniqueness to be parsed")
	}
	if eu.ProtectedTier != "prod" {
		t.Errorf("ProtectedTier default = %q, want prod", eu.ProtectedTier)
	}
	if eu.Source == nil {
		t.Fatal("expected Source defaults to be synthesized when block absent")
	}
	if eu.Source.Glob != "components/**/instances/*.tm.yml" {
		t.Errorf("Source.Glob default = %q", eu.Source.Glob)
	}
	if eu.Source.EnvironmentsPath != "environments" {
		t.Errorf("Source.EnvironmentsPath default = %q", eu.Source.EnvironmentsPath)
	}
	if eu.Source.InputsPath != "inputs" {
		t.Errorf("Source.InputsPath default = %q", eu.Source.InputsPath)
	}
}

// TestEnvUniquenessAllowRequiresReason ensures an allow block with an empty
// (or whitespace-only) reason fails Load, since unreviewed exceptions
// defeat the point of the lint.
func TestEnvUniquenessAllowRequiresReason(t *testing.T) {
	_, err := loadHCL(t, `
env_uniqueness {
  allow {
    unit   = "svc/api"
    key    = "app-dev"
    envs   = ["dev"]
    reason = "   "
  }
}
`)
	if err == nil {
		t.Fatal("expected error for allow block with blank reason")
	}
	if !strings.Contains(err.Error(), "reason") {
		t.Fatalf("error = %v, want it to mention reason", err)
	}
}
