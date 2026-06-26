package statemove

import (
	"fmt"
	"sort"

	tfjson "github.com/hashicorp/terraform-json"
)

// AddressSet maps resource addresses to their corresponding ProviderName.
type AddressSet map[string]string

// DestProviders represents a set of destination providers.
type DestProviders map[string]bool

type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

type Diagnostic struct {
	Code     string
	Severity Severity
	Message  string
}

// expandPairs resolves each declared move pair against the live source/dest state
// addresses. A module-/prefix-level pair (e.g. module.x[0] -> module.y) fans out
// to the concrete per-resource pairs it covers (module.x[0].r -> module.y.r),
// mirroring CrossStackPairs (which fans out against a plan) — so a manifest may
// name a whole module and the move still works. An exact resource/instance pair
// resolves to itself. An already-moved pair (nothing under `from` in the source,
// children under `to` in the dest) is re-keyed from `to`'s children so decide()
// returns Skip (idempotent re-run). A pair matching neither side is kept verbatim
// so decide() fails closed. matches() is the same exact-or-child ("."/"[" boundary)
// relation classify uses, keeping plan-time and apply-time semantics aligned.
func expandPairs(srcAddrs, dstAddrs AddressSet, pairs []Move) []Move {
	var out []Move
	for _, p := range pairs {
		var src, dst []string
		for a := range srcAddrs {
			if matches(a, p.From) {
				src = append(src, a)
			}
		}
		for a := range dstAddrs {
			if matches(a, p.To) {
				dst = append(dst, a)
			}
		}
		switch {
		case len(src) > 0:
			sort.Strings(src)
			for _, s := range src {
				out = append(out, Move{From: s, To: p.To + s[len(p.From):]})
			}
		case len(dst) > 0:
			sort.Strings(dst)
			for _, d := range dst {
				out = append(out, Move{From: p.From + d[len(p.To):], To: d})
			}
		default:
			out = append(out, p)
		}
	}
	return out
}

// Decision is the per-move runtime action derived from the two live states.
type Decision int

const (
	DecisionMove Decision = iota // source has it, dest doesn't → move
	DecisionSkip                 // dest already has it → idempotent skip
)

// decide is the fail-closed idempotency table for one (from in source, to in
// dest) pair, given the address sets of both live states.
func decide(srcAddrs, dstAddrs AddressSet, from, to string) (Decision, error) {
	_, inSrc := srcAddrs[from]
	_, inDst := dstAddrs[to]
	switch {
	case inSrc && !inDst:
		return DecisionMove, nil
	case !inSrc && inDst:
		return DecisionSkip, nil
	case inSrc && inDst:
		return 0, fmt.Errorf("ambiguous: %q is in the source state AND %q is in the destination state (would duplicate)", from, to)
	default:
		return 0, fmt.Errorf("missing: %q is not in the source state and %q is not in the destination state (manifest wrong or already pruned)", from, to)
	}
}

// stateAddresses collects every resource address in a state (root + child modules).
func stateAddresses(s *tfjson.State) AddressSet {
	out := AddressSet{}
	if s == nil || s.Values == nil {
		return out
	}
	var walk func(m *tfjson.StateModule)
	walk = func(m *tfjson.StateModule) {
		if m == nil {
			return
		}
		for _, r := range m.Resources {
			out[r.Address] = r.ProviderName
		}
		for _, c := range m.ChildModules {
			walk(c)
		}
	}
	walk(s.Values.RootModule)
	return out
}
