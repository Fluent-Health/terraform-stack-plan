// Package store is the server's SQLite persistence layer: executions, their
// stack/edge subgraph, and per-(class,target) approval gate state. Pure-Go
// SQLite (modernc.org/sqlite, no cgo); schema migrated by goose at Open.
package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/store/migrations"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// Open opens the SQLite database at dsn, enables WAL, and applies all
// migrations. dsn is a modernc.org/sqlite DSN, e.g. a file path or
// "file:/data/server.db".
func Open(dsn string) (*sql.DB, error) {
	// Inject per-connection PRAGMAs via the DSN so they apply to every connection
	// in the pool. busy_timeout lets concurrent goroutines (e.g. the reconcile
	// loop) retry on SQLITE_BUSY instead of failing immediately.
	dsnWithPragmas := pragmaDSN(dsn, "journal_mode(WAL)", "busy_timeout(5000)")
	db, err := sql.Open("sqlite", dsnWithPragmas)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(1)
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

// pragmaDSN appends _pragma=<p> query parameters to dsn so that modernc.org/sqlite
// applies them on every new connection. Works for plain file paths and file: URIs.
func pragmaDSN(dsn string, pragmas ...string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	} else if !strings.HasPrefix(dsn, "file:") {
		// Convert bare path to a file URI so we can append query parameters.
		dsn = "file:" + dsn
	}
	for _, p := range pragmas {
		dsn += sep + "_pragma=" + p
		sep = "&"
	}
	return dsn
}
