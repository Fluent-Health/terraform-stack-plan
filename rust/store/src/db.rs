//! Opening the database and applying the migrations.

use std::path::Path;
use std::sync::{Mutex, MutexGuard, PoisonError};
use std::time::Duration;

use rusqlite::{Connection, TransactionBehavior};

use crate::{Error, Result};

/// The goose migrations Go embeds, in version order, compiled into the binary from
/// `internal/store/migrations` so both binaries apply the same SQL.
macro_rules! migration {
    ($version:literal, $file:literal) => {
        (
            $version,
            include_str!(concat!("../../../internal/store/migrations/", $file)),
        )
    };
}
pub(crate) const MIGRATIONS: &[(i64, &str)] = &[
    migration!(1, "001_init.sql"),
    migration!(2, "002_stack_outputs.sql"),
    migration!(3, "003_stack_categories.sql"),
    migration!(4, "004_gate_target_requester.sql"),
    migration!(5, "005_stack_counts.sql"),
    migration!(6, "006_apply_lock.sql"),
    migration!(7, "007_event_store.sql"),
    migration!(8, "008_event_store_cutover.sql"),
    migration!(9, "009_claim_ledger_cutover.sql"),
    migration!(10, "010_superseded_by.sql"),
    migration!(11, "011_applylock_execution.sql"),
    migration!(12, "012_pr_meta.sql"),
    migration!(13, "013_execution_progress.sql"),
    migration!(14, "014_change_reasons.sql"),
    migration!(15, "015_execution_phases.sql"),
];

/// goose's version table, named and shaped as goose creates it for SQLite.
const GOOSE_TABLE: &str = "CREATE TABLE goose_db_version (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		version_id INTEGER NOT NULL,
		is_applied INTEGER NOT NULL,
		tstamp TIMESTAMP DEFAULT (datetime('now'))
	)";

/// How long a write waits for another connection's lock: Go's `busy_timeout(5000)`,
/// polled every millisecond.
const BUSY_POLL: Duration = Duration::from_millis(1);
const BUSY_POLLS: i32 = 5000;

/// The SQLite database behind serve: one connection, used by one caller at a time.
///
/// Every write is an explicit `BEGIN IMMEDIATE` transaction, never an autocommit
/// statement. A deferred transaction that reads
/// before it writes (an append checks the stream version first) gets `SQLITE_BUSY`
/// at once when another connection commits in between, and the busy timeout never
/// applies to that lock upgrade. Taking the write lock up front puts the wait where
/// the busy timeout does apply. This is the Go store's `_txlock=immediate` fix for
/// a lost stack tick that left an apply hanging.
#[derive(Debug)]
pub struct Store {
    conn: Mutex<Connection>,
}

impl Store {
    /// Opens (creating if needed) the database at `path`, sets the busy timeout
    /// and WAL, and applies the migrations not yet recorded in `goose_db_version`.
    pub fn open(path: impl AsRef<Path>) -> Result<Store> {
        let mut conn = Connection::open(path)?;
        // The busy handler first, so switching the journal mode waits for a lock too.
        conn.busy_handler(Some(wait_for_lock))?;
        conn.query_row("PRAGMA journal_mode=WAL", [], |_| Ok(()))?;
        // The bundled SQLite enforces foreign keys by default; Go's driver does not,
        // and the schema's references (stacks and edges to executions) were never
        // enforced. Match Go so the two binaries accept the same writes.
        conn.pragma_update(None, "foreign_keys", false)?;
        migrate(&mut conn)?;
        Ok(Store {
            conn: Mutex::new(conn),
        })
    }

    /// The connection. A panic while it was held leaves nothing half-written (a
    /// transaction rolls back on drop), so a poisoned lock is still usable.
    pub(crate) fn conn(&self) -> MutexGuard<'_, Connection> {
        self.conn.lock().unwrap_or_else(PoisonError::into_inner)
    }

    /// Runs `f` in an immediate transaction and commits when it returns `Ok`.
    pub(crate) fn write<T>(
        &self,
        f: impl FnOnce(&rusqlite::Transaction<'_>) -> Result<T>,
    ) -> Result<T> {
        let mut conn = self.conn();
        let tx = conn.transaction_with_behavior(TransactionBehavior::Immediate)?;
        let out = f(&tx)?;
        tx.commit()?;
        Ok(out)
    }
}

/// SQLite's busy handler: called with the number of earlier calls for the same
/// lock, it sleeps and asks SQLite to retry, until about five seconds have passed.
///
/// SQLite's own busy timeout backs off to 100 ms between tries, so a connection
/// waiting on a busy writer rarely lands in the short gap between its
/// transactions and can starve past the timeout. Polling every millisecond keeps
/// the same five-second limit without that starvation.
#[expect(
    clippy::disallowed_methods,
    reason = "SQLite runs this on the thread that is already blocked on the lock; there is no async runtime here"
)]
fn wait_for_lock(polls: i32) -> bool {
    if polls >= BUSY_POLLS {
        return false;
    }
    std::thread::sleep(BUSY_POLL);
    true
}

/// Applies every migration newer than the database's goose version, each in its
/// own transaction together with its `goose_db_version` row, as goose does.
fn migrate(conn: &mut Connection) -> Result<()> {
    let current = goose_version(conn)?;
    for &(version, sql) in MIGRATIONS.iter().filter(|(v, _)| *v > current) {
        let tx = conn.transaction_with_behavior(TransactionBehavior::Immediate)?;
        tx.execute_batch(up_section(sql))
            .and_then(|()| {
                tx.execute(
                    "INSERT INTO goose_db_version (version_id, is_applied) VALUES (?1, 1)",
                    [version],
                )
            })
            .map_err(|source| Error::Migration { version, source })?;
        tx.commit()?;
    }
    Ok(())
}

/// The database's migration version, read the way goose reads it: the newest row
/// per version decides whether that version is applied. A new database gets the
/// table and goose's initial version 0.
fn goose_version(conn: &Connection) -> Result<i64> {
    let exists: bool = conn.query_row(
        "SELECT EXISTS (SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'goose_db_version')",
        [],
        |r| r.get(0),
    )?;
    if !exists {
        conn.execute_batch(GOOSE_TABLE)?;
        conn.execute(
            "INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, 1)",
            [],
        )?;
        return Ok(0);
    }
    let mut stmt =
        conn.prepare("SELECT version_id, is_applied FROM goose_db_version ORDER BY id DESC")?;
    let rows = stmt.query_map([], |r| Ok((r.get::<_, i64>(0)?, r.get::<_, bool>(1)?)))?;
    let mut rolled_back = Vec::new();
    for row in rows {
        let (version, applied) = row?;
        if rolled_back.contains(&version) {
            continue;
        }
        if applied {
            return Ok(version);
        }
        rolled_back.push(version);
    }
    Ok(0)
}

/// The SQL between `-- +goose Up` and `-- +goose Down`.
pub(crate) fn up_section(sql: &str) -> &str {
    let up = sql.split_once("-- +goose Up").map_or(sql, |(_, up)| up);
    up.split_once("-- +goose Down").map_or(up, |(up, _)| up)
}

#[cfg(test)]
pub(crate) mod tests;
