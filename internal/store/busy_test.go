package store

import (
	"fmt"
	"sync"
	"testing"
)

// TestAppendSurvivesConcurrentWriters reproduces an apply that hung in
// `report`: a stack's terminal /api/update 500'd in 18ms while a
// sibling stack's handler and the log pump were writing, so its `safe` tick was
// never recorded and driveApply waited on it forever.
//
// EventStore.Append opens a DEFERRED transaction and reads (SELECT MAX(version))
// before it writes. In WAL mode, when another connection commits between that
// read and the INSERT, SQLite returns SQLITE_BUSY(_SNAPSHOT) at once — the
// busy_timeout handler is never consulted, because waiting cannot make a stale
// read snapshot current. Only BEGIN IMMEDIATE (write lock taken up front, where
// busy_timeout does apply) closes the window.
func TestAppendSurvivesConcurrentWriters(t *testing.T) {
	db := newTestDB(t)
	es := NewEventStore(db)

	const appenders, writers, rounds = 4, 4, 50
	var wg sync.WaitGroup
	errs := make(chan error, appenders*rounds+writers*rounds)

	for a := 0; a < appenders; a++ {
		wg.Add(1)
		go func(a int) {
			defer wg.Done()
			stream := fmt.Sprintf("run:exec-%d", a)
			for v := 0; v < rounds; v++ {
				if err := es.Append(stream, v, []StoredEvent{{Type: "tick", Data: []byte(`{}`)}}); err != nil {
					errs <- fmt.Errorf("append %s v%d: %w", stream, v, err)
					return
				}
			}
		}(a)
	}
	// Autocommit writers on another table stand in for the /api/logs pump and
	// the sibling handler's claim/log writes.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				if _, err := db.Exec(
					`INSERT INTO snapshots (stream_id, version, state) VALUES (?,?,?)
					 ON CONFLICT(stream_id) DO UPDATE SET version = excluded.version`,
					fmt.Sprintf("noise-%d", w), i, []byte(`{}`)); err != nil {
					errs <- fmt.Errorf("writer %d: %w", w, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
