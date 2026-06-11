package store

import "testing"

func TestStackOutputUpsertAndGet(t *testing.T) {
	db := newTestDB(t)
	if err := UpsertStackOutput(db, "e1", "stacks/a", "log", "", "tail-excerpt"); err != nil {
		t.Fatal(err)
	}
	if err := UpsertStackOutput(db, "e1", "stacks/a", "log", "obj/key", "more-excerpt"); err != nil {
		t.Fatal(err)
	}
	pointer, excerpt, ok, err := GetStackOutput(db, "e1", "stacks/a", "log")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || pointer != "obj/key" || excerpt != "more-excerpt" {
		t.Fatalf("output = %q/%q/%v", pointer, excerpt, ok)
	}
	if _, _, ok, _ := GetStackOutput(db, "e1", "stacks/a", "verify"); ok {
		t.Error("unset kind should report ok=false")
	}
}
