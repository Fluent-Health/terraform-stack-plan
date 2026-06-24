package server

import (
	"testing"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/claims"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// TestHandleClaimAcquireProjectsAndFolds: handleClaim(AcquireClaim) appends to
// the env stream (loadClaims reflects it) AND projects the apply_claims rows.
func TestHandleClaimAcquireProjectsAndFolds(t *testing.T) {
	a, _ := newApplyLockTestApp(t)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	if err := a.shell.handleClaim("prod", claims.AcquireClaim{PR: 7, Stacks: []string{"a", "b"}, Now: now}); err != nil {
		t.Fatalf("handleClaim acquire: %v", err)
	}

	// The folded ClaimSet is the source of truth.
	cs, err := a.shell.loadClaims("prod")
	if err != nil {
		t.Fatalf("loadClaims: %v", err)
	}
	if cs["a"].PR != 7 || cs["b"].PR != 7 {
		t.Fatalf("fold = %+v, want a,b → 7", cs)
	}
	if !cs["a"].ExpiresAt.After(now) {
		t.Fatalf("lease not set: %v", cs["a"].ExpiresAt)
	}

	// The apply_claims projection mirrors the fold (cross-env index for the sweep + UI).
	proj, _ := store.ClaimedStacks(a.db, "prod", now)
	if proj["a"] != 7 || proj["b"] != 7 || len(proj) != 2 {
		t.Fatalf("projection = %v, want a,b → 7", proj)
	}
}

// TestHandleClaimReleaseClearsBoth: ReleaseClaim clears the fold AND the projection.
func TestHandleClaimReleaseClearsBoth(t *testing.T) {
	a, _ := newApplyLockTestApp(t)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	if err := a.shell.handleClaim("prod", claims.AcquireClaim{PR: 7, Stacks: []string{"a", "b"}, Now: now}); err != nil {
		t.Fatal(err)
	}
	if err := a.shell.handleClaim("prod", claims.ReleaseClaim{PR: 7}); err != nil {
		t.Fatalf("handleClaim release: %v", err)
	}

	cs, _ := a.shell.loadClaims("prod")
	if len(cs) != 0 {
		t.Fatalf("fold after release = %+v, want empty", cs)
	}
	proj, _ := store.ClaimedStacks(a.db, "prod", now)
	if len(proj) != 0 {
		t.Fatalf("projection after release = %v, want empty", proj)
	}
}

// TestHandleClaimReleaseStack: ReleaseClaimStack drops one stack from both fold + projection.
func TestHandleClaimReleaseStack(t *testing.T) {
	a, _ := newApplyLockTestApp(t)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	_ = a.shell.handleClaim("prod", claims.AcquireClaim{PR: 7, Stacks: []string{"a", "b"}, Now: now})
	if err := a.shell.handleClaim("prod", claims.ReleaseClaimStack{PR: 7, Stack: "a"}); err != nil {
		t.Fatal(err)
	}
	cs, _ := a.shell.loadClaims("prod")
	if _, ok := cs["a"]; ok {
		t.Fatalf("stack a still claimed: %+v", cs)
	}
	if cs["b"].PR != 7 {
		t.Fatalf("stack b lost: %+v", cs)
	}
	proj, _ := store.ClaimedStacks(a.db, "prod", now)
	if len(proj) != 1 || proj["b"] != 7 {
		t.Fatalf("projection = %v, want only b → 7", proj)
	}
}

// TestEvalApplyLockOverlapFromFold: evalApplyLock reads the folded ClaimSet —
// PR 5 holds "a"; PR 7 touching "a","b" is held; a disjoint PR is clear.
func TestEvalApplyLockOverlapFromFold(t *testing.T) {
	a, _ := newApplyLockTestApp(t)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	if err := a.shell.handleClaim("prod", claims.AcquireClaim{PR: 5, Stacks: []string{"a"}, Now: now}); err != nil {
		t.Fatal(err)
	}

	// PR 7 overlaps on "a" => held, blocking [a].
	v := a.evalApplyLock("prod", 7, []string{"a", "b"}, now)
	if v.State != "held" || len(v.Blocking) != 1 || v.Blocking[0] != "a" {
		t.Fatalf("overlap verdict = %+v, want held blocking [a]", v)
	}

	// PR 5 itself is clear (own claim).
	if v := a.evalApplyLock("prod", 5, []string{"a"}, now); v.State != "clear" {
		t.Fatalf("self verdict = %+v, want clear", v)
	}

	// A disjoint PR is clear.
	if v := a.evalApplyLock("prod", 9, []string{"c", "d"}, now); v.State != "clear" {
		t.Fatalf("disjoint verdict = %+v, want clear", v)
	}

	// After the lease lapses, the held query is clear (expiry enforced at read).
	if v := a.evalApplyLock("prod", 7, []string{"a"}, now.Add(claims.Lease()+time.Minute)); v.State != "clear" {
		t.Fatalf("post-expiry verdict = %+v, want clear", v)
	}
}
