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
