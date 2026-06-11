package store

import "database/sql"

// UpsertStackOutput records (or updates) the pointer + tail excerpt for one
// stack's output of a given kind ("log", later "plan"/"verify").
func UpsertStackOutput(db *sql.DB, execID, stack, kind, pointer, excerpt string) error {
	_, err := db.Exec(
		`INSERT INTO stack_outputs (execution_id, stack_path, kind, pointer, excerpt)
		 VALUES (?,?,?,?,?)
		 ON CONFLICT(execution_id, stack_path, kind) DO UPDATE SET
		   pointer    = excluded.pointer,
		   excerpt    = excluded.excerpt,
		   updated_at = CURRENT_TIMESTAMP`,
		execID, stack, kind, pointer, excerpt)
	return err
}

// GetStackOutput returns the stored pointer + excerpt for a stack's output kind.
// ok is false when none is recorded.
func GetStackOutput(db *sql.DB, execID, stack, kind string) (pointer, excerpt string, ok bool, err error) {
	err = db.QueryRow(
		`SELECT COALESCE(pointer,''), COALESCE(excerpt,'') FROM stack_outputs
		 WHERE execution_id = ? AND stack_path = ? AND kind = ?`,
		execID, stack, kind).Scan(&pointer, &excerpt)
	if err == sql.ErrNoRows {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return pointer, excerpt, true, nil
}
