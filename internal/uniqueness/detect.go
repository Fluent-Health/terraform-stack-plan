package uniqueness

import (
	"fmt"
	"regexp"
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

// FindEnvTokens detects, within a single Unit, a leaf value in one env that
// embeds another env's identifying token — e.g. a dev URL accidentally
// hardcoded into prod's config. For each env E (in sorted order) and each of
// its leaves (sorted by key, skipping any key for which
// Classifier.IsEnvScoped is true), the leaf's value is turned into candidate
// strings: a non-empty scalar string yields itself; a List yields its
// non-empty elements; anything else (bool, number, empty string, nil) is
// skipped entirely — it can never leak a token. If any candidate contains,
// as a whole token (see BoundaryTokenPattern), any token belonging to a
// *foreign* env (any env F != E in c.EnvTokens), exactly one Violation is
// emitted for that leaf and scanning moves on to the next leaf — at most one
// violation per leaf, regardless of how many foreign tokens or candidates
// matched.
//
// This ports the Python prototype's find_env_token_violations plus its
// _token_in boundary-safe helper (here, BoundaryTokenPattern).
func FindEnvTokens(u Unit, c Classifier) []Violation {
	tokenPats := compileTokenPatternsByToken(c.EnvTokens)

	envs := make([]string, 0, len(u.Inputs))
	for env := range u.Inputs {
		envs = append(envs, env)
	}
	sort.Strings(envs)

	var out []Violation
	for _, env := range envs {
		flat := u.Inputs[env]
		foreign := foreignTokens(c.EnvTokens, env)

		keys := make([]string, 0, len(flat))
		for k := range flat {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, key := range keys {
			if c.IsEnvScoped(key) {
				continue
			}
			val := flat[key]
			candidates := envTokenCandidates(val)
			if len(candidates) == 0 {
				continue
			}
			if anyForeignTokenLeaks(foreign, tokenPats, candidates) {
				out = append(out, Violation{
					Unit:     u.ID,
					Key:      key,
					Value:    val,
					Envs:     []string{env},
					Kind:     KindEnvToken,
					Severity: SeverityViolation,
				})
			}
		}
	}
	return out
}

// envTokenCandidates builds the candidate strings to search for a leaked
// foreign token in a flattened leaf value: a non-empty scalar string yields
// itself; a List yields its non-empty elements; anything else (bool,
// number, empty string, nil) yields nothing, since none of those can embed a
// token.
func envTokenCandidates(value any) []string {
	switch v := value.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case List:
		out := make([]string, 0, len(v.Elems))
		for _, e := range v.Elems {
			if e != "" {
				out = append(out, e)
			}
		}
		return out
	default:
		return nil
	}
}

// foreignTokens collects every token belonging to any env other than env
// across c.EnvTokens (order doesn't matter — anyForeignTokenLeaks only needs
// the set).
func foreignTokens(envTokens map[string][]string, env string) []string {
	var out []string
	for f, toks := range envTokens {
		if f == env {
			continue
		}
		out = append(out, toks...)
	}
	return out
}

// anyForeignTokenLeaks reports whether any candidate string contains any of
// the foreign tokens as a whole token, using tokenPats (precompiled once per
// FindEnvTokens call by compileTokenPatternsByToken) for the boundary-safe
// match.
func anyForeignTokenLeaks(foreign []string, tokenPats map[string]*regexp.Regexp, candidates []string) bool {
	for _, tok := range foreign {
		pat, ok := tokenPats[tok]
		if !ok {
			continue
		}
		for _, cand := range candidates {
			if pat.MatchString(cand) {
				return true
			}
		}
	}
	return false
}

// compileTokenPatternsByToken compiles one BoundaryTokenPattern per unique,
// non-empty token across every env in envTokens, keyed by the token text —
// so FindEnvTokens compiles each distinct token's pattern exactly once per
// call (not once per candidate string or per leaf), reusing the shared
// BoundaryTokenPattern helper rather than reimplementing boundary matching.
func compileTokenPatternsByToken(envTokens map[string][]string) map[string]*regexp.Regexp {
	out := make(map[string]*regexp.Regexp)
	for _, toks := range envTokens {
		for _, tok := range toks {
			if tok == "" {
				continue
			}
			if _, ok := out[tok]; ok {
				continue
			}
			out[tok] = BoundaryTokenPattern(tok)
		}
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
