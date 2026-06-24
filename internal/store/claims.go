package store

import (
	"database/sql"
	"time"
)

// ReplaceClaims rewrites the apply_claims projection for env to exactly match
// `rows` (stack_path → {owner_pr, expires_at}). Upserts each row and deletes any
// existing rows for env not present in the new set. This is the projector seam:
// the env:<env> event stream is the source of truth, apply_claims a derived
// cross-env index for the sweep + the live-UI list. Atomic in one tx.
func ReplaceClaims(db *sql.DB, env string, rows map[string]Claim) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	keep := make(map[string]bool, len(rows))
	for stack, c := range rows {
		keep[stack] = true
		if _, err := tx.Exec(
			`INSERT INTO apply_claims (environment, stack_path, owner_pr, execution_id, expires_at)
			 VALUES (?,?,?,?,?)
			 ON CONFLICT(environment, stack_path) DO UPDATE SET
			   owner_pr=excluded.owner_pr, expires_at=excluded.expires_at`,
			env, stack, c.OwnerPR, nil, c.ExpiresAt.UTC()); err != nil {
			return err
		}
	}
	// Delete projection rows for env that the folded set no longer contains.
	existing, err := tx.Query(`SELECT stack_path FROM apply_claims WHERE environment = ?`, env)
	if err != nil {
		return err
	}
	var drop []string
	for existing.Next() {
		var s string
		if err := existing.Scan(&s); err != nil {
			existing.Close()
			return err
		}
		if !keep[s] {
			drop = append(drop, s)
		}
	}
	existing.Close()
	if err := existing.Err(); err != nil {
		return err
	}
	for _, s := range drop {
		if _, err := tx.Exec(
			`DELETE FROM apply_claims WHERE environment = ? AND stack_path = ?`, env, s); err != nil {
			return err
		}
	}
	return tx.Commit()
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
