// Package uniqueness implements the per-environment value-duplication engine:
// flattening per-env input maps to dot-paths, classifying which leaves look
// like identifiers, detecting duplicates and cross-env token leakage, and
// evaluating both against reviewed `allow` exceptions. It is a generic,
// config-driven port of a proven Python prototype (see docs/DESIGN.md).
package uniqueness

// Tier is an environment's protection class (e.g. "prod", "nonprod"). It is
// declared per environment (or resolved from data) and drives whether a
// cross-boundary duplicate is a blocking violation or report-only.
type Tier string

// Unit is one lintable thing: a stack/component instance with a stable ID and
// its flattened per-environment inputs. Inputs[env][dotpath] holds the
// flattened leaf value for that environment (see Flatten).
type Unit struct {
	ID     string
	Envs   []string
	Inputs map[string]map[string]any
}

// Severity classifies a Violation's blocking-ness: "violation" fails the
// command, "report-only" is surfaced but never fails it.
type Severity string

const (
	SeverityViolation  Severity = "violation"
	SeverityReportOnly Severity = "report-only"
)

// Kind identifies which detector produced a Violation.
type Kind string

const (
	KindDuplicate Kind = "duplicate"
	KindEnvToken  Kind = "env-token"
)

// Violation is one flagged finding: a unit/key pair whose value is
// duplicated across (or leaks a token into) the listed envs.
type Violation struct {
	Unit     string
	Key      string
	Value    any
	Envs     []string
	Kind     Kind
	Severity Severity
}

// AllowRule is one reviewed exception (an `allow {}` config block): it
// justifies a violation for a given unit/key across a set of envs, with a
// mandatory reason and an optional expiry after which it no longer applies.
type AllowRule struct {
	Unit    string
	Key     string
	Envs    []string
	Reason  string
	Expires string
}

// Report is the outcome of evaluating all units: violations left
// unjustified (command should fail), allow rules that matched nothing
// (stale — should be removed), and violations that are report-only (never
// fail the command but are still worth surfacing).
type Report struct {
	Unjustified []Violation
	Stale       []AllowRule
	ReportOnly  []Violation
}

// List is the sentinel Flatten emits for a []any leaf: its elements
// stringified, in original order. It stands in for the Python prototype's
// ("__list__", tuple(...)) tuple sentinel.
type List struct {
	Elems []string
}
