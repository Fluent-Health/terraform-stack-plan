package store

import "database/sql"

// GateTarget holds the per-(class,target) grant state recorded for a (pr,
// environment).
type GateTarget struct {
	Class     string
	Target    string
	GrantName string
	State     string
}

// Gate identifies the unit an approval verdict is posted for.
type Gate struct {
	PR          int
	Environment string
}

// UpsertTarget records or updates the grant for a (pr, environment, class,
// target). On conflict the grant_name, state, and updated_at are overwritten.
func UpsertTarget(db *sql.DB, pr int, environment, class, target, grant, state string) error {
	_, err := db.Exec(
		`INSERT INTO gate_targets (pr, environment, class, target, grant_name, state)
		 VALUES (?,?,?,?,?,?)
		 ON CONFLICT(pr, environment, class, target) DO UPDATE SET
		   grant_name = excluded.grant_name,
		   state      = excluded.state,
		   updated_at = CURRENT_TIMESTAMP`,
		pr, environment, class, target, grant, state)
	return err
}

// PendingGates returns the (pr, environment) gates that still have at least one
// target not yet ACTIVE — i.e. gates whose verdict may still need posting. The
// reconcile loop walks these to self-heal the gap where an approval event is
// processed while the grant is still activating (some providers fire no further
// event for the activating→active transition) and dropped events / restarts.
func PendingGates(db *sql.DB) ([]Gate, error) {
	rows, err := db.Query(
		`SELECT pr, environment FROM gate_targets
		 GROUP BY pr, environment
		 HAVING SUM(CASE WHEN state = 'ACTIVE' THEN 1 ELSE 0 END) < COUNT(*)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Gate{}
	for rows.Next() {
		var g Gate
		if err := rows.Scan(&g.PR, &g.Environment); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// MarkActive flips every stored target for (pr, environment) to ACTIVE, so the
// gate drops out of PendingGates and the live approval panel shows approved
// rather than the stale request-time waiting state.
func MarkActive(db *sql.DB, pr int, environment string) error {
	_, err := db.Exec(
		`UPDATE gate_targets SET state = 'ACTIVE', updated_at = CURRENT_TIMESTAMP
		 WHERE pr = ? AND environment = ?`, pr, environment)
	return err
}

// MarkClassified records that (pr, environment) was classified by a plan, even
// when the plan was clean (zero gate_targets). Idempotent.
func MarkClassified(db *sql.DB, pr int, environment string) error {
	_, err := db.Exec(
		`INSERT INTO gate_runs (pr, environment) VALUES (?, ?)
		 ON CONFLICT(pr, environment) DO UPDATE SET classified_at = CURRENT_TIMESTAMP`,
		pr, environment)
	return err
}

// IsClassified reports whether a plan ever classified (pr, environment).
func IsClassified(db *sql.DB, pr int, environment string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM gate_runs WHERE pr = ? AND environment = ?`,
		pr, environment).Scan(&n)
	return n > 0, err
}

// TargetsFor returns every GateTarget recorded for (pr, environment). Returns a
// non-nil empty slice when none match.
func TargetsFor(db *sql.DB, pr int, environment string) ([]GateTarget, error) {
	rows, err := db.Query(
		`SELECT class, target, COALESCE(grant_name,''), COALESCE(state,'')
		 FROM gate_targets WHERE pr = ? AND environment = ?`, pr, environment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GateTarget{}
	for rows.Next() {
		var t GateTarget
		if err := rows.Scan(&t.Class, &t.Target, &t.GrantName, &t.State); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
