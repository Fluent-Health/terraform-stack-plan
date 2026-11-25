//! Opens a database the Go binary wrote and checks the Rust store reads it the
//! same way: no migration re-runs, every stored event and snapshot decodes and
//! re-encodes to the same JSON, every stream replays to the state Go snapshotted,
//! and rebuilding the projections from the log leaves Go's tables as they were.
//!
//! `fixtures/go.db` comes from the Go test `TestWriteRustCompatDB`
//! (`internal/server/rustcompat_test.go`), which drives Go's shell through every
//! aggregate. Regenerate it from the repository root with:
//!
//! ```sh
//! TFSTACKPLAN_RUST_COMPAT_DB=$PWD/rust/store/tests/fixtures/go.db \
//!   go test -count=1 -run TestWriteRustCompatDB ./internal/server/
//! ```
#![expect(
    clippy::expect_used,
    reason = "the helpers fail the test by panicking, as the test itself does"
)]

use std::path::{Path, PathBuf};

use rusqlite::Connection;
use rusqlite::types::Value as Sql;
use serde_json::Value;
use tfstackplan_gate::claims::{self, Claims};
use tfstackplan_gate::eventsourcing::Decider;
use tfstackplan_gate::execution::{self, Execution};
use tfstackplan_gate::reconcile::{self, Gate};
use tfstackplan_store::Store;

/// A copy of the Go database in a temp dir, so the test never writes the fixture.
fn go_db() -> (tempfile::TempDir, PathBuf) {
    let fixture = Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/go.db");
    let dir = tempfile::Builder::new()
        .prefix("tfsp-compat-")
        .tempdir()
        .expect("temp dir");
    let path = dir.path().join("go.db");
    std::fs::copy(&fixture, &path).expect("copy fixture");
    (dir, path)
}

/// Every row of a query, as SQL values.
fn rows(conn: &Connection, sql: &str) -> Vec<Vec<Sql>> {
    let mut stmt = conn.prepare(sql).expect(sql);
    let n = stmt.column_count();
    stmt.query_map([], |r| (0..n).map(|i| r.get::<_, Sql>(i)).collect())
        .expect(sql)
        .collect::<rusqlite::Result<_>>()
        .expect(sql)
}

/// The projection tables, without the columns only the clock writes
/// (`updated_at`, `created_at`, a phase row's `at`).
fn projections(path: &Path) -> Vec<(&'static str, Vec<Vec<Sql>>)> {
    let conn = Connection::open(path).expect("open");
    [
        (
            "gate_targets",
            "SELECT pr, environment, class, target, grant_name, state, requester FROM gate_targets ORDER BY 1, 2, 3, 4",
        ),
        (
            "apply_claims",
            "SELECT environment, stack_path, owner_pr, execution_id, expires_at FROM apply_claims ORDER BY 1, 2",
        ),
        (
            "executions",
            "SELECT id, repo, sha, pr, environment, status_context, log_url, phase, status, progress_label, progress_pct, superseded_by FROM executions ORDER BY 1",
        ),
        (
            "stacks",
            "SELECT execution_id, stack_path, project, status, detail, categories, counts FROM stacks ORDER BY 1, 2",
        ),
        (
            "edges",
            "SELECT execution_id, from_stack, to_stack FROM edges ORDER BY 1, 2, 3",
        ),
        (
            "execution_phases",
            "SELECT execution_id, phase, label, progress_pct FROM execution_phases ORDER BY execution_id, rowid",
        ),
    ]
    .into_iter()
    .map(|(table, sql)| (table, rows(&conn, sql)))
    .collect()
}

/// Whether two JSON values match, reading Go's `null` for a nil slice or map as
/// the empty array or object Rust writes.
fn same_json(go: &Value, rust: &Value) -> bool {
    match (go, rust) {
        (Value::Null, Value::Array(a)) => a.is_empty(),
        (Value::Null, Value::Object(o)) => o.is_empty(),
        (Value::Array(a), Value::Array(b)) => {
            a.len() == b.len() && a.iter().zip(b).all(|(x, y)| same_json(x, y))
        }
        (Value::Object(a), Value::Object(b)) => {
            a.len() == b.len()
                && a.iter()
                    .all(|(k, v)| b.get(k).is_some_and(|w| same_json(v, w)))
        }
        _ => go == rust,
    }
}

fn assert_same_json(what: &str, go: &[u8], rust: &[u8]) {
    let go: Value = serde_json::from_slice(go).expect("Go JSON");
    let rust: Value = serde_json::from_slice(rust).expect("Rust JSON");
    assert!(same_json(&go, &rust), "{what}:\n   go {go}\n rust {rust}");
}

/// Checks one stream: each event re-encodes to Go's JSON, the snapshot re-encodes
/// to Go's JSON, and replaying the log gives the state Go snapshotted.
fn check_stream<D>(store: &Store, stream: &str, patch: impl Fn(&mut D::State)) -> usize
where
    D: Decider,
    D::State: PartialEq + std::fmt::Debug,
{
    let (events, version) = store.load_events(stream).expect("events");
    for (i, se) in events.iter().enumerate() {
        let ev = D::unmarshal_event(&se.kind, &se.data)
            .unwrap_or_else(|e| panic!("{stream} v{}: {e}", i + 1));
        let (kind, data) = D::marshal_event(&ev).expect("marshal");
        assert_eq!(kind, se.kind, "{stream} v{}", i + 1);
        assert_same_json(&format!("{stream} v{}", i + 1), &se.data, &data);
    }
    let (snap, snap_version) = store
        .load_snapshot(stream)
        .expect("snapshot")
        .unwrap_or_else(|| panic!("{stream}: Go wrote no snapshot"));
    assert_eq!(snap_version, version, "{stream}: snapshot version");
    let go_state = D::unmarshal_snapshot(&snap).expect("snapshot decodes");
    assert_same_json(
        &format!("{stream} snapshot"),
        &snap,
        &D::marshal_snapshot(&go_state).expect("marshal snapshot"),
    );
    let (mut replayed, replay_version) = store.replay::<D>(stream).expect("replay");
    patch(&mut replayed);
    assert_eq!(replay_version, version);
    assert_eq!(
        replayed, go_state,
        "{stream}: replay differs from Go's fold"
    );
    events.len()
}

#[test]
fn opens_folds_and_reprojects_a_go_database() {
    let (_dir, path) = go_db();
    let before = projections(&path);
    let goose_rows = |p: &Path| -> i64 {
        Connection::open(p)
            .expect("open")
            .query_row("SELECT count(*) FROM goose_db_version", [], |r| r.get(0))
            .expect("goose table")
    };
    let goose_before = goose_rows(&path);

    let store = Store::open(&path).expect("Rust opens the Go database");
    assert_eq!(goose_rows(&path), goose_before, "a migration ran again");

    let mut checked = 0;
    for stream in store.stream_ids("exec:").expect("streams") {
        let (pr, env) = stream
            .strip_prefix("exec:")
            .and_then(|rest| rest.split_once(':'))
            .expect("exec:<pr>:<env>");
        let pr: i64 = pr.parse().expect("pr");
        checked += check_stream::<Gate>(&store, &stream, |cs: &mut reconcile::ChangeSet| {
            // The gate's events do not carry their key; the shell sets it.
            cs.pr = pr;
            cs.environment = env.to_string();
        });
    }
    for stream in store.stream_ids("run:").expect("streams") {
        checked += check_stream::<Execution>(&store, &stream, |_: &mut execution::State| {});
    }
    for stream in store.stream_ids("env:").expect("streams") {
        checked += check_stream::<Claims>(&store, &stream, |_: &mut claims::ClaimSet| {});
    }
    let (total,): (i64,) = Connection::open(&path)
        .expect("open")
        .query_row("SELECT count(*) FROM events", [], |r| Ok((r.get(0)?,)))
        .expect("count");
    assert_eq!(
        i64::try_from(checked).expect("count"),
        total,
        "every event checked"
    );

    store.rebuild_all().expect("rebuild");
    drop(store);
    let after = projections(&path);
    for ((table, go), (_, rust)) in before.iter().zip(&after) {
        assert!(
            !go.is_empty(),
            "{table}: the fixture has no rows to compare"
        );
        assert_eq!(go.len(), rust.len(), "{table}: row count");
        for (g, r) in go.iter().zip(rust) {
            assert!(
                g == r || go_history(table, g, r),
                "{table}: Rust's projection differs from Go's\n   go {g:?}\n rust {r:?}"
            );
        }
    }
}

/// Whether two rows differ only in a column Go writes from a round's events
/// rather than from any fold, which no replay can rebuild:
/// - `executions.status` of a serve-started run: the round whose start failed
///   marks it `failure`, and a later round on the execution's own stream (a
///   supersede) re-projects it as `in_progress`. Which came last is in neither log.
/// - `stacks.status` left `gated` or `safe`: a released gate stops writing the
///   overlay, so its last value stays on the stacks.
fn go_history(table: &str, go: &[Sql], rust: &[Sql]) -> bool {
    let col = match table {
        "executions" => 8,
        "stacks" => 3,
        _ => return false,
    };
    let text = |v: &Sql| match v {
        Sql::Text(s) => s.clone(),
        _ => String::new(),
    };
    let others_equal = go
        .iter()
        .zip(rust)
        .enumerate()
        .all(|(i, (g, r))| i == col || g == r);
    let allowed = match table {
        "executions" => {
            text(&go[0]).starts_with("run-")
                && [text(&go[col]), text(&rust[col])].contains(&"failure".to_string())
        }
        _ => ["gated", "safe"].contains(&text(&go[col]).as_str()),
    };
    others_equal && allowed
}
