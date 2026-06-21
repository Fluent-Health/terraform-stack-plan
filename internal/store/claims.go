package store

import (
	"database/sql"
	"time"
)

// ClaimStacks records that PR `pr` (apply exec `execID`, may be "") holds the
// per-stack lock claim for `stacks` in `env` until `expiresAt`. Idempotent:
// re-claiming the same stacks for the same PR refreshes the row.
func ClaimStacks(db *sql.DB, env string, pr int, execID string, stacks []string, expiresAt time.Time) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, s := range stacks {
		if _, err := tx.Exec(
			`INSERT INTO apply_claims (environment, stack_path, owner_pr, execution_id, expires_at)
			 VALUES (?,?,?,?,?)
			 ON CONFLICT(environment, stack_path) DO UPDATE SET
			   owner_pr=excluded.owner_pr, execution_id=excluded.execution_id, expires_at=excluded.expires_at`,
			env, s, pr, nullStr(execID), expiresAt.UTC()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ClaimedStacks returns the active (unexpired at `now`) claims for env as
// stack_path → owner_pr.
func ClaimedStacks(db *sql.DB, env string, now time.Time) (map[string]int, error) {
	rows, err := db.Query(
		`SELECT stack_path, owner_pr FROM apply_claims WHERE environment = ? AND expires_at > ?`,
		env, now.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var s string
		var pr int
		if err := rows.Scan(&s, &pr); err != nil {
			return nil, err
		}
		out[s] = pr
	}
	return out, rows.Err()
}

// ReleaseClaimsByPREnv drops all claims a PR holds in an env.
func ReleaseClaimsByPREnv(db *sql.DB, env string, pr int) error {
	_, err := db.Exec(`DELETE FROM apply_claims WHERE environment = ? AND owner_pr = ?`, env, pr)
	return err
}

// RenewClaims extends the lease for a PR's claims in an env (heartbeat).
func RenewClaims(db *sql.DB, env string, pr int, expiresAt time.Time) error {
	_, err := db.Exec(
		`UPDATE apply_claims SET expires_at = ? WHERE environment = ? AND owner_pr = ?`,
		expiresAt.UTC(), env, pr)
	return err
}

// AssociateClaimExecution links a PR's claims to the apply execution that now
// owns them (so heartbeats/finalize can target by pr+env).
func AssociateClaimExecution(db *sql.DB, env string, pr int, execID string) error {
	_, err := db.Exec(
		`UPDATE apply_claims SET execution_id = ? WHERE environment = ? AND owner_pr = ?`,
		execID, env, pr)
	return err
}

// SweepExpiredClaims deletes all claims expired at `now` and returns the
// distinct environments that lost a claim (so the caller can re-evaluate their
// held checks).
func SweepExpiredClaims(db *sql.DB, now time.Time) ([]string, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	utcNow := now.UTC()
	rows, err := tx.Query(
		`SELECT DISTINCT environment FROM apply_claims WHERE expires_at <= ?`, utcNow)
	if err != nil {
		return nil, err
	}
	var envs []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			rows.Close()
			return nil, err
		}
		envs = append(envs, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM apply_claims WHERE expires_at <= ?`, utcNow); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return envs, nil
}

// Claim is one row from the apply_claims table, returned by ListClaims.
type Claim struct {
	Environment string
	StackPath   string
	OwnerPR     int
	ExpiresAt   time.Time
}

// ListClaims returns all claims for env (any expiry) ordered by stack_path.
func ListClaims(db *sql.DB, env string) ([]Claim, error) {
	rows, err := db.Query(
		`SELECT environment, stack_path, owner_pr, expires_at FROM apply_claims
		 WHERE environment = ? ORDER BY stack_path`,
		env)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Claim
	for rows.Next() {
		var c Claim
		if err := rows.Scan(&c.Environment, &c.StackPath, &c.OwnerPR, &c.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ReleaseClaimStack deletes the claim for a single stack owned by pr in env.
func ReleaseClaimStack(db *sql.DB, env string, pr int, stack string) error {
	_, err := db.Exec(
		`DELETE FROM apply_claims WHERE environment = ? AND owner_pr = ? AND stack_path = ?`,
		env, pr, stack)
	return err
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
