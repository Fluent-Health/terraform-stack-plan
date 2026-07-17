package uniqueness

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// DefaultValuePatterns returns the built-in, generic value-shape patterns
// that mark a leaf as identifier-like regardless of its key name: a UUID, an
// http(s) URL, a generic dotted hostname with a TLD (NOT any specific
// domain), and a long opaque numeric string (e.g. a phone/account/system
// id). Deliberately does NOT match semver, bare enums, or booleans.
func DefaultValuePatterns() []*regexp.Regexp {
	return compileAll(
		`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`, // uuid
		`^https?://`, // URL
		`(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}`, // generic dotted hostname + TLD
		`^\d{6,}$`, // long opaque numeric id
	)
}

// DefaultKeyPatterns returns the built-in, generic key-name patterns: a leaf
// whose last dot-segment matches one of these is identifier-like by name
// alone (value-shape still gates via QtySuffixes/empty/bool checks in
// IsIdentifier).
func DefaultKeyPatterns() []*regexp.Regexp {
	return compileAll(
		`_id$`,
		`_uuid$`,
		`client_id$`,
		`project_id$`,
		`domain_id$`,
		`phone_number_id$`,
		`account_id$`,
		`app_id$`,
		`system_user_id$`,
		`org_id$`,
	)
}

// DefaultQuantitySuffixes returns the built-in leaf-key suffixes that denote
// quantities, durations, or counts — never identifiers even when the value
// is a long number (e.g. kills jwt_expiration_ms=86400000 false positives).
func DefaultQuantitySuffixes() []string {
	return []string{
		"_ms",
		"_seconds",
		"_sec",
		"_days",
		"_count",
		"_size",
		"_size_gb",
		"_bytes",
		"_port",
		"_replicas",
		"_percent",
		"_validity",
		"_validity_seconds",
		"_expiration",
		"_timeout",
		"_lead_days",
		"_max_age_days",
	}
}

// compileAll compiles each pattern in order, panicking on an invalid regexp
// (all inputs are built-in constants, so a failure here is a programmer
// error, not a runtime condition).
func compileAll(patterns ...string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(patterns))
	for i, p := range patterns {
		out[i] = regexp.MustCompile(p)
	}
	return out
}

// Classifier decides whether a flattened (key, value) leaf is
// identifier-shaped and whether a key is env-scoped fan-out config that
// should be skipped entirely. ValPats/KeyPats/QtySuffixes are typically the
// generic Default* built-ins; EnvTokens/ScopedSegs are the per-repo derived
// tokens/segments from DeriveTokensAndSegs.
//
// tokenPats caches the compiled EnvTokens boundary patterns (see
// BoundaryTokenPattern). It is unexported and left zero (nil) by a plain
// struct literal — IsIdentifier still works in that case, falling back to
// compiling EnvTokens on the fly, just without the cache. Use NewClassifier
// to get the patterns precompiled once instead of on every IsIdentifier call.
type Classifier struct {
	ValPats     []*regexp.Regexp
	KeyPats     []*regexp.Regexp
	QtySuffixes []string
	EnvTokens   map[string][]string
	ScopedSegs  map[string]bool

	tokenPats []*regexp.Regexp
}

// NewClassifier builds a Classifier with its EnvTokens boundary patterns
// precompiled once (see BoundaryTokenPattern), instead of recompiling them on
// every IsIdentifier call — IsIdentifier is called once per flattened leaf
// per unit, so per-call recompilation over all EnvTokens is a real, easily
// avoidable cost. Prefer this over a bare struct literal whenever the
// Classifier will be reused across many IsIdentifier calls (e.g. by a
// detector, as in Task 5).
func NewClassifier(valPats, keyPats []*regexp.Regexp, qtySuffixes []string, envTokens map[string][]string, scopedSegs map[string]bool) Classifier {
	return Classifier{
		ValPats:     valPats,
		KeyPats:     keyPats,
		QtySuffixes: qtySuffixes,
		EnvTokens:   envTokens,
		ScopedSegs:  scopedSegs,
		tokenPats:   compileTokenPatterns(envTokens),
	}
}

// IsIdentifier ports the Python prototype's is_identifier: bool, nil, and
// empty-string values are never identifiers. A List value is an identifier
// iff any element is (checked recursively under the same key). Otherwise,
// if the key's last dot-segment ends with a quantity suffix, it is never an
// identifier; else it is an identifier if that leaf matches a KeyPat, or the
// stringified value matches a ValPat, or a derived env token (from
// EnvTokens, boundary-safe) appears in the stringified value.
func (c Classifier) IsIdentifier(key string, value any) bool {
	switch v := value.(type) {
	case bool:
		return false
	case nil:
		return false
	case List:
		for _, e := range v.Elems {
			if c.IsIdentifier(key, e) {
				return true
			}
		}
		return false
	}

	s := stringifyValue(value)
	if s == "" {
		return false
	}

	leaf := lastSegment(key)
	for _, suf := range c.QtySuffixes {
		if strings.HasSuffix(leaf, suf) {
			return false
		}
	}

	for _, p := range c.KeyPats {
		if p.MatchString(leaf) {
			return true
		}
	}
	for _, p := range c.ValPats {
		if p.MatchString(s) {
			return true
		}
	}
	return c.matchesEnvToken(s)
}

// IsEnvScoped reports whether any lowercased dot-segment of key is a known
// env-scoped segment (an env name, a derived env token, or a repo-configured
// extra scoped segment) — i.e. the key is fan-out config keyed by
// environment, not a per-instance value the duplication/token detectors
// should look at.
func (c Classifier) IsEnvScoped(key string) bool {
	for _, seg := range strings.Split(key, ".") {
		if c.ScopedSegs[strings.ToLower(seg)] {
			return true
		}
	}
	return false
}

// BoundaryTokenPattern returns a compiled RE2 regexp matching tok as a whole
// token: not flanked by an alphanumeric character. E.g. the token "acme-dev"
// matches inside "acme-dev.example.com" but not inside "acme-development".
// RE2 has no lookaround, so this consumes the boundary character (start/end
// of string or a non-alphanumeric rune) rather than asserting it via
// zero-width lookaround, mirroring the Python prototype's _token_in helper.
// Shared by IsIdentifier's env-token folding and (per Task 5) the env-token
// leak detector, so both use identical boundary semantics.
func BoundaryTokenPattern(tok string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[^0-9A-Za-z])` + regexp.QuoteMeta(tok) + `(?:$|[^0-9A-Za-z])`)
}

// compileTokenPatterns compiles a BoundaryTokenPattern for every non-empty
// token across all envs in envTokens. Always returns a non-nil slice (even
// when envTokens is empty), so callers can tell "precompiled, no tokens"
// (non-nil, empty) apart from "never precompiled" (nil) — see
// Classifier.tokenPats.
func compileTokenPatterns(envTokens map[string][]string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0)
	for _, toks := range envTokens {
		for _, tok := range toks {
			if tok == "" {
				continue
			}
			out = append(out, BoundaryTokenPattern(tok))
		}
	}
	return out
}

// matchesEnvToken reports whether s contains any derived env token as a
// whole token (boundary-safe). It uses the precompiled c.tokenPats cache
// when available (built by NewClassifier); otherwise — a plain Classifier
// struct literal, whose tokenPats field is left nil — it falls back to
// compiling c.EnvTokens on the fly, so a struct-literal Classifier still
// classifies correctly, just without the cache.
func (c Classifier) matchesEnvToken(s string) bool {
	pats := c.tokenPats
	if pats == nil {
		pats = compileTokenPatterns(c.EnvTokens)
	}
	for _, p := range pats {
		if p.MatchString(s) {
			return true
		}
	}
	return false
}

// stringifyValue stringifies a leaf value for value-shape matching. A YAML
// bare integer beyond uint64 range decodes as a float64 (rather than the
// Python prototype's arbitrary-precision int); fmt.Sprint on a large
// integral float64 renders scientific notation (e.g. "1.2345678901234568e+24"),
// which would never match the long-opaque-numeric value pattern — a silent
// fail-open vs. the Python spec. So a float64 that is finite and has no
// fractional part is formatted without an exponent instead
// (strconv.FormatFloat with 'f'); every other value (including non-integral
// floats, Inf, and NaN) stringifies via fmt.Sprint, unchanged.
func stringifyValue(value any) string {
	if f, ok := value.(float64); ok && !math.IsInf(f, 0) && !math.IsNaN(f) && f == math.Trunc(f) {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return fmt.Sprint(value)
}

// lastSegment returns the final dot-separated segment of a flattened key
// ("a.b.c" -> "c"; "c" -> "c").
func lastSegment(key string) string {
	if i := strings.LastIndexByte(key, '.'); i >= 0 {
		return key[i+1:]
	}
	return key
}

// DeriveTokensAndSegs computes, per environment, the set of tokens that
// identify it in a config value (the {env}-templated project name if
// template is non-empty, plus the bare env name, plus any extraTokens for
// that env), and the full set of env-scoped segment names (lowercased env
// names, lowercased tokens, and lowercased extraSegs) used by IsEnvScoped.
func DeriveTokensAndSegs(envs []string, template string, extraTokens map[string][]string, extraSegs []string) (map[string][]string, map[string]bool) {
	tokens := map[string][]string{}
	segs := map[string]bool{}

	for _, env := range envs {
		var toks []string
		if template != "" {
			toks = append(toks, strings.ReplaceAll(template, "{env}", env))
		}
		toks = append(toks, env)
		tokens[env] = toks
		segs[strings.ToLower(env)] = true
	}

	for env, extras := range extraTokens {
		tokens[env] = append(tokens[env], extras...)
	}

	for _, toks := range tokens {
		for _, tok := range toks {
			segs[strings.ToLower(tok)] = true
		}
	}
	for _, seg := range extraSegs {
		segs[strings.ToLower(seg)] = true
	}

	return tokens, segs
}
