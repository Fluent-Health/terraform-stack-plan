//! Per-key serialization (Go: the shell's per-(pr, environment), per-execution and
//! per-environment mutex maps).

use std::collections::HashMap;
use std::hash::Hash;
use std::sync::{Arc, Mutex, PoisonError};

/// One mutex per key, made on first use. Signals for the same key run one at a
/// time; different keys run in parallel. The serve layer holds a key's lock across
/// a whole decide, persist and act round, so a stream is never appended at a
/// version another round has already moved past.
#[derive(Debug)]
pub struct KeyedLocks<K> {
    locks: Mutex<HashMap<K, Arc<Mutex<()>>>>,
}

impl<K> Default for KeyedLocks<K> {
    fn default() -> Self {
        KeyedLocks {
            locks: Mutex::new(HashMap::new()),
        }
    }
}

impl<K: Eq + Hash> KeyedLocks<K> {
    /// Runs `f` while holding `key`'s lock.
    pub fn with<T>(&self, key: K, f: impl FnOnce() -> T) -> T {
        let lock = Arc::clone(
            self.locks
                .lock()
                .unwrap_or_else(PoisonError::into_inner)
                .entry(key)
                .or_default(),
        );
        // A round is two transactions (append with snapshot, then projections), so
        // a panic between them can leave a projection behind the log, as in Go. The
        // next round on the key, or a rebuild, projects it again.
        let _guard = lock.lock().unwrap_or_else(PoisonError::into_inner);
        f()
    }
}
