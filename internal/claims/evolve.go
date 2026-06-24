package claims

import "maps"

// Evolve applies a single domain Event to a ClaimSet, returning the new state.
// It is total (never panics on any state/event combination), pure (no I/O), and
// deterministic. Crucially, it NEVER mutates the input map — it always clones
// first so that replay (folding the same event sequence multiple times) is safe.
func Evolve(s ClaimSet, e Event) ClaimSet {
	out := maps.Clone(s)
	switch ev := e.(type) {
	case ClaimAcquired:
		for _, stack := range ev.Stacks {
			out[stack] = Claim{PR: ev.PR, ExpiresAt: ev.ExpiresAt}
		}
	case ClaimRenewed:
		for k, c := range out {
			if c.PR == ev.PR {
				out[k] = Claim{PR: c.PR, ExpiresAt: ev.ExpiresAt}
			}
		}
	case ClaimReleased:
		for k, c := range out {
			if c.PR == ev.PR {
				delete(out, k)
			}
		}
	case ClaimStackReleased:
		if c, ok := out[ev.Stack]; ok && c.PR == ev.PR {
			delete(out, ev.Stack)
		}
	}
	return out
}
