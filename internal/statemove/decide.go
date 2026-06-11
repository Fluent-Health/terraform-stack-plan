package statemove

import (
	"fmt"

	tfjson "github.com/hashicorp/terraform-json"
)

// Decision is the per-move runtime action derived from the two live states.
type Decision int

const (
	DecisionMove Decision = iota // source has it, dest doesn't → move
	DecisionSkip                 // dest already has it → idempotent skip
)

// decide is the fail-closed idempotency table for one (from in source, to in
// dest) pair, given the address sets of both live states.
func decide(srcAddrs, dstAddrs map[string]bool, from, to string) (Decision, error) {
	inSrc, inDst := srcAddrs[from], dstAddrs[to]
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
func stateAddresses(s *tfjson.State) map[string]bool {
	out := map[string]bool{}
	if s == nil || s.Values == nil {
		return out
	}
	var walk func(m *tfjson.StateModule)
	walk = func(m *tfjson.StateModule) {
		if m == nil {
			return
		}
		for _, r := range m.Resources {
			out[r.Address] = true
		}
		for _, c := range m.ChildModules {
			walk(c)
		}
	}
	walk(s.Values.RootModule)
	return out
}
