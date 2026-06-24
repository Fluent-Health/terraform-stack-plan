package store

import (
	"database/sql"
	"errors"
	"sync"

	sqlite3 "modernc.org/sqlite"
)

// ErrConcurrencyConflict is returned by Append when the stream's current version
// is not the expected version — i.e. another writer advanced the stream since the
// caller last read it. The caller should reload and retry.
var ErrConcurrencyConflict = errors.New("eventstore: concurrency conflict")

// isUniqueViolation reports whether err is a SQLite UNIQUE or PRIMARY KEY
// constraint violation. This is the schema-level optimistic-concurrency backstop
// on (stream_id, version): if two writers race past the in-tx MAX-version check
// (or the in-process mutex is absent in a future multi-writer topology), the
// INSERT will fail with one of these codes and Append maps it to
// ErrConcurrencyConflict.
//
// modernc.org/sqlite wraps constraint errors as *sqlite.Error; Code() returns the
// SQLite extended result code:
//
//	1555 = SQLITE_CONSTRAINT_PRIMARYKEY
//	2067 = SQLITE_CONSTRAINT_UNIQUE
func isUniqueViolation(err error) bool {
	var se *sqlite3.Error
	if errors.As(err, &se) {
		c := se.Code()
		return c == 1555 || c == 2067 // SQLITE_CONSTRAINT_PRIMARYKEY / _UNIQUE
	}
	return false
}

// StoredEvent is one opaque appended fact. Type is an event-type tag and Data is
// the opaque payload (JSON bytes in practice). The store never interprets either.
type StoredEvent struct {
	Type string
	Data []byte
}

// EventStore is the append-only event log + snapshot cache over SQLite. It is
// domain-agnostic: it deals only in StoredEvent and []byte snapshot blobs.
type EventStore struct {
	db *sql.DB
	// mu serializes Append in-process so the in-transaction version check is
	// authoritative. The server is a single writer (one instance; Litestream
	// single-primary), so a process mutex is sufficient and avoids SQLite
	// busy-snapshot nondeterminism between concurrent deferred transactions. The
	// PRIMARY KEY(stream_id, version) remains the schema-level backstop.
	mu sync.Mutex
}

// NewEventStore wraps db as an EventStore.
func NewEventStore(db *sql.DB) *EventStore { return &EventStore{db: db} }

// Append writes evs as the next contiguous versions after expectedVersion (the
// caller's last-seen stream version; 0 for a new/empty stream). It is
// all-or-nothing. Returns ErrConcurrencyConflict if the stream's current version
// is not expectedVersion.
func (s *EventStore) Append(streamID string, expectedVersion int, evs []StoredEvent) error {
	if len(evs) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var cur int
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(version), 0) FROM events WHERE stream_id = ?`,
		streamID).Scan(&cur); err != nil {
		return err
	}
	if cur != expectedVersion {
		return ErrConcurrencyConflict
	}
	for i, e := range evs {
		if _, err := tx.Exec(
			`INSERT INTO events (stream_id, version, type, data) VALUES (?,?,?,?)`,
			streamID, expectedVersion+1+i, e.Type, e.Data); err != nil {
			if isUniqueViolation(err) {
				return ErrConcurrencyConflict
			}
			return err
		}
	}
	return tx.Commit()
}

// Load returns the stream's events in version order plus its current version
// (0 and an empty slice for an unknown/empty stream).
func (s *EventStore) Load(streamID string) ([]StoredEvent, int, error) {
	rows, err := s.db.Query(
		`SELECT version, type, data FROM events WHERE stream_id = ? ORDER BY version`,
		streamID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []StoredEvent{}
	version := 0
	for rows.Next() {
		var v int
		var e StoredEvent
		if err := rows.Scan(&v, &e.Type, &e.Data); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
		version = v
	}
	return out, version, rows.Err()
}

// SaveSnapshot upserts the latest snapshot for the stream (last-write-wins). No
// concurrency check: the events table is authoritative, so a stale snapshot only
// means replaying a longer event tail. version is the event version the snapshot
// reflects.
func (s *EventStore) SaveSnapshot(streamID string, version int, state []byte) error {
	_, err := s.db.Exec(
		`INSERT INTO snapshots (stream_id, version, state) VALUES (?,?,?)
		 ON CONFLICT(stream_id) DO UPDATE SET
		   version = excluded.version, state = excluded.state,
		   updated_at = CURRENT_TIMESTAMP`,
		streamID, version, state)
	return err
}

// LoadSnapshot returns the latest snapshot for the stream (ok=false if none).
func (s *EventStore) LoadSnapshot(streamID string) (state []byte, version int, ok bool, err error) {
	err = s.db.QueryRow(
		`SELECT state, version FROM snapshots WHERE stream_id = ?`,
		streamID).Scan(&state, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	return state, version, true, nil
}
