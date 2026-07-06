package claims

import (
	"sort"
	"time"
)

// Verdict is the result of a Held query.
type Verdict struct {
	Held        bool
	Blocking    []string // blocked stacks, sorted
	BlockingPRs []int    // owning PRs of the blocking claims, sorted, de-duplicated
}

// Held reports whether any of pr's stacks is claimed by ANOTHER pr with an
// unexpired lease at now. Blocking is sorted for determinism. Mirrors the old
// evalApplyLock/overlap + the `expires_at > now` filter.
//
// Note: expiry is ONLY enforced here at read time — there is no ClaimExpired
// event. The log replays deterministically; stale entries are simply invisible
// to Held once their lease lapses.
func Held(s ClaimSet, pr int, stacks []string, now time.Time) Verdict {
	var blocking []string
	prSet := map[int]bool{}
	for _, stack := range stacks {
		c, ok := s[stack]
		if ok && c.PR != pr && c.ExpiresAt.After(now) {
			blocking = append(blocking, stack)
			prSet[c.PR] = true
		}
	}
	sort.Strings(blocking)
	prs := make([]int, 0, len(prSet))
	for p := range prSet {
		prs = append(prs, p)
	}
	sort.Ints(prs)
	if len(prs) == 0 {
		prs = nil
	}
	return Verdict{Held: len(blocking) > 0, Blocking: blocking, BlockingPRs: prs}
}
