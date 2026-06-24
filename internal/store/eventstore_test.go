package store

import (
	"bytes"
	"errors"
	"sync"
	"testing"
)

func ev(typ, data string) StoredEvent { return StoredEvent{Type: typ, Data: []byte(data)} }

func TestEventStoreAppendAndLoad(t *testing.T) {
	es := NewEventStore(newTestDB(t))
	if err := es.Append("exec:1:nonprod", 0, []StoredEvent{ev("A", "a"), ev("B", "b")}); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, version, err := es.Load("exec:1:nonprod")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if version != 2 {
		t.Fatalf("version = %d; want 2", version)
	}
	if len(got) != 2 || got[0].Type != "A" || got[1].Type != "B" {
		t.Fatalf("events = %+v; want [A B] in order", got)
	}
	if !bytes.Equal(got[0].Data, []byte("a")) || !bytes.Equal(got[1].Data, []byte("b")) {
		t.Fatalf("data not preserved: %+v", got)
	}
}

func TestEventStoreContiguousAppend(t *testing.T) {
	es := NewEventStore(newTestDB(t))
	if err := es.Append("s", 0, []StoredEvent{ev("A", "1")}); err != nil {
		t.Fatal(err)
	}
	if err := es.Append("s", 1, []StoredEvent{ev("B", "2")}); err != nil {
		t.Fatalf("second append at v1: %v", err)
	}
	got, version, _ := es.Load("s")
	if version != 2 || len(got) != 2 {
		t.Fatalf("version=%d len=%d; want 2,2", version, len(got))
	}
}

func TestEventStoreStaleVersionConflictWritesNothing(t *testing.T) {
	es := NewEventStore(newTestDB(t))
	if err := es.Append("s", 0, []StoredEvent{ev("A", "1")}); err != nil {
		t.Fatal(err)
	}
	// Caller thinks the stream is still empty (stale expectedVersion 0).
	err := es.Append("s", 0, []StoredEvent{ev("B", "2")})
	if !errors.Is(err, ErrConcurrencyConflict) {
		t.Fatalf("err = %v; want ErrConcurrencyConflict", err)
	}
	_, version, _ := es.Load("s")
	if version != 1 {
		t.Fatalf("version = %d after rejected append; want 1 (nothing written)", version)
	}
}

func TestEventStoreEmptyStream(t *testing.T) {
	es := NewEventStore(newTestDB(t))
	got, version, err := es.Load("nope")
	if err != nil {
		t.Fatal(err)
	}
	if version != 0 || len(got) != 0 {
		t.Fatalf("empty stream: version=%d len=%d; want 0,0", version, len(got))
	}
}

func TestEventStoreConcurrentAppendOneWins(t *testing.T) {
	es := NewEventStore(newTestDB(t))
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = es.Append("s", 0, []StoredEvent{ev("A", "x")})
		}(i)
	}
	wg.Wait()
	okCount, conflictCount := 0, 0
	for _, e := range errs {
		switch {
		case e == nil:
			okCount++
		case errors.Is(e, ErrConcurrencyConflict):
			conflictCount++
		default:
			t.Fatalf("unexpected error: %v", e)
		}
	}
	if okCount != 1 || conflictCount != 1 {
		t.Fatalf("ok=%d conflict=%d; want exactly 1 each", okCount, conflictCount)
	}
	_, version, _ := es.Load("s")
	if version != 1 {
		t.Fatalf("version = %d; want exactly 1 (one writer won)", version)
	}
}

func TestEventStoreAppendEmptyIsNoop(t *testing.T) {
	es := NewEventStore(newTestDB(t))
	if err := es.Append("s", 0, nil); err != nil {
		t.Fatalf("empty append: %v", err)
	}
	_, version, _ := es.Load("s")
	if version != 0 {
		t.Fatalf("version = %d; want 0", version)
	}
}

func TestEventStoreDataByteFidelity(t *testing.T) {
	es := NewEventStore(newTestDB(t))
	blob := []byte{0x00, 0x01, 0xff, 0xfe, '{', '}'}
	if err := es.Append("s", 0, []StoredEvent{{Type: "X", Data: blob}}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := es.Load("s")
	if !bytes.Equal(got[0].Data, blob) {
		t.Fatalf("binary data corrupted: %v", got[0].Data)
	}
}

func TestSnapshotSaveAndLoad(t *testing.T) {
	es := NewEventStore(newTestDB(t))
	if err := es.SaveSnapshot("s", 5, []byte("state-v5")); err != nil {
		t.Fatalf("save: %v", err)
	}
	state, version, ok, err := es.LoadSnapshot("s")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !ok || version != 5 || !bytes.Equal(state, []byte("state-v5")) {
		t.Fatalf("got state=%q version=%d ok=%v; want state-v5,5,true", state, version, ok)
	}
}

func TestSnapshotOverwriteKeepsLatest(t *testing.T) {
	es := NewEventStore(newTestDB(t))
	if err := es.SaveSnapshot("s", 5, []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := es.SaveSnapshot("s", 9, []byte("new")); err != nil {
		t.Fatal(err)
	}
	state, version, ok, _ := es.LoadSnapshot("s")
	if !ok || version != 9 || !bytes.Equal(state, []byte("new")) {
		t.Fatalf("got state=%q version=%d; want new,9", state, version)
	}
}

func TestSnapshotLoadMissing(t *testing.T) {
	es := NewEventStore(newTestDB(t))
	_, _, ok, err := es.LoadSnapshot("nope")
	if err != nil {
		t.Fatalf("load missing: %v", err)
	}
	if ok {
		t.Fatal("ok = true for missing snapshot; want false")
	}
}

func TestSnapshotStateByteFidelity(t *testing.T) {
	es := NewEventStore(newTestDB(t))
	blob := []byte{0x00, 0x7f, 0xff, '}'}
	if err := es.SaveSnapshot("s", 1, blob); err != nil {
		t.Fatal(err)
	}
	state, _, _, _ := es.LoadSnapshot("s")
	if !bytes.Equal(state, blob) {
		t.Fatalf("binary state corrupted: %v", state)
	}
}

func TestIsUniqueViolationMapsPKConflict(t *testing.T) {
	db := newTestDB(t)
	// A real PK collision on the events table yields a unique-violation error.
	_, _ = db.Exec(`INSERT INTO events (stream_id, version, type, data) VALUES ('s',1,'x','{}')`)
	_, err := db.Exec(`INSERT INTO events (stream_id, version, type, data) VALUES ('s',1,'y','{}')`)
	if err == nil {
		t.Fatal("expected a PK unique-violation")
	}
	if !isUniqueViolation(err) {
		t.Fatalf("isUniqueViolation(%v) = false; want true", err)
	}
}
