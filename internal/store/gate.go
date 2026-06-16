package store

import "database/sql"

// GateTarget holds the per-(class,target) grant state recorded for a (pr,
// environment).
type GateTarget struct {
	Class     string
	Target    string
	GrantName string
	State     string
	Requester string
}

// Gate identifies the unit an approval verdict is posted for.
type Gate struct {
	PR          int
	Environment string
}

// UpsertTarget records or updates the grant for a (pr, environment, class,
// target). On conflict the grant_name, state, and updated_at are overwritten.
// NOTE: `requester` is deliberately excluded from the ON CONFLICT update set.
// SetTargetRequester writes the leased requester SA after the initial upsert;
// subsequent reconcile-loop UpsertTarget calls (state refreshes) must not
// clobber it — keeping it out of the update set is the invariant that lets
// handleGateCheck trust targets[0].Requester for the entire (pr, environment).
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
		`SELECT class, target, COALESCE(grant_name,''), COALESCE(state,''), COALESCE(requester,'')
		 FROM gate_targets WHERE pr = ? AND environment = ?`, pr, environment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GateTarget{}
	for rows.Next() {
		var t GateTarget
		if err := rows.Scan(&t.Class, &t.Target, &t.GrantName, &t.State, &t.Requester); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RawChangeSet is the persisted state for (pr, environment): the classified
// marker and gate targets. It is the store-level shape the server maps into
// reconcile.ChangeSet.
type RawChangeSet struct {
	PR          int
	Environment string
	Classified  bool
	Targets     []GateTarget
}

// LoadChangeSet reads the classified marker and gate targets for (pr, env).
func LoadChangeSet(db *sql.DB, pr int, environment string) (RawChangeSet, error) {
	classified, err := IsClassified(db, pr, environment)
	if err != nil {
		return RawChangeSet{}, err
	}
	targets, err := TargetsFor(db, pr, environment)
	if err != nil {
		return RawChangeSet{}, err
	}
	return RawChangeSet{PR: pr, Environment: environment, Classified: classified, Targets: targets}, nil
}

// SetTargetRequester records the leased requester SA for every gate target of a
// (pr, environment). Idempotent; no-op if no rows match.
func SetTargetRequester(db *sql.DB, pr int, environment, requester string) error {
	_, err := db.Exec(
		`UPDATE gate_targets SET requester = ?, updated_at = CURRENT_TIMESTAMP
		   WHERE pr = ? AND environment = ?`,
		requester, pr, environment)
	return err
}

// OpenGrantPR is a PR that still holds at least one open (non-terminal) grant,
// paired with the repo of its latest execution (for the orphan sweep's GitHub
// closed-state check). Repo is "" when no execution row exists.
type OpenGrantPR struct {
	PR   int
	Repo string
}

// OpenGrantPRs returns every PR with at least one gate target in an open state
// (AWAITING/ACTIVATING/ACTIVE), each paired with its latest execution's repo.
// Unlike PendingGates it INCLUDES fully-ACTIVE (Satisfied) gates — the merged-PR
// orphan whose grants were never revoked — which is exactly what the sweep must
// re-examine. Terminal states (DENIED/REVOKED/EXPIRED) are deliberately excluded:
// those grants are no longer live, so the sweep has nothing to revoke for them
// (an EXPIRED grant the reconciler may later re-arm becomes AWAITING again — open
// — and is caught on the next sweep). A PR is one row regardless of how many
// environments hold open grants (GROUP BY pr); revokeOrphans handles every env.
func OpenGrantPRs(db *sql.DB) ([]OpenGrantPR, error) {
	rows, err := db.Query(
		`SELECT gt.pr,
		        COALESCE((SELECT e.repo FROM executions e
		                  WHERE e.pr = gt.pr ORDER BY e.created_at DESC, e.id DESC LIMIT 1), '')
		 FROM gate_targets gt
		 WHERE gt.state IN ('AWAITING','ACTIVATING','ACTIVE')
		 GROUP BY gt.pr
		 ORDER BY gt.pr`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OpenGrantPR{}
	for rows.Next() {
		var p OpenGrantPR
		if err := rows.Scan(&p.PR, &p.Repo); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteTarget removes a single gate target row.
func DeleteTarget(tx *sql.Tx, pr int, environment, class, target string) error {
	_, err := tx.Exec(
		`DELETE FROM gate_targets WHERE pr=? AND environment=? AND class=? AND target=?`,
		pr, environment, class, target)
	return err
}

// PRTarget is a (environment, class, target) tuple recorded for a PR, used by
// the PR-closed webhook to revoke orphaned grants across all environments.
type PRTarget struct {
	Environment string
	Class       string
	Target      string
}

// PRTargets returns every (environment, class, target) recorded for pr across
// all environments. Returns a non-nil empty slice when none match.
func PRTargets(db *sql.DB, pr int) ([]PRTarget, error) {
	rows, err := db.Query(
		`SELECT DISTINCT environment, class, target FROM gate_targets WHERE pr = ?`, pr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PRTarget{}
	for rows.Next() {
		var t PRTarget
		if err := rows.Scan(&t.Environment, &t.Class, &t.Target); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
