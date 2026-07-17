package uniqueness

import (
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/config"
)

// Evaluate runs the full per-environment-value-duplication lint for cfg over
// units as of now: it resolves each discovered env's Tier (either from
// declared cfg.Environments blocks, or — when cfg.Source.TierInput is set —
// from that flattened input leaf, read consistently across all units),
// derives the repo's env tokens/scoped segments, builds a Classifier,
// collects every duplicate and env-token Violation across all units, and
// partitions them against cfg.Allows as of now. Mirrors the Python
// prototype's evaluate.
func Evaluate(cfg *config.EnvUniquenessConfig, units []Unit, now time.Time) (Report, error) {
	envNames := discoverEnvNames(units)

	tierOf, err := resolveTiers(cfg, units, envNames)
	if err != nil {
		return Report{}, err
	}

	tokens, segs := DeriveTokensAndSegs(envNames, cfg.ProjectTemplate, cfg.ExtraEnvTokens, cfg.ExtraScopedSegs)

	keyPats := append([]*regexp.Regexp(nil), DefaultKeyPatterns()...)
	for _, p := range cfg.ExtraKeyPats {
		re, err := regexp.Compile(p)
		if err != nil {
			return Report{}, fmt.Errorf("env_uniqueness: invalid extra_key_patterns entry %q: %w", p, err)
		}
		keyPats = append(keyPats, re)
	}

	classifier := NewClassifier(DefaultValuePatterns(), keyPats, DefaultQuantitySuffixes(), tokens, segs)

	protected := Tier(cfg.ProtectedTier)
	if protected == "" {
		protected = "prod"
	}

	// When tiers are resolved from data (Source.TierInput), that leaf must
	// not itself be fed to the detectors — mirrors the Python prototype's
	// load_bundles, which pops tier_class before flattening. Build filtered
	// copies rather than mutating the caller's units/maps.
	detectUnits := units
	if cfg.Source != nil && cfg.Source.TierInput != "" {
		detectUnits = stripTierInputLeaf(units, cfg.Source.TierInput)
	}

	var all []Violation
	for _, u := range detectUnits {
		all = append(all, FindDuplicates(u, tierOf, protected, classifier)...)
		all = append(all, FindEnvTokens(u, classifier)...)
	}

	allowRules := make([]AllowRule, 0, len(cfg.Allows))
	for _, a := range cfg.Allows {
		allowRules = append(allowRules, AllowRule{
			Unit:    a.Unit,
			Key:     a.Key,
			Envs:    a.Envs,
			Reason:  a.Reason,
			Expires: a.Expires,
		})
	}

	unjustified, stale := Partition(all, allowRules, now)

	var reportOnly []Violation
	for _, v := range all {
		if v.Severity == SeverityReportOnly {
			reportOnly = append(reportOnly, v)
		}
	}

	return Report{Unjustified: unjustified, Stale: stale, ReportOnly: reportOnly}, nil
}

// discoverEnvNames collects the sorted, de-duplicated set of env names
// appearing as Inputs keys across all units.
func discoverEnvNames(units []Unit) []string {
	set := map[string]bool{}
	for _, u := range units {
		for env := range u.Inputs {
			set[env] = true
		}
	}
	names := make([]string, 0, len(set))
	for env := range set {
		names = append(names, env)
	}
	sort.Strings(names)
	return names
}

// resolveTiers builds the env->Tier map Evaluate needs. When
// cfg.Source.TierInput is set, each env's tier is read from that flattened
// input leaf, checked for consistency across every unit that has data for
// that env; an env with no unit resolving a tier for it, or with
// disagreeing values across units, is an error. Otherwise, every discovered
// env must be declared in cfg.Environments; an undeclared env is an error.
func resolveTiers(cfg *config.EnvUniquenessConfig, units []Unit, envNames []string) (map[string]Tier, error) {
	if cfg.Source != nil && cfg.Source.TierInput != "" {
		return resolveTiersFromData(cfg.Source.TierInput, units, envNames)
	}
	return resolveTiersFromDeclared(cfg.Environments, envNames)
}

// resolveTiersFromData reads each env's tier from tierInput's flattened leaf
// across all units, requiring every unit that has data for that env to agree.
func resolveTiersFromData(tierInput string, units []Unit, envNames []string) (map[string]Tier, error) {
	tierOf := map[string]Tier{}
	for _, env := range envNames {
		var resolved Tier
		found := false
		for _, u := range units {
			flat, ok := u.Inputs[env]
			if !ok {
				continue
			}
			raw, ok := flat[tierInput]
			if !ok {
				continue
			}
			t := Tier(fmt.Sprint(raw))
			if !found {
				resolved = t
				found = true
				continue
			}
			if resolved != t {
				return nil, fmt.Errorf("env_uniqueness: inconsistent tier for env %q: %q vs %q (source.tier_input=%q)", env, resolved, t, tierInput)
			}
		}
		if !found {
			return nil, fmt.Errorf("env_uniqueness: no resolvable tier for env %q (source.tier_input=%q)", env, tierInput)
		}
		tierOf[env] = resolved
	}
	return tierOf, nil
}

// stripTierInputLeaf returns a copy of units with the tierInput leaf removed
// from every per-env input map that has it — so the tier-discriminator leaf
// itself (e.g. tier_class) is never seen by the duplicate/env-token
// detectors. It never mutates the caller's units or their Inputs maps: any
// env map containing tierInput is shallow-copied minus that one key; any env
// map that never had it is reused as-is (nothing to remove, nothing to
// copy).
func stripTierInputLeaf(units []Unit, tierInput string) []Unit {
	out := make([]Unit, len(units))
	for i, u := range units {
		newInputs := make(map[string]map[string]any, len(u.Inputs))
		for env, flat := range u.Inputs {
			if _, ok := flat[tierInput]; !ok {
				newInputs[env] = flat
				continue
			}
			cp := make(map[string]any, len(flat)-1)
			for k, v := range flat {
				if k == tierInput {
					continue
				}
				cp[k] = v
			}
			newInputs[env] = cp
		}
		out[i] = Unit{ID: u.ID, Envs: u.Envs, Inputs: newInputs}
	}
	return out
}

// resolveTiersFromDeclared builds the env->Tier map from cfg.Environments,
// erroring if any discovered env is not declared.
func resolveTiersFromDeclared(declared []config.EnvBlock, envNames []string) (map[string]Tier, error) {
	byName := make(map[string]Tier, len(declared))
	for _, e := range declared {
		byName[e.Name] = Tier(e.Tier)
	}
	tierOf := make(map[string]Tier, len(envNames))
	for _, env := range envNames {
		t, ok := byName[env]
		if !ok {
			return nil, fmt.Errorf("env_uniqueness: environment %q found in data but not declared in any environment {} block", env)
		}
		tierOf[env] = t
	}
	return tierOf, nil
}
