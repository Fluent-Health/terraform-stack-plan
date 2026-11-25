//! The GitHub surface serve needs (Go: `internal/server/github.go`).

use std::sync::Mutex;

use async_trait::async_trait;

use crate::Result;

/// Everything a PATCH to a check run needs.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct CheckRunUpdate {
    /// The check-run title, e.g. "Terraform plan".
    pub title: String,
    /// GFM headline, verdict chips and per-stack table, shown first.
    pub summary: String,
    /// The rendered plan report, shown below the summary.
    pub text: String,
    /// The Details link.
    pub details_url: String,
    /// Empty while running; otherwise success, failure, action_required or neutral.
    pub conclusion: String,
}

/// One PR queued on a branch's merge queue.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct MergeQueueEntry {
    pub position: i64,
    pub pr: i64,
    /// GitHub's MergeQueueEntryState: QUEUED, MERGEABLE, AWAITING_CHECKS, LOCKED, UNMERGEABLE.
    pub state: String,
}

/// A repository's merge queue for its default branch.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct MergeQueueResult {
    pub branch: String,
    pub entries: Vec<MergeQueueEntry>,
}

/// The calls serve makes to GitHub. The real client is a GitHub App; tests and demo
/// mode use [`MockGitHub`].
#[async_trait]
pub trait GitHub: Send + Sync {
    /// Opens an in_progress check run named `name` with Details link `details_url`;
    /// returns its id.
    async fn create_check_run(
        &self,
        repo: &str,
        sha: &str,
        name: &str,
        details_url: &str,
    ) -> Result<i64>;
    /// Patches an existing check run.
    async fn update_check_run(
        &self,
        repo: &str,
        check_run_id: i64,
        u: &CheckRunUpdate,
    ) -> Result<()>;
    /// Sets a commit status.
    async fn post_status(
        &self,
        repo: &str,
        sha: &str,
        context: &str,
        state: &str,
        description: &str,
        target_url: &str,
    ) -> Result<()>;
    /// The pull request's current head SHA.
    async fn pr_head_sha(&self, repo: &str, pr: i64) -> Result<String>;
    /// Whether the PR is closed without having been merged.
    async fn pr_abandoned(&self, repo: &str, pr: i64) -> Result<bool>;
    /// The PRs whose commits compose the merge group at `head_sha`.
    async fn merge_group_prs(&self, repo: &str, head_sha: &str) -> Result<Vec<i64>>;
    /// Re-requests a check run.
    async fn rerequest_check_run(&self, repo: &str, check_run_id: i64) -> Result<()>;
    /// The default branch's merge queue; a missing queue is an empty result.
    async fn merge_queue(&self, repo: &str) -> Result<MergeQueueResult>;
}

/// One recorded call to [`MockGitHub`].
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Call {
    CreateCheckRun {
        repo: String,
        sha: String,
        name: String,
        details_url: String,
    },
    UpdateCheckRun {
        repo: String,
        check_run_id: i64,
        update: CheckRunUpdate,
    },
    PostStatus {
        repo: String,
        sha: String,
        context: String,
        state: String,
        description: String,
        target_url: String,
    },
    PrHeadSha {
        repo: String,
        pr: i64,
    },
    PrAbandoned {
        repo: String,
        pr: i64,
    },
    MergeGroupPrs {
        repo: String,
        head_sha: String,
    },
    RerequestCheckRun {
        repo: String,
        check_run_id: i64,
    },
    MergeQueue {
        repo: String,
    },
}

type Hook<A, R> = Box<dyn Fn(A) -> Result<R> + Send + Sync>;

/// Go's `MockGitHub`: every call is recorded, and an unset hook is a no-op that
/// returns the zero value (check run id 0, empty SHA, not abandoned).
#[derive(Default)]
pub struct MockGitHub {
    pub create_check_run_fn: Option<Hook<(String, String, String, String), i64>>,
    pub update_check_run_fn: Option<Hook<(String, i64, CheckRunUpdate), ()>>,
    pub post_status_fn: Option<Hook<(String, String, String, String, String, String), ()>>,
    pub pr_head_sha_fn: Option<Hook<(String, i64), String>>,
    pub pr_abandoned_fn: Option<Hook<(String, i64), bool>>,
    pub merge_group_prs_fn: Option<Hook<(String, String), Vec<i64>>>,
    pub rerequest_check_run_fn: Option<Hook<(String, i64), ()>>,
    pub merge_queue_fn: Option<Hook<String, MergeQueueResult>>,
    calls: Mutex<Vec<Call>>,
}

impl MockGitHub {
    /// Every call so far, in order.
    pub fn calls(&self) -> Vec<Call> {
        self.calls.lock().map(|c| c.clone()).unwrap_or_default()
    }

    /// How many check runs were created (Go's `CreateCheckRunCalls`).
    pub fn create_check_run_calls(&self) -> usize {
        self.calls()
            .iter()
            .filter(|c| matches!(c, Call::CreateCheckRun { .. }))
            .count()
    }

    fn record(&self, c: Call) {
        if let Ok(mut calls) = self.calls.lock() {
            calls.push(c);
        }
    }
}

#[async_trait]
impl GitHub for MockGitHub {
    async fn create_check_run(
        &self,
        repo: &str,
        sha: &str,
        name: &str,
        details_url: &str,
    ) -> Result<i64> {
        self.record(Call::CreateCheckRun {
            repo: repo.into(),
            sha: sha.into(),
            name: name.into(),
            details_url: details_url.into(),
        });
        match &self.create_check_run_fn {
            Some(f) => f((repo.into(), sha.into(), name.into(), details_url.into())),
            None => Ok(0),
        }
    }

    async fn update_check_run(
        &self,
        repo: &str,
        check_run_id: i64,
        u: &CheckRunUpdate,
    ) -> Result<()> {
        self.record(Call::UpdateCheckRun {
            repo: repo.into(),
            check_run_id,
            update: u.clone(),
        });
        match &self.update_check_run_fn {
            Some(f) => f((repo.into(), check_run_id, u.clone())),
            None => Ok(()),
        }
    }

    async fn post_status(
        &self,
        repo: &str,
        sha: &str,
        context: &str,
        state: &str,
        description: &str,
        target_url: &str,
    ) -> Result<()> {
        self.record(Call::PostStatus {
            repo: repo.into(),
            sha: sha.into(),
            context: context.into(),
            state: state.into(),
            description: description.into(),
            target_url: target_url.into(),
        });
        match &self.post_status_fn {
            Some(f) => f((
                repo.into(),
                sha.into(),
                context.into(),
                state.into(),
                description.into(),
                target_url.into(),
            )),
            None => Ok(()),
        }
    }

    async fn pr_head_sha(&self, repo: &str, pr: i64) -> Result<String> {
        self.record(Call::PrHeadSha {
            repo: repo.into(),
            pr,
        });
        match &self.pr_head_sha_fn {
            Some(f) => f((repo.into(), pr)),
            None => Ok(String::new()),
        }
    }

    async fn pr_abandoned(&self, repo: &str, pr: i64) -> Result<bool> {
        self.record(Call::PrAbandoned {
            repo: repo.into(),
            pr,
        });
        match &self.pr_abandoned_fn {
            Some(f) => f((repo.into(), pr)),
            None => Ok(false),
        }
    }

    async fn merge_group_prs(&self, repo: &str, head_sha: &str) -> Result<Vec<i64>> {
        self.record(Call::MergeGroupPrs {
            repo: repo.into(),
            head_sha: head_sha.into(),
        });
        match &self.merge_group_prs_fn {
            Some(f) => f((repo.into(), head_sha.into())),
            None => Ok(Vec::new()),
        }
    }

    async fn rerequest_check_run(&self, repo: &str, check_run_id: i64) -> Result<()> {
        self.record(Call::RerequestCheckRun {
            repo: repo.into(),
            check_run_id,
        });
        match &self.rerequest_check_run_fn {
            Some(f) => f((repo.into(), check_run_id)),
            None => Ok(()),
        }
    }

    async fn merge_queue(&self, repo: &str) -> Result<MergeQueueResult> {
        self.record(Call::MergeQueue { repo: repo.into() });
        match &self.merge_queue_fn {
            Some(f) => f(repo.into()),
            None => Ok(MergeQueueResult::default()),
        }
    }
}
