package uniqueness

import (
	"fmt"
	"sort"
	"strings"
)

// FindDuplicates detects, within a single Unit, dot-path keys whose leaf
// value is identical across two or more of the unit's environments — where
// that value is identifier-shaped (per Classifier.IsIdentifier) and the key
// itself is not env-scoped fan-out config (per Classifier.IsEnvScoped).
//
// Severity is tier-aware: for each equal-value group of ≥2 envs, the tier of
// an env is tierOf[env] if present, else protected (fail-closed — an env
// missing from tierOf is always treated as protected, never as an
// escape hatch). A group is a blocking SeverityViolation if its tiers
// include both protected and some non-protected tier; a group confined to a
// single tier (all protected, or all some other tier) is SeverityReportOnly.
//
// Keys are iterated in sorted order, and each Violation's Envs are sorted,
// for deterministic output. This ports the Python prototype's
// find_duplicate_violations plus its _tier fail-closed helper.
func FindDuplicates(u Unit, tierOf map[string]Tier, protected Tier, c Classifier) []Violation {
	allKeys := map[string]bool{}
	for _, flat := range u.Inputs {
		for k := range flat {
			allKeys[k] = true
		}
	}
	keys := make([]string, 0, len(allKeys))
	for k := range allKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var out []Violation
	for _, key := range keys {
		if c.IsEnvScoped(key) {
			continue
		}
		out = append(out, findDuplicatesForKey(u, key, tierOf, protected, c)...)
	}
	return out
}

// findDuplicatesForKey groups every env's value at key by equal value, and
// emits one Violation per group of ≥2 envs whose shared value is
// identifier-shaped.
func findDuplicatesForKey(u Unit, key string, tierOf map[string]Tier, protected Tier, c Classifier) []Violation {
	type group struct {
		value any
		envs  []string
	}
	groups := map[string]*group{}
	for env, flat := range u.Inputs {
		val, ok := flat[key]
		if !ok {
			continue
		}
		gk := groupKey(val)
		g, exists := groups[gk]
		if !exists {
			g = &group{value: val}
			groups[gk] = g
		}
		g.envs = append(g.envs, env)
	}

	gks := make([]string, 0, len(groups))
	for gk := range groups {
		gks = append(gks, gk)
	}
	sort.Strings(gks)

	var out []Violation
	for _, gk := range gks {
		g := groups[gk]
		if len(g.envs) < 2 {
			continue
		}
		if !c.IsIdentifier(key, g.value) {
			continue
		}

		envs := append([]string(nil), g.envs...)
		sort.Strings(envs)

		hasProtected, hasNonProtected := false, false
		for _, e := range envs {
			t, ok := tierOf[e]
			if !ok {
				t = protected // fail-closed: unknown tier counts as protected
			}
			if t == protected {
				hasProtected = true
			} else {
				hasNonProtected = true
			}
		}
		severity := SeverityReportOnly
		if hasProtected && hasNonProtected {
			severity = SeverityViolation
		}

		out = append(out, Violation{
			Unit:     u.ID,
			Key:      key,
			Value:    g.value,
			Envs:     envs,
			Kind:     KindDuplicate,
			Severity: severity,
		})
	}
	return out
}

// groupKey builds a stable, hashable grouping key for a flattened leaf
// value: a scalar (including bool and nil) stringifies under a "scalar\x1f"
// prefix, while a List sentinel joins its elements under a distinct
// "list\x1f" prefix — so a list can never collide with a same-looking
// scalar (e.g. a single-element list vs. its bare element), regardless of
// what characters appear inside the elements themselves.
func groupKey(value any) string {
	if lst, ok := value.(List); ok {
		return "list\x1f" + strings.Join(lst.Elems, "\x1f")
	}
	return "scalar\x1f" + fmt.Sprint(value)
}
