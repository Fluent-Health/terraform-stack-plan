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
#![forbid(unsafe_code)]

pub mod approval;
pub mod executor;
pub mod github;
pub mod objects;

/// A client failure. The text is what Go's error would print, because some of it is
/// stored (a failed build start lands in the gate's event log) or returned to callers.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum Error {
    /// Any failure, carrying Go's error text.
    #[error("{0}")]
    Message(String),
    /// An approval request hit an occupied requester slot.
    #[error(transparent)]
    SlotCollision(#[from] tfstackplan_gate::approval::SlotCollisionError),
    /// The object does not exist (GCS 404, or a missing local object).
    #[error("{0}")]
    NotFound(String),
}

impl Error {
    /// An error with Go's text.
    pub fn msg(text: impl Into<String>) -> Error {
        Error::Message(text.into())
    }
}

/// The clients' result type.
pub type Result<T, E = Error> = std::result::Result<T, E>;
