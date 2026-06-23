package store

import (
	"database/sql"
	"errors"
	"sync"
)

// ErrConcurrencyConflict is returned by Append when the stream's current version
// is not the expected version — i.e. another writer advanced the stream since the
// caller last read it. The caller should reload and retry.
var ErrConcurrencyConflict = errors.New("eventstore: concurrency conflict")

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
