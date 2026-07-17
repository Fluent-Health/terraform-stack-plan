package uniqueness

import "testing"

// newDefaultClassifier builds a Classifier wired to the generic built-in
// patterns only (no derived env tokens/scoped segments), for tests that
// exercise pure key/value shape classification.
func newDefaultClassifier() Classifier {
	return Classifier{
		ValPats:     DefaultValuePatterns(),
		KeyPats:     DefaultKeyPatterns(),
		QtySuffixes: DefaultQuantitySuffixes(),
	}
}

// TestIsIdentifierQuantitySuffix verifies a leaf ending in a known quantity
// suffix (duration/count/size) is never classified as an identifier, even
// though the value itself is a long number that would otherwise match the
// long-opaque-numeric value pattern.
func TestIsIdentifierQuantitySuffix(t *testing.T) {
	c := newDefaultClassifier()
	if c.IsIdentifier("jwt_expiration_ms", 86400000) {
		t.Fatal("jwt_expiration_ms=86400000 should not be an identifier (quantity suffix)")
	}
}

// TestIsIdentifierKeyPattern verifies a leaf matching a known identifier key
// pattern (here, "_id$") classifies its value as an identifier regardless of
// value shape.
func TestIsIdentifierKeyPattern(t *testing.T) {
	c := newDefaultClassifier()
	if !c.IsIdentifier("system_user_id", "615840001234") {
		t.Fatal("system_user_id=<opaque> should be an identifier (key pattern)")
	}
}

// TestIsIdentifierEmptyAndBoolAreNever verifies empty strings and booleans
// are never identifiers, no matter the key.
func TestIsIdentifierEmptyAndBoolAreNever(t *testing.T) {
	c := newDefaultClassifier()
	if c.IsIdentifier("client_id", "") {
		t.Fatal(`client_id="" should not be an identifier (empty value)`)
	}
	if c.IsIdentifier("client_id", false) {
		t.Fatal("client_id=false should not be an identifier (bool value)")
	}
}

// TestIsIdentifierListAnyElement verifies a List value classifies as an
// identifier if ANY element does — here via the generic dotted-hostname
// value pattern, not a specific domain.
func TestIsIdentifierListAnyElement(t *testing.T) {
	c := newDefaultClassifier()
	v := List{Elems: []string{"acme-dev.example.com"}}
	if !c.IsIdentifier("allowed_hosts", v) {
		t.Fatal("list containing a dotted hostname should be an identifier")
	}
}

// TestIsIdentifierEnvTokenFolded verifies a value equal to a derived env
// token classifies as an identifier even though it matches no built-in
// pattern, per the brief: env tokens are folded into value matching.
func TestIsIdentifierEnvTokenFolded(t *testing.T) {
	c := newDefaultClassifier()
	c.EnvTokens = map[string][]string{"dev": {"acme-dev"}}
	if !c.IsIdentifier("some_setting", "acme-dev") {
		t.Fatal("value equal to a derived env token should be an identifier")
	}
	if c.IsIdentifier("some_setting", "unrelated-value") {
		t.Fatal("value unrelated to any env token should not be an identifier")
	}
}

// TestIsEnvScoped verifies IsEnvScoped matches when any lowercased dot
// segment of the key is in ScopedSegs, and does not match on a key that
// merely contains an env name as a substring of a segment (not as its own
// segment).
func TestIsEnvScoped(t *testing.T) {
	c := Classifier{ScopedSegs: map[string]bool{"dev": true}}
	if !c.IsEnvScoped("environments.dev.project") {
		t.Fatal(`"environments.dev.project" should be env-scoped (segment "dev")`)
	}
	if c.IsEnvScoped("enable_dev_mode") {
		t.Fatal(`"enable_dev_mode" should not be env-scoped (no exact "dev" segment)`)
	}
}

// TestDeriveTokensAndSegs verifies per-env token derivation from a
// "{env}"-templated project name, plus the bare env name, both fold into
// ScopedSegs alongside the env names themselves.
func TestDeriveTokensAndSegs(t *testing.T) {
	tokens, segs := DeriveTokensAndSegs([]string{"dev", "prod"}, "acme-{env}", nil, nil)

	found := false
	for _, tok := range tokens["dev"] {
		if tok == "acme-dev" {
			found = true
		}
	}
	if !found {
		t.Fatalf(`tokens["dev"] = %v, want to contain "acme-dev"`, tokens["dev"])
	}
	if !segs["dev"] {
		t.Fatal(`segs["dev"] should be true`)
	}
	if !segs["acme-dev"] {
		t.Fatal(`segs["acme-dev"] should be true`)
	}
}

// TestBoundaryTokenPattern verifies the shared boundary-token helper matches
// a token only as a whole token: flanked by start/end of string or a
// non-alphanumeric rune, not as a substring of a longer alphanumeric run.
func TestBoundaryTokenPattern(t *testing.T) {
	p := BoundaryTokenPattern("acme-dev")
	if !p.MatchString("acme-dev.example.com") {
		t.Fatal(`BoundaryTokenPattern("acme-dev") should match in "acme-dev.example.com"`)
	}
	if p.MatchString("acme-development") {
		t.Fatal(`BoundaryTokenPattern("acme-dev") should NOT match in "acme-development"`)
	}
}

// TestNewClassifierPrecompilesTokenPatterns verifies a Classifier built via
// NewClassifier (precompiled tokenPats cache) still classifies a
// token-valued leaf as an identifier, and correctly rejects an unrelated
// value — i.e. the cached path produces the same result as the uncached one.
func TestNewClassifierPrecompilesTokenPatterns(t *testing.T) {
	c := NewClassifier(
		DefaultValuePatterns(), DefaultKeyPatterns(), DefaultQuantitySuffixes(),
		map[string][]string{"dev": {"acme-dev"}}, nil,
	)
	if !c.IsIdentifier("some_setting", "acme-dev") {
		t.Fatal("NewClassifier: value equal to a derived env token should be an identifier")
	}
	if c.IsIdentifier("some_setting", "unrelated-value") {
		t.Fatal("NewClassifier: value unrelated to any env token should not be an identifier")
	}
}

// TestClassifierStructLiteralTokenFoldingUncached verifies a Classifier
// built by plain struct literal (tokenPats left at its nil zero value, so
// matchesEnvToken falls back to compiling EnvTokens on the fly) still
// classifies a token-valued leaf correctly — proving the lazy/uncached path
// is correct, not just the precompiled NewClassifier path.
func TestClassifierStructLiteralTokenFoldingUncached(t *testing.T) {
	c := Classifier{
		ValPats:     DefaultValuePatterns(),
		KeyPats:     DefaultKeyPatterns(),
		QtySuffixes: DefaultQuantitySuffixes(),
		EnvTokens:   map[string][]string{"dev": {"acme-dev"}},
	}
	if !c.IsIdentifier("some_setting", "acme-dev") {
		t.Fatal("struct-literal Classifier: value equal to a derived env token should be an identifier")
	}
	if c.IsIdentifier("some_setting", "unrelated-value") {
		t.Fatal("struct-literal Classifier: value unrelated to any env token should not be an identifier")
	}
}

// TestIsIdentifierIntegralFloat64LongNumeric verifies a huge integral
// float64 (as YAML decodes a bare integer literal beyond uint64 range)
// stringifies without scientific notation for value-pattern matching, so it
// still matches the long-opaque-numeric value pattern under a non-identifier
// key — mirroring the Python prototype's arbitrary-precision int behavior.
func TestIsIdentifierIntegralFloat64LongNumeric(t *testing.T) {
	c := newDefaultClassifier()
	if !c.IsIdentifier("blob", float64(1234567890123456789012345)) {
		t.Fatal("blob=<huge integral float64> should be an identifier (long-numeric value shape)")
	}
}

// TestIsIdentifierNonIntegralFloat64NotFlagged verifies a non-integral
// float64 under a non-identifier key is not flagged merely because it's a
// float — stringifyValue's exponent-avoidance only applies to whole-number
// floats, and "1.5" matches no built-in value pattern.
func TestIsIdentifierNonIntegralFloat64NotFlagged(t *testing.T) {
	c := newDefaultClassifier()
	if c.IsIdentifier("blob", 1.5) {
		t.Fatal("blob=1.5 should not be an identifier")
	}
}

// TestDeriveTokensAndSegsExtras verifies extraTokens (per-env) and extraSegs
// (bare) merge into the result alongside the derived template/env tokens.
func TestDeriveTokensAndSegsExtras(t *testing.T) {
	tokens, segs := DeriveTokensAndSegs(
		[]string{"dev"}, "",
		map[string][]string{"dev": {"acme-dev-svc"}},
		[]string{"shared-infra"},
	)

	if len(tokens["dev"]) != 2 {
		t.Fatalf(`tokens["dev"] = %v, want ["dev", "acme-dev-svc"]`, tokens["dev"])
	}
	if !segs["acme-dev-svc"] {
		t.Fatal(`segs["acme-dev-svc"] should be true (from extraTokens)`)
	}
	if !segs["shared-infra"] {
		t.Fatal(`segs["shared-infra"] should be true (from extraSegs)`)
	}
}
