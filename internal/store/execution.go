package store

import (
	"database/sql"
	"fmt"

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
	Status         string
	StatusContext  string
	Phase          string
}

// UpsertInit records an execution and its changed subgraph from an Init event.
// Re-init with the same id is upsert-safe. The phase column is intentionally
// left untouched: the lifecycle phase is owned by UpsertPhase, which may run
// before Init.
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
			   project=excluded.project, status=excluded.status`,
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
		        COALESCE(status,''), COALESCE(status_context,''), COALESCE(phase,'')
		 FROM executions WHERE id = ?`, id).
		Scan(&e.ID, &e.Repo, &e.SHA, &e.PR, &e.Environment, &e.CheckRunID,
			&e.Rev, &e.ReportMarkdown, &e.LogURL, &e.Status, &e.StatusContext, &e.Phase)
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
	var g events.Graph
	rows, err := db.Query(
		`SELECT stack_path, COALESCE(project,''), COALESCE(status,''), COALESCE(detail,'')
		 FROM stacks WHERE execution_id = ? ORDER BY stack_path`, id)
	if err != nil {
		return g, err
	}
	defer rows.Close()
	for rows.Next() {
		var s events.StackState
		var st string
		if err := rows.Scan(&s.Path, &s.Project, &st, &s.Detail); err != nil {
			return g, err
		}
		s.Status = events.Status(st)
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

// SetCheckRunID records the GitHub check-run id created for an execution.
func SetCheckRunID(db *sql.DB, id string, checkRunID int64) error {
	_, err := db.Exec(`UPDATE executions SET check_run_id = ? WHERE id = ?`, checkRunID, id)
	return err
}
