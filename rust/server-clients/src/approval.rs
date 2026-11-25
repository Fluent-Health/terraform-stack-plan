//! The approval backend (Go: `internal/approval`): the server requests, lists and
//! revokes grants; only a human approves, in the provider. The grant types are the
//! gate core's.

use std::collections::BTreeMap;
use std::sync::Mutex;

use async_trait::async_trait;
pub use tfstackplan_gate::approval::{Grant, GrantState, Request, SlotCollisionError};

use crate::Result;

/// An approval provider.
#[async_trait]
pub trait Backend: Send + Sync {
    /// Ensures an open grant exists for `req`, reusing an open one (idempotent).
    async fn request_grant(&self, req: &Request) -> Result<Grant>;
    /// The grants on a (class, target) entitlement across all PRs.
    async fn list_grants(&self, class: &str, target: &str) -> Result<Vec<Grant>>;
    /// Revokes the open grants matching `req`; a no-op when there are none.
    async fn revoke(&self, req: &Request) -> Result<()>;
}

/// Go's `approval.Fake`: in memory, grants start AWAITING and a test approves them.
#[derive(Debug, Default)]
pub struct Fake {
    inner: Mutex<FakeInner>,
    /// Requester identities leased like gcp-pam does when a request names none.
    pub pool: Vec<String>,
}

#[derive(Debug, Default)]
struct FakeInner {
    grants: BTreeMap<String, Grant>,
    seq: u64,
}

fn fkey(r: &Request) -> String {
    format!("{}|{}|{}|{}", r.class, r.target, r.pr, r.environment)
}

impl Fake {
    /// An empty backend.
    pub fn new() -> Fake {
        Fake::default()
    }

    /// An empty backend leasing requesters from `pool`.
    pub fn with_pool(pool: Vec<String>) -> Fake {
        Fake {
            pool,
            ..Fake::default()
        }
    }

    /// Flips the grant for `req` to ACTIVE, as an approver would.
    pub fn approve(&self, req: &Request) {
        self.set_state(req, GrantState::Active);
    }

    /// Sets the grant for `req` to `state` (tests use it for denial and expiry).
    pub fn set_state(&self, req: &Request, state: GrantState) {
        if let Ok(mut inner) = self.inner.lock()
            && let Some(g) = inner.grants.get_mut(&fkey(req))
        {
            g.state = state;
        }
    }

    fn lock(&self) -> Result<std::sync::MutexGuard<'_, FakeInner>> {
        self.inner
            .lock()
            .map_err(|_| crate::Error::msg("approval fake: poisoned"))
    }
}

#[async_trait]
impl Backend for Fake {
    async fn request_grant(&self, req: &Request) -> Result<Grant> {
        let mut inner = self.lock()?;
        if let Some(g) = inner.grants.get(&fkey(req))
            && g.state.open()
        {
            return Ok(g.clone());
        }
        let mut requester = req.requester.clone();
        if requester.is_empty() && !self.pool.is_empty() {
            let used: Vec<&str> = inner
                .grants
                .values()
                .filter(|g| g.state.open() && !g.requester.is_empty())
                .map(|g| g.requester.as_str())
                .collect();
            if let Some(sa) = self.pool.iter().find(|sa| !used.contains(&sa.as_str())) {
                requester = sa.clone();
            }
        }
        inner.seq += 1;
        let g = Grant {
            name: format!("grant-{}", inner.seq),
            state: GrantState::Awaiting,
            request: req.clone(),
            requester,
        };
        inner.grants.insert(fkey(req), g.clone());
        Ok(g)
    }

    async fn list_grants(&self, class: &str, target: &str) -> Result<Vec<Grant>> {
        let inner = self.lock()?;
        let mut out: Vec<Grant> = inner
            .grants
            .values()
            .filter(|g| g.request.class == class && g.request.target == target)
            .cloned()
            .collect();
        out.sort_by(|a, b| a.name.cmp(&b.name));
        Ok(out)
    }

    async fn revoke(&self, req: &Request) -> Result<()> {
        let mut inner = self.lock()?;
        if let Some(g) = inner.grants.get_mut(&fkey(req))
            && g.state.open()
        {
            g.state = GrantState::Revoked;
        }
        Ok(())
    }
}
