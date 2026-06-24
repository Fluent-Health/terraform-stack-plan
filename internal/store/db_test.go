package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// newTestDB opens a fresh migrated SQLite database in a temp dir.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenRunsMigrations(t *testing.T) {
	db := newTestDB(t)
	// Each migrated table must be queryable.
	for _, table := range []string{"executions", "stacks", "edges", "gate_targets", "events", "snapshots"} {
		if _, err := db.Exec("SELECT count(*) FROM " + table); err != nil {
			t.Errorf("table %q not present: %v", table, err)
		}
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "test.db")
	db1, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db1.Close()
	// Re-opening an already-migrated database must succeed (goose is a no-op).
	db2, err := Open(dsn)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	db2.Close()
}
