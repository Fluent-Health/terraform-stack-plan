package store

import "database/sql"

// IsQuiescent reports whether no PR is in flight: no gate_targets row is in a
// non-terminal state (anything other than DENIED/REVOKED/EXPIRED). Used to gate
// the reconciler_core cut-over so the engine swaps only between PRs.
func IsQuiescent(db *sql.DB) (bool, error) {
	var n int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM gate_targets
		   WHERE state NOT IN ('DENIED','REVOKED','EXPIRED')`).Scan(&n)
	if err != nil {
		return false, err
	}
	return n == 0, nil
}
