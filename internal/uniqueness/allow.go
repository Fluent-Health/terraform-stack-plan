package uniqueness

import (
	"path/filepath"
	"time"
)

// AllowMatches reports whether allow rule a justifies violation v as of now:
// their Unit is identical, a's Key equals v's Key exactly or as a
// filepath.Match glob against it, a's Envs is a superset of v's Envs, and a
// is not expired. Mirrors the Python prototype's entry_matches.
func AllowMatches(a AllowRule, v Violation, now time.Time) bool {
	if a.Unit != v.Unit {
		return false
	}
	if a.Key != v.Key {
		if ok, err := filepath.Match(a.Key, v.Key); err != nil || !ok {
			return false
		}
	}
	if !envsSuperset(a.Envs, v.Envs) {
		return false
	}
	return !isExpired(a.Expires, now)
}

// envsSuperset reports whether every env in sub is present in super.
func envsSuperset(super, sub []string) bool {
	set := make(map[string]bool, len(super))
	for _, e := range super {
		set[e] = true
	}
	for _, e := range sub {
		if !set[e] {
			return false
		}
	}
	return true
}

// isExpired reports whether an allow rule's Expires date has passed as of
// now: an empty Expires never expires; otherwise it is expired iff the
// parsed date (YYYY-MM-DD) is strictly before now's calendar date. A
// malformed Expires string is treated as expired (fail-closed — a rule with
// a date we can't understand should not silently keep justifying anything).
// Mirrors the Python prototype's _expired.
func isExpired(expires string, now time.Time) bool {
	if expires == "" {
		return false
	}
	exp, err := time.Parse("2006-01-02", expires)
	if err != nil {
		return true
	}
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return exp.Before(today)
}

// Partition splits violations against the given allow rules as of now: only
// SeverityViolation entries require justification (report-only violations
// never need an allow and are never unjustified). Expired allows are
// dropped from consideration entirely first — they neither justify a
// violation nor count as stale. Among the remaining active allows, a
// violation with no matching active allow is unjustified; an active allow
// that matches zero violations is stale. Mirrors the Python prototype's
// partition (first-matching-allow semantics).
func Partition(vios []Violation, allows []AllowRule, now time.Time) (unjustified []Violation, stale []AllowRule) {
	active := make([]AllowRule, 0, len(allows))
	for _, a := range allows {
		if !isExpired(a.Expires, now) {
			active = append(active, a)
		}
	}

	matched := make([]bool, len(active))
	for _, v := range vios {
		if v.Severity != SeverityViolation {
			continue
		}
		hit := -1
		for i, a := range active {
			if AllowMatches(a, v, now) {
				hit = i
				break
			}
		}
		if hit == -1 {
			unjustified = append(unjustified, v)
		} else {
			matched[hit] = true
		}
	}

	for i, a := range active {
		if !matched[i] {
			stale = append(stale, a)
		}
	}
	return unjustified, stale
}
