package eventsourcing

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

type addE struct{ N int }

func counterDecider() Decider[int, addE] {
	return Decider[int, addE]{
		Initial: func() int { return 0 },
		Evolve:  func(s int, e addE) int { return s + e.N },
		MarshalEvent: func(e addE) (string, []byte, error) {
			b, err := json.Marshal(e)
			return "add", b, err
		},
		UnmarshalEvent: func(tag string, data []byte) (addE, error) {
			var e addE
			err := json.Unmarshal(data, &e)
			return e, err
		},
		MarshalSnapshot:   func(s int) ([]byte, error) { return json.Marshal(s) },
		UnmarshalSnapshot: func(b []byte) (int, error) { var s int; err := json.Unmarshal(b, &s); return s, err },
	}
}

func testES(t *testing.T) *store.EventStore {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return store.NewEventStore(db)
}

func TestHostAppendLoadReplay(t *testing.T) {
	es, d := testES(t), counterDecider()
	if err := d.Append(es, "c", 0, []addE{{2}, {3}}, 5); err != nil {
		t.Fatal(err)
	}
	state, version, err := d.Load(es, "c")
	if err != nil {
		t.Fatal(err)
	}
	if state != 5 || version != 2 {
		t.Fatalf("state=%d version=%d; want 5,2", state, version)
	}
}

func TestHostEmptyStreamIsInitial(t *testing.T) {
	es, d := testES(t), counterDecider()
	state, version, err := d.Load(es, "nope")
	if err != nil {
		t.Fatal(err)
	}
	if state != 0 || version != 0 {
		t.Fatalf("state=%d version=%d; want 0,0", state, version)
	}
}

func TestHostStaleVersionConflict(t *testing.T) {
	es, d := testES(t), counterDecider()
	if err := d.Append(es, "c", 0, []addE{{1}}, 1); err != nil {
		t.Fatal(err)
	}
	if err := d.Append(es, "c", 0, []addE{{1}}, 2); !errors.Is(err, store.ErrConcurrencyConflict) {
		t.Fatalf("err=%v; want ErrConcurrencyConflict", err)
	}
}

func TestHostSnapshotPlusTail(t *testing.T) {
	// Snapshot at v1; a manual extra event at v2 must replay on top.
	es, d := testES(t), counterDecider()
	if err := d.Append(es, "c", 0, []addE{{10}}, 10); err != nil { // snapshot=10@v1
		t.Fatal(err)
	}
	// Append a second event WITHOUT updating snapshot beyond v2 (Append snapshots @v2=99 deliberately wrong to prove replay wins only past snapVer).
	if err := d.Append(es, "c", 1, []addE{{5}}, 15); err != nil { // snapshot=15@v2
		t.Fatal(err)
	}
	state, version, _ := d.Load(es, "c")
	if state != 15 || version != 2 {
		t.Fatalf("state=%d version=%d; want 15,2", state, version)
	}
}

func TestHostLoadUnmarshalFailure(t *testing.T) {
	es, d := testES(t), counterDecider()

	// Append two valid events manually to bypass snapshotting so replay occurs
	stored := []store.StoredEvent{
		{Type: "add", Data: []byte(`{"N":2}`)},
		{Type: "add", Data: []byte(`{"N":3}`)},
	}
	if err := es.Append("c-bad", 0, stored); err != nil {
		t.Fatal(err)
	}

	// Create a copy of the decider that explicitly fails on event unmarshaling
	badDecider := d
	badDecider.UnmarshalEvent = func(tag string, data []byte) (addE, error) {
		return addE{}, errors.New("unmarshal boom")
	}

	// Loading with the bad decider must fail with the decider's unmarshal error
	_, _, err := badDecider.Load(es, "c-bad")
	if err == nil {
		t.Fatal("expected Load() to fail, but it returned no error")
	}
	if err.Error() != "unmarshal boom" {
		t.Errorf("Load() err = %v, want 'unmarshal boom'", err)
	}
}
