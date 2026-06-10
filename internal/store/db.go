// Package store is the server's SQLite persistence layer: executions, their
// stack/edge subgraph, and per-(class,target) approval gate state. Pure-Go
// SQLite (modernc.org/sqlite, no cgo); schema migrated by goose at Open.
package store

import (
	"database/sql"
	"fmt"

	"github.com/Fluent-Health/terraform-stack-plan/internal/store/migrations"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// Open opens the SQLite database at dsn, enables WAL, and applies all
// migrations. dsn is a modernc.org/sqlite DSN, e.g. a file path or
// "file:/data/server.db".
func Open(dsn string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL: %w", err)
	}
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite"); err != nil {
		db.Close()
		return nil, err
	}
	if err := goose.Up(db, "."); err != nil {
		db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}
	return db, nil
}
