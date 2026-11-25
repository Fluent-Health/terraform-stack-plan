// Ported from internal/store/db_test.go, plus the goose table and the embedded
// migration list.
use std::collections::BTreeSet;
use std::path::PathBuf;

use tempfile::TempDir;

use super::*;

/// A fresh migrated database in a temp dir, removed with the dir.
pub(crate) fn temp_store() -> (TempDir, Store) {
    let dir = tempfile::Builder::new()
        .prefix("tfsp-store-")
        .tempdir()
        .expect("temp dir");
    let store = Store::open(dir.path().join("test.db")).expect("open");
    (dir, store)
}

#[test]
fn open_runs_migrations() {
    let (_dir, s) = temp_store();
    for table in [
        "executions",
        "stacks",
        "edges",
        "gate_targets",
        "events",
        "snapshots",
    ] {
        s.conn()
            .query_row(&format!("SELECT count(*) FROM {table}"), [], |_| Ok(()))
            .unwrap_or_else(|e| panic!("table {table}: {e}"));
    }
}

#[test]
fn open_is_idempotent() {
    let dir = tempfile::Builder::new()
        .prefix("tfsp-store-")
        .tempdir()
        .unwrap();
    let path = dir.path().join("test.db");
    drop(Store::open(&path).unwrap());
    let s = Store::open(&path).unwrap();
    // One goose row per migration plus goose's initial version 0: nothing re-ran.
    let rows: i64 = s
        .conn()
        .query_row("SELECT count(*) FROM goose_db_version", [], |r| r.get(0))
        .unwrap();
    assert_eq!(rows, i64::try_from(MIGRATIONS.len()).unwrap() + 1);
}

#[test]
fn goose_version_reads_the_newest_row_per_version() {
    let (_dir, s) = temp_store();
    let conn = s.conn();
    assert_eq!(goose_version(&conn).unwrap(), 15);
    // A rolled-back version (goose down) no longer counts.
    conn.execute(
        "INSERT INTO goose_db_version (version_id, is_applied) VALUES (15, 0)",
        [],
    )
    .unwrap();
    assert_eq!(goose_version(&conn).unwrap(), 14);
}

#[test]
fn embeds_every_migration_in_order() {
    let dir = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../internal/store/migrations");
    let on_disk: BTreeSet<String> = std::fs::read_dir(&dir)
        .unwrap()
        .filter_map(|e| e.ok()?.file_name().into_string().ok())
        .filter(|n| n.ends_with(".sql"))
        .collect();
    assert_eq!(on_disk.len(), MIGRATIONS.len(), "{on_disk:?}");
    for (name, (version, sql)) in on_disk.iter().zip(MIGRATIONS) {
        assert_eq!(name[..3].parse::<i64>().unwrap(), *version, "{name}");
        assert_eq!(
            std::fs::read_to_string(dir.join(name)).unwrap(),
            *sql,
            "{name}"
        );
        // Only plain Up/Down sections: no statement blocks or no-transaction
        // migrations, which this runner does not implement.
        for line in sql.lines().filter(|l| l.starts_with("-- +goose")) {
            assert!(
                line == "-- +goose Up" || line == "-- +goose Down",
                "{name}: {line}"
            );
        }
    }
}

#[test]
fn up_section_stops_at_down() {
    let sql = "-- +goose Up\nCREATE TABLE a (x);\n-- +goose Down\nDROP TABLE a;\n";
    assert_eq!(up_section(sql), "\nCREATE TABLE a (x);\n");
}

#[test]
fn foreign_keys_are_off_as_in_go() {
    let (_dir, s) = temp_store();
    let on: bool = s
        .conn()
        .query_row("PRAGMA foreign_keys", [], |r| r.get(0))
        .unwrap();
    assert!(!on);
}

/// A write on one connection waits for another connection's transaction instead
/// of failing with SQLITE_BUSY.
#[test]
fn a_writer_waits_for_another_connections_lock() {
    let (dir, s) = temp_store();
    let other = Store::open(dir.path().join("test.db")).unwrap();
    let (done_tx, done_rx) = std::sync::mpsc::channel();
    std::thread::scope(|scope| {
        let mut conn = s.conn();
        let tx = conn
            .transaction_with_behavior(TransactionBehavior::Immediate)
            .unwrap();
        tx.execute(
            "INSERT INTO execution_phases (execution_id, phase) VALUES ('a', 'x')",
            [],
        )
        .unwrap();
        scope.spawn(|| {
            let result = other.save_snapshot("s", 1, b"{}");
            done_tx.send(result).unwrap();
        });
        // The other writer is blocked while the transaction is open...
        assert!(done_rx.recv_timeout(Duration::from_millis(200)).is_err());
        tx.commit().unwrap();
        // ...and goes through once it commits.
        done_rx
            .recv_timeout(Duration::from_secs(5))
            .unwrap()
            .unwrap();
    });
}
