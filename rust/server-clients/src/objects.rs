//! Completed-log offload (Go: `internal/server/objects.go`).

use async_trait::async_trait;

use crate::Result;

/// Persists completed log objects. A missing object is [`crate::Error::NotFound`].
#[async_trait]
pub trait ObjectStore: Send + Sync {
    /// Writes `body` at `key`.
    async fn put(&self, key: &str, body: Vec<u8>) -> Result<()>;
    /// Reads the object at `key`.
    async fn get(&self, key: &str) -> Result<Vec<u8>>;
}
