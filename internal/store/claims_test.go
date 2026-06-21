package store

import (
	"testing"
	"time"
)

func TestClaimsLifecycle(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()
	exp := now.Add(30 * time.Minute)

	if err := ClaimStacks(db, "prod", 7, "exec-1", []string{"a", "b"}, exp); err != nil {
		t.Fatal(err)
	}
	got, err := ClaimedStacks(db, "prod", now)
	if err != nil {
		t.Fatal(err)
	}
	if got["a"] != 7 || got["b"] != 7 || len(got) != 2 {
		t.Fatalf("claimed = %v, want a,b → 7", got)
	}
	// Expired claims are not returned.
	later := exp.Add(time.Minute)
	if g, _ := ClaimedStacks(db, "prod", later); len(g) != 0 {
		t.Fatalf("expired claims returned: %v", g)
	}
	// Renew extends.
	if err := RenewClaims(db, "prod", 7, later.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if g, _ := ClaimedStacks(db, "prod", later); len(g) != 2 {
		t.Fatalf("renew did not extend: %v", g)
	}
	// Release by pr+env.
	if err := ReleaseClaimsByPREnv(db, "prod", 7); err != nil {
		t.Fatal(err)
	}
	if g, _ := ClaimedStacks(db, "prod", now); len(g) != 0 {
		t.Fatalf("release left claims: %v", g)
	}
}

func TestListClaims(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()
	exp := now.Add(30 * time.Minute)

	_ = ClaimStacks(db, "prod", 7, "e1", []string{"b", "a"}, exp) // insert out of order
	got, err := ListClaims(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ListClaims len = %d, want 2", len(got))
	}
	// ordered by stack_path
	if got[0].StackPath != "a" || got[1].StackPath != "b" {
		t.Fatalf("ListClaims order = %v %v, want a,b", got[0].StackPath, got[1].StackPath)
	}
	if got[0].OwnerPR != 7 || got[0].Environment != "prod" {
		t.Fatalf("ListClaims[0] = %+v", got[0])
	}
	// Returns all claims regardless of expiry
	_ = ClaimStacks(db, "prod", 9, "e2", []string{"c"}, now.Add(-time.Minute))
	got2, _ := ListClaims(db, "prod")
	if len(got2) != 3 {
		t.Fatalf("ListClaims with expired: len = %d, want 3", len(got2))
	}
	// Other env is not returned
	_ = ClaimStacks(db, "stage", 5, "e3", []string{"x"}, exp)
	got3, _ := ListClaims(db, "prod")
	if len(got3) != 3 {
		t.Fatalf("ListClaims cross-env contamination: len = %d", len(got3))
	}
}

func TestReleaseClaimStack(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()
	exp := now.Add(30 * time.Minute)
	_ = ClaimStacks(db, "prod", 7, "e1", []string{"a", "b"}, exp)

	if err := ReleaseClaimStack(db, "prod", 7, "a"); err != nil {
		t.Fatal(err)
	}
	got, _ := ListClaims(db, "prod")
	if len(got) != 1 || got[0].StackPath != "b" {
		t.Fatalf("after ReleaseClaimStack: %+v", got)
	}
	// Releasing a non-existent stack is a no-op
	if err := ReleaseClaimStack(db, "prod", 7, "nonexistent"); err != nil {
		t.Fatalf("release nonexistent: %v", err)
	}
}

func TestSweepExpiredClaims(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()
	_ = ClaimStacks(db, "prod", 7, "e1", []string{"a"}, now.Add(-time.Minute)) // already expired
	_ = ClaimStacks(db, "stage", 8, "e2", []string{"b"}, now.Add(time.Hour))   // live
	envs, err := SweepExpiredClaims(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 1 || envs[0] != "prod" {
		t.Fatalf("swept envs = %v, want [prod]", envs)
	}
	if g, _ := ClaimedStacks(db, "stage", now); len(g) != 1 {
		t.Fatalf("swept a live claim: %v", g)
	}
}
