package store

import (
	"reflect"
	"testing"
)

func TestApplyLockCheckRoundTrip(t *testing.T) {
	db := newTestDB(t)
	c := ApplyLockCheck{Environment: "prod", HeadSHA: "sha1", CheckRunID: 99, PR: 7,
		Stacks: []string{"a", "b"}, State: "held", Kind: "merge_group"}
	if err := UpsertApplyLockCheck(db, c); err != nil {
		t.Fatal(err)
	}
	got, ok, err := GetApplyLockCheck(db, "prod", "sha1")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.CheckRunID != 99 || got.PR != 7 || got.State != "held" || got.Kind != "merge_group" ||
		!reflect.DeepEqual(got.Stacks, []string{"a", "b"}) {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	// HeldApplyLockChecks returns only held rows.
	c2 := c
	c2.HeadSHA, c2.State = "sha2", "clear"
	_ = UpsertApplyLockCheck(db, c2)
	held, err := HeldApplyLockChecks(db, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 1 || held[0].HeadSHA != "sha1" {
		t.Fatalf("held = %+v, want only sha1", held)
	}
}
