package uniqueness

import (
	"testing"
	"time"
)

// TestAllowMatchesExactKey verifies an allow rule whose Key is an exact,
// non-glob match for the violation's Key (and whose Unit/Envs line up)
// matches.
func TestAllowMatchesExactKey(t *testing.T) {
	a := AllowRule{Unit: "svc/api", Key: "client_id", Envs: []string{"dev", "prod"}, Reason: "shared by design"}
	v := Violation{Unit: "svc/api", Key: "client_id", Envs: []string{"dev", "prod"}, Severity: SeverityViolation}
	if !AllowMatches(a, v, time.Now()) {
		t.Fatal("expected exact key/unit/envs match to match")
	}
}

// TestAllowMatchesGlobKey verifies an allow rule's Key can be a glob pattern
// (filepath.Match syntax) matched against the violation's Key.
func TestAllowMatchesGlobKey(t *testing.T) {
	a := AllowRule{Unit: "svc/api", Key: "*_url", Envs: []string{"dev", "prod"}, Reason: "shared by design"}
	v := Violation{Unit: "svc/api", Key: "callback_url", Envs: []string{"dev", "prod"}, Severity: SeverityViolation}
	if !AllowMatches(a, v, time.Now()) {
		t.Fatal("expected glob key match to match")
	}
}

// TestAllowMatchesGlobKeyNoMatch verifies a non-matching glob pattern (and a
// non-equal literal key) does not match.
func TestAllowMatchesGlobKeyNoMatch(t *testing.T) {
	a := AllowRule{Unit: "svc/api", Key: "*_url", Envs: []string{"dev", "prod"}, Reason: "shared by design"}
	v := Violation{Unit: "svc/api", Key: "client_id", Envs: []string{"dev", "prod"}, Severity: SeverityViolation}
	if AllowMatches(a, v, time.Now()) {
		t.Fatal("expected non-matching glob key to not match")
	}
}

// TestAllowMatchesUnitMismatch verifies a differing Unit never matches,
// regardless of key/envs.
func TestAllowMatchesUnitMismatch(t *testing.T) {
	a := AllowRule{Unit: "svc/api", Key: "client_id", Envs: []string{"dev", "prod"}, Reason: "shared by design"}
	v := Violation{Unit: "svc/other", Key: "client_id", Envs: []string{"dev", "prod"}, Severity: SeverityViolation}
	if AllowMatches(a, v, time.Now()) {
		t.Fatal("expected unit mismatch to not match")
	}
}

// TestAllowMatchesEnvsSuperset verifies an allow whose Envs is a strict
// superset of the violation's Envs still matches (the allow can cover more
// envs than any single violation touches).
func TestAllowMatchesEnvsSuperset(t *testing.T) {
	a := AllowRule{Unit: "svc/api", Key: "client_id", Envs: []string{"dev", "staging", "prod"}, Reason: "shared by design"}
	v := Violation{Unit: "svc/api", Key: "client_id", Envs: []string{"dev", "prod"}, Severity: SeverityViolation}
	if !AllowMatches(a, v, time.Now()) {
		t.Fatal("expected allow envs superset of violation envs to match")
	}
}

// TestAllowMatchesEnvsNotSuperset verifies an allow whose Envs does not
// cover all of the violation's Envs does not match.
func TestAllowMatchesEnvsNotSuperset(t *testing.T) {
	a := AllowRule{Unit: "svc/api", Key: "client_id", Envs: []string{"dev"}, Reason: "shared by design"}
	v := Violation{Unit: "svc/api", Key: "client_id", Envs: []string{"dev", "prod"}, Severity: SeverityViolation}
	if AllowMatches(a, v, time.Now()) {
		t.Fatal("expected allow envs not covering violation envs to not match")
	}
}

// TestAllowMatchesExpired verifies an allow whose Expires date is strictly
// before now's date no longer matches.
func TestAllowMatchesExpired(t *testing.T) {
	a := AllowRule{Unit: "svc/api", Key: "client_id", Envs: []string{"dev", "prod"}, Reason: "temporary", Expires: "2020-01-01"}
	v := Violation{Unit: "svc/api", Key: "client_id", Envs: []string{"dev", "prod"}, Severity: SeverityViolation}
	now := time.Date(2026, time.July, 17, 0, 0, 0, 0, time.UTC)
	if AllowMatches(a, v, now) {
		t.Fatal("expected expired allow to not match")
	}
}

// TestAllowMatchesExpiresTodayStillMatches verifies an allow whose Expires
// equals now's date is NOT yet expired (expiry is inclusive of its date).
func TestAllowMatchesExpiresTodayStillMatches(t *testing.T) {
	a := AllowRule{Unit: "svc/api", Key: "client_id", Envs: []string{"dev", "prod"}, Reason: "temporary", Expires: "2026-07-17"}
	v := Violation{Unit: "svc/api", Key: "client_id", Envs: []string{"dev", "prod"}, Severity: SeverityViolation}
	now := time.Date(2026, time.July, 17, 15, 30, 0, 0, time.UTC)
	if !AllowMatches(a, v, now) {
		t.Fatal("expected allow expiring today to still match")
	}
}

// TestAllowMatchesNoExpiryNeverExpires verifies an allow with an empty
// Expires never expires.
func TestAllowMatchesNoExpiryNeverExpires(t *testing.T) {
	a := AllowRule{Unit: "svc/api", Key: "client_id", Envs: []string{"dev", "prod"}, Reason: "permanent"}
	v := Violation{Unit: "svc/api", Key: "client_id", Envs: []string{"dev", "prod"}, Severity: SeverityViolation}
	now := time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC)
	if !AllowMatches(a, v, now) {
		t.Fatal("expected allow with no expiry to never expire")
	}
}

// TestPartitionJustifiedViolationIsClean verifies a violation matched by an
// active allow rule produces no unjustified entries and leaves the allow
// non-stale.
func TestPartitionJustifiedViolationIsClean(t *testing.T) {
	v := Violation{Unit: "svc/api", Key: "client_id", Envs: []string{"dev", "prod"}, Severity: SeverityViolation}
	a := AllowRule{Unit: "svc/api", Key: "client_id", Envs: []string{"dev", "prod"}, Reason: "shared by design"}

	unjustified, stale := Partition([]Violation{v}, []AllowRule{a}, time.Now())

	if len(unjustified) != 0 {
		t.Errorf("unjustified = %+v, want empty", unjustified)
	}
	if len(stale) != 0 {
		t.Errorf("stale = %+v, want empty", stale)
	}
}

// TestPartitionUnjustifiedViolationHasNoAllow verifies a violation with no
// matching allow rule at all lands in unjustified.
func TestPartitionUnjustifiedViolationHasNoAllow(t *testing.T) {
	v := Violation{Unit: "svc/api", Key: "account_id", Envs: []string{"dev", "prod"}, Severity: SeverityViolation}

	unjustified, stale := Partition([]Violation{v}, nil, time.Now())

	if len(unjustified) != 1 || unjustified[0].Key != "account_id" {
		t.Fatalf("unjustified = %+v, want [account_id]", unjustified)
	}
	if len(stale) != 0 {
		t.Errorf("stale = %+v, want empty", stale)
	}
}

// TestPartitionAllowMatchingNothingIsStale verifies an active allow rule
// that matches zero violations is reported as stale.
func TestPartitionAllowMatchingNothingIsStale(t *testing.T) {
	a := AllowRule{Unit: "svc/api", Key: "nonexistent_id", Envs: []string{"dev", "prod"}, Reason: "no longer applies"}

	unjustified, stale := Partition(nil, []AllowRule{a}, time.Now())

	if len(unjustified) != 0 {
		t.Errorf("unjustified = %+v, want empty", unjustified)
	}
	if len(stale) != 1 || stale[0].Key != "nonexistent_id" {
		t.Fatalf("stale = %+v, want [nonexistent_id]", stale)
	}
}

// TestPartitionExpiredAllowIsNeitherActiveNorStale verifies an expired allow
// rule is dropped entirely: it neither justifies a violation nor is it
// reported as stale (an expired allow should just be deleted by hand, not
// flagged as a currently-stale entry).
func TestPartitionExpiredAllowIsNeitherActiveNorStale(t *testing.T) {
	v := Violation{Unit: "svc/api", Key: "client_id", Envs: []string{"dev", "prod"}, Severity: SeverityViolation}
	a := AllowRule{Unit: "svc/api", Key: "client_id", Envs: []string{"dev", "prod"}, Reason: "temporary", Expires: "2020-01-01"}
	now := time.Date(2026, time.July, 17, 0, 0, 0, 0, time.UTC)

	unjustified, stale := Partition([]Violation{v}, []AllowRule{a}, now)

	if len(unjustified) != 1 {
		t.Errorf("unjustified = %+v, want the violation (expired allow can't justify it)", unjustified)
	}
	if len(stale) != 0 {
		t.Errorf("stale = %+v, want empty (expired allow is dropped, not stale)", stale)
	}
}

// TestPartitionReportOnlyNeverNeedsAllow verifies a report-only violation is
// never placed in unjustified, even with zero allow rules present.
func TestPartitionReportOnlyNeverNeedsAllow(t *testing.T) {
	v := Violation{Unit: "svc/api", Key: "client_id", Envs: []string{"dev1", "dev2"}, Severity: SeverityReportOnly}

	unjustified, stale := Partition([]Violation{v}, nil, time.Now())

	if len(unjustified) != 0 {
		t.Errorf("unjustified = %+v, want empty (report-only never needs an allow)", unjustified)
	}
	if len(stale) != 0 {
		t.Errorf("stale = %+v, want empty", stale)
	}
}

// TestPartitionFirstMatchingAllowAbsorbsMultipleViolations verifies a single
// allow rule can justify more than one violation (e.g. via a glob key) and
// is not stale as long as it matched at least one.
func TestPartitionFirstMatchingAllowAbsorbsMultipleViolations(t *testing.T) {
	v1 := Violation{Unit: "svc/api", Key: "callback_url", Envs: []string{"dev", "prod"}, Severity: SeverityViolation}
	v2 := Violation{Unit: "svc/api", Key: "webhook_url", Envs: []string{"dev", "prod"}, Severity: SeverityViolation}
	a := AllowRule{Unit: "svc/api", Key: "*_url", Envs: []string{"dev", "prod"}, Reason: "shared sandbox urls"}

	unjustified, stale := Partition([]Violation{v1, v2}, []AllowRule{a}, time.Now())

	if len(unjustified) != 0 {
		t.Errorf("unjustified = %+v, want empty", unjustified)
	}
	if len(stale) != 0 {
		t.Errorf("stale = %+v, want empty", stale)
	}
}
