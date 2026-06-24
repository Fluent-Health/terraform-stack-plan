package store

import (
	"testing"
	"time"
)

func TestListClaims(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()
	exp := now.Add(30 * time.Minute)

	_ = ReplaceClaims(db, "prod", map[string]Claim{
		"b": {OwnerPR: 7, ExpiresAt: exp},
		"a": {OwnerPR: 7, ExpiresAt: exp},
	})
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
	// Returns all claims regardless of expiry (pass all 3 rows for prod)
	_ = ReplaceClaims(db, "prod", map[string]Claim{
		"a": {OwnerPR: 7, ExpiresAt: exp},
		"b": {OwnerPR: 7, ExpiresAt: exp},
		"c": {OwnerPR: 9, ExpiresAt: now.Add(-time.Minute)},
	})
	got2, _ := ListClaims(db, "prod")
	if len(got2) != 3 {
		t.Fatalf("ListClaims with expired: len = %d, want 3", len(got2))
	}
	// Other env is not returned
	_ = ReplaceClaims(db, "stage", map[string]Claim{
		"x": {OwnerPR: 5, ExpiresAt: exp},
	})
	got3, _ := ListClaims(db, "prod")
	if len(got3) != 3 {
		t.Fatalf("ListClaims cross-env contamination: len = %d", len(got3))
	}
}

func TestSweepExpiredClaims(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()
	_ = ReplaceClaims(db, "prod", map[string]Claim{"a": {OwnerPR: 7, ExpiresAt: now.Add(-time.Minute)}})
	_ = ReplaceClaims(db, "stage", map[string]Claim{"b": {OwnerPR: 8, ExpiresAt: now.Add(time.Hour)}})
	envs, err := SweepExpiredClaims(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 1 || envs[0] != "prod" {
		t.Fatalf("swept envs = %v, want [prod]", envs)
	}
	got2, _ := ListClaims(db, "stage")
	live := 0
	for _, c := range got2 {
		if c.ExpiresAt.After(now) {
			live++
		}
	}
	if live != 1 {
		t.Fatalf("swept a live claim: %v", got2)
	}
}
