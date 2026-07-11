package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// Execution is a row of the executions table.
type Execution struct {
	ID             string
	Repo           string
	SHA            string
	PR             int
	Environment    string
	CheckRunID     sql.NullInt64
	Rev            int
	ReportMarkdown string
	LogURL         string
	Status         string // execution-level commit status (e.g. "in_progress"/"success"/"failure"); written by the serve handler, distinct from per-stack events.Status
	StatusContext  string
	Phase          string
	CreatedAt      time.Time
	SupersededBy   string
}

// UpsertInit records an execution and its changed subgraph from an Init event.
// Re-init with the same id is upsert-safe. The phase column is intentionally
// left untouched: the lifecycle phase is owned by UpsertPhase, which may run
// before Init. Stack status is set only on first insert; a repeat Init (e.g.
// run register followed by run plan) never regresses an already-advanced stack.
func UpsertInit(db *sql.DB, in events.Init) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT INTO executions (id, repo, sha, pr, environment, log_url, status_context)
		 VALUES (?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET repo=excluded.repo, sha=excluded.sha, pr=excluded.pr,
		   environment=excluded.environment, log_url=excluded.log_url,
		   status_context=excluded.status_context`,
		in.ID, in.Repo, in.SHA, in.PR, in.Environment, in.LogURL, in.Context); err != nil {
		return fmt.Errorf("insert execution: %w", err)
	}
	for _, s := range in.Stacks {
		status := s.Status
		if status == "" {
			status = events.StatusPending
		}
		if _, err := tx.Exec(
			`INSERT INTO stacks (execution_id, stack_path, project, status) VALUES (?,?,?,?)
			 ON CONFLICT(execution_id, stack_path) DO UPDATE SET
			   project=excluded.project`,
			in.ID, s.Path, s.Project, string(status)); err != nil {
			return fmt.Errorf("insert stack %q: %w", s.Path, err)
		}
	}
	for _, e := range in.Edges {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO edges (execution_id, from_stack, to_stack) VALUES (?,?,?)`,
			in.ID, e.From, e.To); err != nil {
			return fmt.Errorf("insert edge: %w", err)
		}
	}
	return tx.Commit()
}

// UpsertPhase sets the execution's lifecycle phase, creating a bare row if Init
// has not run yet. Identity fields are only overwritten when the event carries a
// non-zero value, so a bare phase bump never clobbers data set by an earlier
// Init (and an early phase event survives a later Init).
func UpsertPhase(db *sql.DB, p events.PhaseEvent) error {
	_, err := db.Exec(
		`INSERT INTO executions (id, repo, sha, pr, environment, log_url, status_context, phase)
		 VALUES (?,?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET phase=excluded.phase,
		   repo=CASE WHEN excluded.repo='' THEN executions.repo ELSE excluded.repo END,
		   sha=CASE WHEN excluded.sha='' THEN executions.sha ELSE excluded.sha END,
		   pr=CASE WHEN excluded.pr=0 THEN executions.pr ELSE excluded.pr END,
		   environment=CASE WHEN excluded.environment='' THEN executions.environment ELSE excluded.environment END,
		   status_context=CASE WHEN excluded.status_context='' THEN executions.status_context ELSE excluded.status_context END,
		   log_url=CASE WHEN excluded.log_url='' THEN executions.log_url ELSE excluded.log_url END`,
		p.ID, p.Repo, p.SHA, p.PR, p.Environment, p.LogURL, p.Context, string(p.Phase))
	return err
}

// GetExecution loads one execution row.
func GetExecution(db *sql.DB, id string) (Execution, error) {
	var e Execution
	err := db.QueryRow(
		`SELECT id, repo, sha, COALESCE(pr,0), COALESCE(environment,''), check_run_id,
                        rev, COALESCE(report_markdown,''), COALESCE(log_url,''),
                        COALESCE(status,''), COALESCE(status_context,''), COALESCE(phase,''), created_at,
                        COALESCE(superseded_by, '')
                 FROM executions WHERE id = ?`, id).
		Scan(&e.ID, &e.Repo, &e.SHA, &e.PR, &e.Environment, &e.CheckRunID,
			&e.Rev, &e.ReportMarkdown, &e.LogURL, &e.Status, &e.StatusContext, &e.Phase, &e.CreatedAt,
			&e.SupersededBy)
	if err != nil {
		return Execution{}, err
	}
	return e, nil
}

// UpdateStack ticks one stack's status (and optional failure detail).
func UpdateStack(db *sql.DB, id, stack string, status events.Status, detail string) error {
	_, err := db.Exec(
		`UPDATE stacks SET status = ?, detail = ? WHERE execution_id = ? AND stack_path = ?`,
		string(status), detail, id, stack)
	return err
}

// LoadGraph loads the stacks (ordered by path) and edges of an execution.
func LoadGraph(db *sql.DB, id string) (events.Graph, error) {
	// Non-nil slices always: a zero-stack execution (a change outside the
	// stack tree) must serialize as "stacks": [] — the API contract declares
	// arrays, and a nil slice marshals to null (it crashed the UI live).
	g := events.Graph{Stacks: []events.StackState{}, Edges: []events.Edge{}}
	rows, err := db.Query(
		`SELECT stack_path, COALESCE(project,''), COALESCE(status,''), COALESCE(detail,''), COALESCE(categories,''), COALESCE(counts,'')
		 FROM stacks WHERE execution_id = ? ORDER BY stack_path`, id)
	if err != nil {
		return g, err
	}
	defer rows.Close()
	for rows.Next() {
		var s events.StackState
		var st, cats, counts string
		if err := rows.Scan(&s.Path, &s.Project, &st, &s.Detail, &cats, &counts); err != nil {
			return g, err
		}
		s.Status = events.Status(st)
		if cats != "" {
			_ = json.Unmarshal([]byte(cats), &s.Categories)
		}
		if counts != "" {
			var c events.Counts
			if json.Unmarshal([]byte(counts), &c) == nil {
				s.Counts = &c
			}
		}
		g.Stacks = append(g.Stacks, s)
	}
	if err := rows.Err(); err != nil {
		return g, err
	}
	erows, err := db.Query(
		`SELECT from_stack, to_stack FROM edges WHERE execution_id = ? ORDER BY from_stack, to_stack`, id)
	if err != nil {
		return g, err
	}
	defer erows.Close()
	for erows.Next() {
		var e events.Edge
		if err := erows.Scan(&e.From, &e.To); err != nil {
			return g, err
		}
		g.Edges = append(g.Edges, e)
	}
	return g, erows.Err()
}

// SetReport stores the rendered plan report markdown for an execution.
func SetReport(db *sql.DB, id, markdown string) error {
	_, err := db.Exec(`UPDATE executions SET report_markdown = ? WHERE id = ?`, markdown, id)
	return err
}

// BumpRev increments the cache-bust revision (used by the rendered SVG URL so
// GitHub's image proxy re-fetches on each state change).
func BumpRev(db *sql.DB, id string) error {
	_, err := db.Exec(`UPDATE executions SET rev = rev + 1 WHERE id = ?`, id)
	return err
}

// SetExecutionStatus persists the execution-level commit status (e.g. terminal
// "success"/"failure"). This is the signal isFinished reads for an apply; the
// apply driver writes it once the apply concludes.
func SetExecutionStatus(db *sql.DB, id, status string) error {
	_, err := db.Exec(`UPDATE executions SET status = ? WHERE id = ?`, status, id)
	return err
}

// SetCheckRunID records the GitHub check-run id created for an execution.
func SetCheckRunID(db *sql.DB, id string, checkRunID int64) error {
	_, err := db.Exec(`UPDATE executions SET check_run_id = ? WHERE id = ?`, checkRunID, id)
	return err
}

// listExecutions scans execution list rows from a query returning, in order:
// id, repo, pr, environment, status, phase, created_at.
func listExecutions(rows *sql.Rows) ([]Execution, error) {
	defer rows.Close()
	var out []Execution
	for rows.Next() {
		var e Execution
		if err := rows.Scan(&e.ID, &e.Repo, &e.SHA, &e.PR, &e.Environment, &e.Status,
			&e.StatusContext, &e.Phase, &e.CreatedAt, &e.SupersededBy, &e.LogURL); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// listExecutionColumns is the column set the list queries share — everything a
// summary needs, deliberately excluding the heavyweight report_markdown.
const listExecutionColumns = `id, repo, COALESCE(sha,''), COALESCE(pr,0), COALESCE(environment,''),
	COALESCE(status,''), COALESCE(status_context,''), COALESCE(phase,''), created_at,
	COALESCE(superseded_by,''), COALESCE(log_url,'')`

// ListExecutions returns the most recent executions, newest first.
func ListExecutions(db *sql.DB, limit int) ([]Execution, error) {
	rows, err := db.Query(
		`SELECT `+listExecutionColumns+`
		 FROM executions ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return listExecutions(rows)
}

// ListExecutionsForPR returns all executions for a PR, newest first.
func ListExecutionsForPR(db *sql.DB, pr int) ([]Execution, error) {
	rows, err := db.Query(
		`SELECT `+listExecutionColumns+`
		 FROM executions WHERE pr = ? ORDER BY created_at DESC, id DESC`, pr)
	if err != nil {
		return nil, err
	}
	return listExecutions(rows)
}

// LatestExecutionID returns the most recent execution id for (pr, environment).
// ok is false when none exists.
func LatestExecutionID(db *sql.DB, pr int, environment string) (string, bool) {
	var id string
	err := db.QueryRow(
		`SELECT id FROM executions WHERE pr = ? AND environment = ?
		 ORDER BY created_at DESC, id DESC LIMIT 1`, pr, environment).Scan(&id)
	if err != nil {
		return "", false
	}
	return id, true
}

// EnvironmentsForPR returns the distinct environments that have at least one
// execution for the given PR. Order is unspecified.
func EnvironmentsForPR(db *sql.DB, pr int) ([]string, error) {
	rows, err := db.Query(
		`SELECT DISTINCT environment FROM executions WHERE pr = ? AND environment != ''`, pr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LatestVerifyExecutionID returns the most recent verify-context execution id for
// (pr, environment) — i.e. a run whose status_context begins with "verify". ok is
// false when none exists.
func LatestVerifyExecutionID(db *sql.DB, pr int, environment string) (string, bool) {
	var id string
	err := db.QueryRow(
		`SELECT id FROM executions
                 WHERE pr = ? AND environment = ? AND status_context LIKE 'verify%'
                 ORDER BY created_at DESC, id DESC LIMIT 1`, pr, environment).Scan(&id)
	if err != nil {
		return "", false
	}
	return id, true
}

// FindNonSupersededExecution looks up the most recent non-superseded execution for
// the given (pr, environment, sha, status_context) where pr > 0 and id != incoming id.
func FindNonSupersededExecution(db *sql.DB, pr int, environment, sha, statusContext, incomingID string) (string, bool, error) {
	if pr <= 0 {
		return "", false, nil
	}
	var id string
	err := db.QueryRow(
		`SELECT id FROM executions
                 WHERE pr = ? AND environment = ? AND sha = ? AND status_context = ?
                   AND id != ? AND (superseded_by IS NULL OR superseded_by = '')
                 ORDER BY created_at DESC, id DESC LIMIT 1`,
		pr, environment, sha, statusContext, incomingID,
	).Scan(&id)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

// SupersedeExecution links an old execution ID to its replacing execution ID.
func SupersedeExecution(db *sql.DB, oldID, newID string) error {
	_, err := db.Exec(
		`UPDATE executions SET superseded_by = ? WHERE id = ?`,
		newID, oldID,
	)
	return err
}

// FindExecutionBySHA returns the most recent non-superseded execution for
// (environment, status_context, sha) with pr > 0. The inbound-build correlation
// and the pr==0 runner-Init recovery both key on it: a plan head SHA is PR-unique,
// so this recovers the owning PR when a rerun lost its _PR_NUMBER.
func FindExecutionBySHA(db *sql.DB, environment, statusContext, sha string) (string, bool, error) {
	var id string
	err := db.QueryRow(
		`SELECT id FROM executions
                 WHERE environment = ? AND status_context = ? AND sha = ? AND COALESCE(pr,0) > 0
                   AND (superseded_by IS NULL OR superseded_by = '')
                 ORDER BY created_at DESC, id DESC LIMIT 1`,
		environment, statusContext, sha,
	).Scan(&id)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

// StuckPendingExecutions returns this environment's serve-initiated
// executions (id prefix per reconcile.RunExecutionIDPrefix) still in_progress
// with no runner activity at all (empty phase) created before cutoff — the
// start-watchdog's candidates. Runner-created executions and other tiers' rows
// are excluded in SQL so the watchdog never replays streams it cannot act on.
func StuckPendingExecutions(db *sql.DB, environment string, cutoff time.Time) ([]Execution, error) {
	rows, err := db.Query(
		`SELECT id, repo, sha, COALESCE(pr,0), COALESCE(environment,''), COALESCE(status,''),
		        COALESCE(status_context,''), COALESCE(phase,''), created_at
		   FROM executions
		  WHERE status = 'in_progress' AND COALESCE(phase,'') = ''
		    AND COALESCE(superseded_by,'') = ''
		    AND environment = ?
		    AND id LIKE 'run-%'
		    AND created_at < ?`, environment, cutoff.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Execution
	for rows.Next() {
		var e Execution
		if err := rows.Scan(&e.ID, &e.Repo, &e.SHA, &e.PR, &e.Environment, &e.Status,
			&e.StatusContext, &e.Phase, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LatestReportedExecutionID is LatestExecutionID restricted to executions the
// runner actually reported on (a phase tick or registered stacks) — a
// serve-queued row awaiting its build has neither and must not shadow the last
// real plan (the apply-lock evaluation reads the graph through this).
func LatestReportedExecutionID(db *sql.DB, pr int, environment string) (string, bool) {
	var id string
	err := db.QueryRow(
		`SELECT id FROM executions WHERE pr = ? AND environment = ?
		   AND (COALESCE(phase,'') != ''
		        OR EXISTS (SELECT 1 FROM stacks WHERE execution_id = executions.id))
		 ORDER BY created_at DESC, id DESC LIMIT 1`, pr, environment).Scan(&id)
	if err != nil {
		return "", false
	}
	return id, true
}

// LatestPRForSHA recovers the PR of the newest execution for (sha, env,
// context) — the check_run rerequested path for apply checks, whose webhook
// payload carries no pull_requests on merge commits.
func LatestPRForSHA(db *sql.DB, sha, environment, statusContext string) (int, bool) {
	var pr int
	err := db.QueryRow(
		`SELECT COALESCE(pr,0) FROM executions
		  WHERE sha = ? AND environment = ? AND COALESCE(status_context,'') = ? AND COALESCE(pr,0) > 0
		 ORDER BY created_at DESC, id DESC LIMIT 1`, sha, environment, statusContext).Scan(&pr)
	if err != nil || pr == 0 {
		return 0, false
	}
	return pr, true
}
