//! The SQLite store behind `tfstackplan serve`, ported from Go's `internal/store`
//! and the persistence half of `internal/server`'s shell.
//!
//! The event log is the source of truth. Each aggregate of
//! [`tfstackplan_gate`] is one stream per scope: the gate on `exec:<pr>:<env>`,
//! an execution on `run:<execID>`, a claim ledger on `env:<env>`. [`Store::load`]
//! folds a stream from its latest snapshot, [`Store::append`] adds events with an
//! optimistic version check, and the `step_*` methods run one decide, fold,
//! persist and project round. The tables `gate_targets`, `executions`, `stacks`,
//! `edges`, `execution_phases` and `apply_claims` are projections rebuilt from the
//! folds, never written by hand.
//!
//! The database is the one the Go binary writes: the same goose migrations, the
//! same `goose_db_version` table, and the same JSON in `events` and `snapshots`.
//!
//! ```
//! use tfstackplan_gate::reconcile::{GateState, RunnerFinalize};
//! use tfstackplan_store::{KeyedLocks, Store};
//!
//! # let dir = tempfile::Builder::new().tempdir()?;
//! let store = Store::open(dir.path().join("serve.db"))?;
//! let locks = KeyedLocks::default();
//! let step = locks.with((7, "staging"), || {
//!     store.step_gate(7, "staging", &RunnerFinalize::default().into())
//! })?;
//! assert_eq!(step.state.gate, GateState::Clean);
//! # Ok::<(), Box<dyn std::error::Error>>(())
//! ```
#![forbid(unsafe_code)]

mod claims;
mod db;
mod eventstore;
mod execution;
mod gate;
mod locks;
mod queries;
mod steps;
mod time;

pub use claims::{ClaimRow, project_claims_rows};
pub use db::Store;
pub use eventstore::StoredEvent;
pub use execution::{ExecutionRow, PhaseRow, ProjectedExecution, ProjectedStack};
pub use gate::{GateKey, GateTargetRow, OpenGrantPr, PendingApproval, PrTarget};
pub use locks::KeyedLocks;
pub use queries::{ApplyLockCheck, ExecutionSummary, PrMeta};
pub use steps::{GateStep, env_stream_id, exec_stream_id, run_stream_id};

use tfstackplan_gate::eventsourcing::CodecError;

/// A store failure.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// The stream's version is not the one the caller read: another writer
    /// advanced it. Reload and retry.
    #[error("eventstore: concurrency conflict")]
    ConcurrencyConflict,
    #[error(transparent)]
    Sqlite(#[from] rusqlite::Error),
    #[error(transparent)]
    Codec(#[from] CodecError),
    #[error(transparent)]
    Json(#[from] serde_json::Error),
    /// A migration failed to apply.
    #[error("run migration {version}: {source}")]
    Migration {
        version: i64,
        source: rusqlite::Error,
    },
    /// A `stacks.status` value outside the known set.
    #[error(transparent)]
    UnknownStatus(#[from] tfstackplan_gate::events::UnknownStatus),
    /// A timestamp column held a value that is not a time.
    #[error("parse time {0:?}")]
    Time(String),
}

/// The store's result type.
pub type Result<T, E = Error> = std::result::Result<T, E>;
