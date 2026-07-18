package store

// gate.go is read/index support over the `gate_targets` PROJECTION. gate_targets
// is not an authoritative store: it is rebuilt from the folded gate event stream
// (`exec:<pr>:<env>`) exclusively by server/shell_save.go's project() — the upsert
// and DeleteTarget (the prune) below are that projection's only writers. Every other
// function here is a read (cross-PR indexes for the ops/approvals views). Never write
// gate_targets from a handler; add gate behavior as a reconcile decider transition
// whose fold project() rebuilds this table. (Verified: issue #227 workstream A4.)

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

// PendingApproval is one gate target awaiting human action, joined with the PR
// context a reviewer needs: the repo comes from the (pr, environment)'s latest
// execution ("" when none exists yet).
type PendingApproval struct {
	PR          int
	Environment string
	Repo        string
	Class       string
	Target      string
	GrantName   string
	State       string
	Requester   string
}

// PendingApprovals returns every gate target not yet ACTIVE, across all PRs and
// environments — the cross-PR approvals to-do list (same non-ACTIVE predicate as
// PendingGates, so DENIED/REVOKED targets surface too: they need attention, not
// approval). Returns a non-nil empty slice when none match.
func PendingApprovals(db *sql.DB) ([]PendingApproval, error) {
	rows, err := db.Query(
		`SELECT gt.pr, gt.environment,
		        COALESCE((SELECT e.repo FROM executions e
		                  WHERE e.pr = gt.pr AND e.environment = gt.environment
		                  ORDER BY e.created_at DESC, e.id DESC LIMIT 1), ''),
		        gt.class, gt.target, COALESCE(gt.grant_name,''), COALESCE(gt.state,''),
		        COALESCE(gt.requester,'')
		 FROM gate_targets gt
		 WHERE gt.state != 'ACTIVE'
		 ORDER BY gt.pr, gt.environment, gt.class, gt.target`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PendingApproval{}
	for rows.Next() {
		var p PendingApproval
		if err := rows.Scan(&p.PR, &p.Environment, &p.Repo, &p.Class, &p.Target,
			&p.GrantName, &p.State, &p.Requester); err != nil {
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
