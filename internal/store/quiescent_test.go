package store

import "testing"

func TestIsQuiescent(t *testing.T) {
	db := newTestDB(t)
	// No executions, no gate targets → quiescent.
	q, err := IsQuiescent(db)
	if err != nil {
		t.Fatal(err)
	}
	if !q {
		t.Fatal("empty store should be quiescent")
	}
	// A non-terminal gate target → not quiescent.
	if err := UpsertTarget(db, 7, "staging", "iam", "p1", "g1", "AWAITING"); err != nil {
		t.Fatal(err)
	}
	q, _ = IsQuiescent(db)
	if q {
		t.Fatal("open gate target should block quiescence")
	}
	// A terminal gate target → quiescent again.
	if err := UpsertTarget(db, 7, "staging", "iam", "p1", "g1", "REVOKED"); err != nil {
		t.Fatal(err)
	}
	q, _ = IsQuiescent(db)
	if !q {
		t.Fatal("only terminal targets remain → should be quiescent")
	}
}
