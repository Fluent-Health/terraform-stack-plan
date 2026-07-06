package store

import (
	"database/sql"
	"encoding/json"
)

// ApplyLockCheck is a persisted apply-lock check run (so serve can PATCH it
// later when an overlapping apply releases). Keyed by (environment, head_sha).
type ApplyLockCheck struct {
	Environment string
	HeadSHA     string
	CheckRunID  int64
	PR          int
	Repo        string
	Stacks      []string
	State       string // clear | held | unverifiable
	Kind        string // merge_group | pr_head
	ExecutionID string // plan execution whose check run carries the consolidated render; "" for legacy/merge-group records
}

func UpsertApplyLockCheck(db *sql.DB, c ApplyLockCheck) error {
	stacks, err := json.Marshal(c.Stacks)
	if err != nil {
		return err
	}
	_, err = db.Exec(
		`INSERT INTO applylock_checks (environment, head_sha, check_run_id, pr, repo, stacks, state, kind, execution_id, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP)
		 ON CONFLICT(environment, head_sha) DO UPDATE SET
		   check_run_id=excluded.check_run_id, pr=excluded.pr, repo=excluded.repo, stacks=excluded.stacks,
		   state=excluded.state, kind=excluded.kind, execution_id=excluded.execution_id, updated_at=CURRENT_TIMESTAMP`,
		c.Environment, c.HeadSHA, c.CheckRunID, c.PR, c.Repo, string(stacks), c.State, c.Kind, c.ExecutionID)
	return err
}

func GetApplyLockCheck(db *sql.DB, env, headSHA string) (ApplyLockCheck, bool, error) {
	var c ApplyLockCheck
	var stacks string
	err := db.QueryRow(
		`SELECT environment, head_sha, check_run_id, pr, repo, stacks, state, kind, execution_id
		 FROM applylock_checks WHERE environment = ? AND head_sha = ?`, env, headSHA).
		Scan(&c.Environment, &c.HeadSHA, &c.CheckRunID, &c.PR, &c.Repo, &stacks, &c.State, &c.Kind, &c.ExecutionID)
	if err == sql.ErrNoRows {
		return ApplyLockCheck{}, false, nil
	}
	if err != nil {
		return ApplyLockCheck{}, false, err
	}
	_ = json.Unmarshal([]byte(stacks), &c.Stacks)
	return c, true, nil
}

func HeldApplyLockChecks(db *sql.DB, env string) ([]ApplyLockCheck, error) {
	rows, err := db.Query(
		`SELECT environment, head_sha, check_run_id, pr, repo, stacks, state, kind, execution_id
		 FROM applylock_checks WHERE environment = ? AND state = 'held'`, env)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ApplyLockCheck
	for rows.Next() {
		var c ApplyLockCheck
		var stacks string
		if err := rows.Scan(&c.Environment, &c.HeadSHA, &c.CheckRunID, &c.PR, &c.Repo, &stacks, &c.State, &c.Kind, &c.ExecutionID); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(stacks), &c.Stacks)
		out = append(out, c)
	}
	return out, rows.Err()
}
