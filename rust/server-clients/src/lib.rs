//! The I/O edge of `tfstackplan serve` and `tfstackplan ui`: every client that talks to
//! GitHub or Google, ported from Go. The server crate holds these behind traits so tests
//! and demo mode swap in the in-memory doubles defined here.
//!
//! | Go | module |
//! |---|---|
//! | `internal/server/github.go` (interface, `MockGitHub`) | [`github`] |
//! | `internal/approval` (`Backend`, `Fake`) | [`approval`] |
//! | `internal/executor` (`Backend`) | [`executor`] |
//! | `internal/server/objects.go` (`ObjectStore`) | [`objects`] |
//! | `internal/server/githubreal.go`, `githubauth.go` | [`github::app`] |
//! | `internal/gauth`, `internal/gauth/gauthtest` | [`gauth`] |
//! | `cmd/tfstackplan/gcpcreds.go` | [`gcpcreds`] |
//! | `internal/executor/cloudbuild.go` | `executor::cloudbuild` |
//! | `cmd/tfstackplan/gcsobjects.go` | `objects::Gcs` |
//! | `internal/approval/gcppam` | `approval::gcppam` |
#![forbid(unsafe_code)]

use std::future::Future;
use std::sync::Arc;

use async_trait::async_trait;

pub mod approval;
pub mod executor;
pub mod gauth;
pub mod gcpcreds;
pub mod github;
pub mod http;
pub mod objects;
#[cfg(any(test, feature = "test-support"))]
pub mod testserver;

/// Yields a currently valid bearer token (Go's `TokenFunc`). Implementations cache and
/// refresh; callers ask once per request. Any `Fn() -> impl Future<Output = Result<String>>`
/// is one.
#[async_trait]
pub trait TokenSource: Send + Sync {
    /// A token valid now.
    async fn token(&self) -> Result<String>;
}

#[async_trait]
impl<F, Fut> TokenSource for F
where
    F: Fn() -> Fut + Send + Sync,
    Fut: Future<Output = Result<String>> + Send,
{
    async fn token(&self) -> Result<String> {
        self().await
    }
}

/// A shared [`TokenSource`].
pub type TokenFunc = Arc<dyn TokenSource>;

/// Yields an access token for a given service account (Go's `ImpersonateFunc`). Any
/// `Fn(String) -> impl Future<Output = Result<String>>` is one.
#[async_trait]
pub trait Impersonator: Send + Sync {
    /// A token acting as `service_account`.
    async fn impersonate(&self, service_account: &str) -> Result<String>;
}

#[async_trait]
impl<F, Fut> Impersonator for F
where
    F: Fn(String) -> Fut + Send + Sync,
    Fut: Future<Output = Result<String>> + Send,
{
    async fn impersonate(&self, service_account: &str) -> Result<String> {
        self(service_account.to_string()).await
    }
}

/// A client failure. The text is what Go's error would print, because some of it is
/// stored (a failed build start lands in the gate's event log) or returned to callers.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum Error {
    /// Any failure, carrying Go's error text.
    #[error("{0}")]
    Message(String),
    /// An approval request hit an occupied requester slot. Boxed: the blocking grant is large
    /// and every `Result` here carries this enum.
    #[error(transparent)]
    SlotCollision(Box<tfstackplan_gate::approval::SlotCollisionError>),
    /// The object does not exist (GCS 404, or a missing local object).
    #[error("{0}")]
    NotFound(String),
}

impl From<tfstackplan_gate::approval::SlotCollisionError> for Error {
    fn from(e: tfstackplan_gate::approval::SlotCollisionError) -> Error {
        Error::SlotCollision(Box::new(e))
    }
}

impl Error {
    /// An error with Go's text.
    pub fn msg(text: impl Into<String>) -> Error {
        Error::Message(text.into())
    }
}

/// The clients' result type.
pub type Result<T, E = Error> = std::result::Result<T, E>;
