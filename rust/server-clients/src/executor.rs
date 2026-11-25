//! The CI backend serve drives when it triggers runs itself (Go: `internal/executor`).

use async_trait::async_trait;

use crate::Result;

/// One CI run to start.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct RunRequest {
    /// "plan" or "apply"; selects the backend trigger.
    pub kind: String,
    pub environment: String,
    /// The exact commit to build.
    pub sha: String,
    /// The branch, for backends that need one.
    pub branch: String,
    /// The serve-minted execution id the runner reports under.
    pub execution_id: String,
    pub pr: i64,
}

/// A started run at the backend, e.g. a Cloud Build build id.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Ref {
    pub id: String,
}

/// The backend's view of a run, for the start watchdog.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Phase {
    Queued,
    Working,
    Done,
    Failed,
    NotFound,
}

impl Phase {
    /// Go's string for the phase.
    pub fn as_str(self) -> &'static str {
        match self {
            Phase::Queued => "queued",
            Phase::Working => "working",
            Phase::Done => "done",
            Phase::Failed => "failed",
            Phase::NotFound => "notfound",
        }
    }
}

/// Starts and manages CI runs.
#[async_trait]
pub trait Backend: Send + Sync {
    /// Begins a run.
    async fn start(&self, req: &RunRequest) -> Result<Ref>;
    /// Stops a run; cancelling a finished run is not an error.
    async fn cancel(&self, r: &Ref) -> Result<()>;
    /// The run's current phase.
    async fn probe(&self, r: &Ref) -> Result<Phase>;
}
