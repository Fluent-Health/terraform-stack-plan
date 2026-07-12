package store

import (
	"database/sql"
	"fmt"
	"time"
)

// PRMeta is a row of the pr_meta table: PR-level identity (title, author,
// branch, automerge) as reported by the GitHub webhook, keyed by (repo, pr).
type PRMeta struct {
	Repo        string
	PR          int
	Title       string
	Body        string
	AuthorLogin string
	HeadRef     string
	URL         string
	AutoMerge   bool
	UpdatedAt   time.Time
}

// UpsertPRMeta records the PR's current metadata, overwriting any previous
// row for (repo, pr). Safe to call repeatedly as the webhook re-delivers or
// the PR is edited.
func UpsertPRMeta(db *sql.DB, m PRMeta) error {
	_, err := db.Exec(
		`INSERT INTO pr_meta (repo, pr, title, body, author_login, head_ref, url, auto_merge)
		 VALUES (?,?,?,?,?,?,?,?)
		 ON CONFLICT(repo, pr) DO UPDATE SET
		   title=excluded.title, body=excluded.body, author_login=excluded.author_login,
		   head_ref=excluded.head_ref, url=excluded.url, auto_merge=excluded.auto_merge,
		   updated_at=CURRENT_TIMESTAMP`,
		m.Repo, m.PR, m.Title, m.Body, m.AuthorLogin, m.HeadRef, m.URL, m.AutoMerge)
	if err != nil {
		return fmt.Errorf("upsert pr_meta: %w", err)
	}
	return nil
}

// GetPRMeta loads the metadata row for (repo, pr). ok is false when no row
// exists (absent is not an error).
func GetPRMeta(db *sql.DB, repo string, pr int) (PRMeta, bool, error) {
	var m PRMeta
	err := db.QueryRow(
		`SELECT repo, pr, title, body, author_login, head_ref, url, auto_merge, updated_at
		 FROM pr_meta WHERE repo = ? AND pr = ?`, repo, pr).
		Scan(&m.Repo, &m.PR, &m.Title, &m.Body, &m.AuthorLogin, &m.HeadRef, &m.URL, &m.AutoMerge, &m.UpdatedAt)
	if err == sql.ErrNoRows {
		return PRMeta{}, false, nil
	}
	if err != nil {
		return PRMeta{}, false, fmt.Errorf("get pr_meta: %w", err)
	}
	return m, true, nil
}
