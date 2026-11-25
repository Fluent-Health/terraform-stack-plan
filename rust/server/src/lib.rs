//! The `tfstackplan serve` HTTP layer, ported from Go's `internal/server`.
//!
//! [`App`] is the control plane: it records multi-stack Terraform executions, drives
//! one GitHub check run per environment and projects approval-gate state, all as a
//! function of the SQLite store. The JSON surface is `api/openapi.yaml`; every
//! response's bytes match Go's (the wire goldens in `tests/wire.rs` pin them).
//!
//! The imperative shell is here: a signal for a (PR, environment) is decided, folded
//! and persisted by [`tfstackplan_store::Store::step_gate`], then its React actions
//! run, and their feedback signals loop to a fixpoint, all under that key's lock.
//! Store calls are synchronous and may sleep in SQLite's busy handler, so they run
//! on the blocking pool ([`App::db`]).
#![forbid(unsafe_code)]

use std::collections::HashMap;
use std::sync::{Arc, Mutex, PoisonError};

use futures_util::future::BoxFuture;
use jiff::Timestamp;
use tfstackplan_server_clients::approval::Backend as ApprovalBackend;
use tfstackplan_server_clients::executor::Backend as ExecutorBackend;
use tfstackplan_server_clients::github::GitHub;
use tfstackplan_server_clients::objects::ObjectStore;
use tfstackplan_store::Store;
use tokio::sync::{OwnedMutexGuard, mpsc};

mod admin;
mod applylock;
mod catalog;
mod checkrun;
pub mod config;
mod execution;
pub mod gojson;
mod http;
mod ingest;
mod lifecycle;
mod logs;
pub mod loops;
mod markdown;
mod pubsub;
mod read;
pub mod render;
mod routes;
pub mod serve;
#[cfg(test)]
mod testutil;
mod shell;
mod webhook;

pub use config::Config;

/// A server failure inside a handler or action.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    #[error(transparent)]
    Store(#[from] tfstackplan_store::Error),
    /// A blocking store task did not finish (the runtime is shutting down).
    #[error("store task: {0}")]
    Join(String),
    #[error("{0}")]
    Message(String),
}

/// The server's result type.
pub type Result<T, E = Error> = std::result::Result<T, E>;

/// Verifies a Google-signed OIDC bearer token and returns its email claim.
pub type Verifier = Arc<dyn Fn(String) -> BoxFuture<'static, Result<String, String>> + Send + Sync>;

/// The HTTP application. Build it with [`App::new`], set the optional backends,
/// then serve [`App::router`].
pub struct App {
    store: Arc<Store>,
    gh: Arc<dyn GitHub>,
    cfg: Config,
    hub: Hub,
    /// The approval backend; `None` disables gating.
    pub approval: Option<Arc<dyn ApprovalBackend>>,
    /// Completed-log offload; `None` keeps logs in the local buffer only.
    pub objects: Option<Arc<dyn ObjectStore>>,
    /// The CI backend serve drives itself; `None` keeps serve reactive-only.
    pub executor: Option<Arc<dyn ExecutorBackend>>,
    /// Pub/Sub push token verification; `None` disables `/pubsub/*`.
    pub push_verifier: Option<Verifier>,
    /// `/api/*` token verification; `None` disables API auth (local and dev).
    pub api_verifier: Option<Verifier>,
    /// The clock, injectable so claim leases are deterministic in tests.
    pub now: Arc<dyn Fn() -> Timestamp + Send + Sync>,
    gate_locks: AsyncKeyedLocks<(i64, String)>,
    exec_locks: AsyncKeyedLocks<String>,
    env_locks: AsyncKeyedLocks<String>,
}

impl App {
    /// An app over an open store and a GitHub client.
    pub fn new(store: Arc<Store>, gh: Arc<dyn GitHub>, cfg: Config) -> App {
        App {
            store,
            gh,
            cfg,
            hub: Hub::default(),
            approval: None,
            objects: None,
            executor: None,
            push_verifier: None,
            api_verifier: None,
            now: Arc::new(Timestamp::now),
            gate_locks: AsyncKeyedLocks::default(),
            exec_locks: AsyncKeyedLocks::default(),
            env_locks: AsyncKeyedLocks::default(),
        }
    }

    /// The store.
    pub fn store(&self) -> &Arc<Store> {
        &self.store
    }

    /// The configuration.
    pub fn config(&self) -> &Config {
        &self.cfg
    }

    fn now(&self) -> Timestamp {
        (self.now)()
    }

    /// Runs a store call on the blocking pool.
    pub async fn db<T: Send + 'static>(
        &self,
        f: impl FnOnce(&Store) -> tfstackplan_store::Result<T> + Send + 'static,
    ) -> Result<T> {
        let store = Arc::clone(&self.store);
        match tokio::task::spawn_blocking(move || f(&store)).await {
            Ok(r) => Ok(r?),
            Err(e) if e.is_panic() => std::panic::resume_unwind(e.into_panic()),
            Err(e) => Err(Error::Join(e.to_string())),
        }
    }

    /// Whether serve drives CI runs itself: an executor and a tier environment.
    pub fn run_trigger_armed(&self) -> bool {
        self.executor.is_some() && !self.cfg.environment.is_empty()
    }

    /// The check name of the plan gate: terraform/<env> when armed, else plan/<env>.
    fn plan_check_name(&self, environment: &str) -> String {
        if self.run_trigger_armed() {
            render::consolidated_check_name(environment)
        } else {
            render::check_run_name(environment)
        }
    }

    /// The check name of merge-lock-only surfaces.
    fn merge_gate_check_name(&self, environment: &str) -> String {
        if self.run_trigger_armed() {
            render::consolidated_check_name(environment)
        } else {
            render::apply_lock_name(environment)
        }
    }

    /// The central UI's view of an execution, "" without a UI.
    fn ui_url(&self, pr: i64, id: &str) -> String {
        if self.cfg.ui_base_url.is_empty() {
            return String::new();
        }
        if pr > 0 {
            format!("{}/pr/{pr}", self.cfg.ui_base_url)
        } else {
            format!("{}/t/{}/e/{id}", self.cfg.ui_base_url, self.cfg.environment)
        }
    }
}

/// One async mutex per key, made on first use: the shell's per-(pr, environment),
/// per-execution and per-environment locks. Held across awaits, so they are tokio
/// mutexes, unlike the store's [`tfstackplan_store::KeyedLocks`].
pub(crate) struct AsyncKeyedLocks<K> {
    locks: Mutex<HashMap<K, Arc<tokio::sync::Mutex<()>>>>,
}

impl<K> Default for AsyncKeyedLocks<K> {
    fn default() -> Self {
        AsyncKeyedLocks {
            locks: Mutex::new(HashMap::new()),
        }
    }
}

impl<K: Eq + std::hash::Hash> AsyncKeyedLocks<K> {
    pub(crate) async fn lock(&self, key: K) -> OwnedMutexGuard<()> {
        let m = Arc::clone(
            self.locks
                .lock()
                .unwrap_or_else(PoisonError::into_inner)
                .entry(key)
                .or_default(),
        );
        m.lock_owned().await
    }
}

/// The hub's subscribers by key: (subscription id, sender).
type Subscribers = Arc<Mutex<HashMap<String, Vec<(u64, mpsc::Sender<String>)>>>>;

/// Fans out log chunks and execution changes to live (SSE) subscribers by key. A
/// slow subscriber drops messages; the buffer and the object store keep the record.
#[derive(Default)]
pub(crate) struct Hub {
    subs: Subscribers,
    seq: Mutex<u64>,
}

/// A hub subscription; dropping it unsubscribes.
pub(crate) struct Subscription {
    pub(crate) rx: mpsc::Receiver<String>,
    key: String,
    id: u64,
    subs: Subscribers,
}

impl Drop for Subscription {
    fn drop(&mut self) {
        let mut subs = self.subs.lock().unwrap_or_else(PoisonError::into_inner);
        if let Some(set) = subs.get_mut(&self.key) {
            set.retain(|(id, _)| *id != self.id);
            if set.is_empty() {
                subs.remove(&self.key);
            }
        }
    }
}

impl Hub {
    pub(crate) fn subscribe(&self, key: &str) -> Subscription {
        let (tx, rx) = mpsc::channel(256);
        let id = {
            let mut seq = self.seq.lock().unwrap_or_else(PoisonError::into_inner);
            *seq += 1;
            *seq
        };
        self.subs
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .entry(key.to_string())
            .or_default()
            .push((id, tx));
        Subscription {
            rx,
            key: key.to_string(),
            id,
            subs: Arc::clone(&self.subs),
        }
    }

    pub(crate) fn publish(&self, key: &str, data: &str) {
        let subs = self.subs.lock().unwrap_or_else(PoisonError::into_inner);
        for (_, tx) in subs.get(key).into_iter().flatten() {
            let _ = tx.try_send(data.to_string());
        }
    }
}

#[cfg(test)]
mod tests;
