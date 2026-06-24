package claims

import (
	"sort"
	"time"
)

// Verdict is the result of a Held query.
type Verdict struct {
	Held     bool
	Blocking []string
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
	for _, stack := range stacks {
		c, ok := s[stack]
		if ok && c.PR != pr && c.ExpiresAt.After(now) {
			blocking = append(blocking, stack)
		}
	}
	sort.Strings(blocking)
	return Verdict{Held: len(blocking) > 0, Blocking: blocking}
}
