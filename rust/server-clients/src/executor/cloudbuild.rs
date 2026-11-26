//! Google Cloud Build as the CI backend (Go: `internal/executor/cloudbuild.go`): serve runs
//! terraform-managed triggers by name (`triggers.run`), cancels builds and reads their status.

use std::collections::BTreeMap;
use std::time::Duration;

use async_trait::async_trait;
use reqwest::Method;
use serde::Deserialize;
use serde_json::{Value, json};

use super::{Backend, Phase, Ref, RunRequest};
use crate::http::{self, is_2xx};
use crate::{Error, Result, TokenFunc};

/// The public Cloud Build API.
pub const DEFAULT_BASE_URL: &str = "https://cloudbuild.googleapis.com";

/// How long one Cloud Build call may take.
const TIMEOUT: Duration = Duration::from_secs(30);

/// A response body is read up to this many bytes.
const BODY_LIMIT: usize = 1 << 20;

/// Go's `CloudBuild`.
pub struct CloudBuild {
    project: String,
    region: String,
    /// Run kind ("plan", "apply") to trigger name.
    triggers: BTreeMap<String, String>,
    token: TokenFunc,
    base: String,
    hc: reqwest::Client,
}

impl std::fmt::Debug for CloudBuild {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("CloudBuild")
            .field("project", &self.project)
            .field("region", &self.region)
            .field("triggers", &self.triggers)
            .field("base", &self.base)
            .finish_non_exhaustive()
    }
}

impl CloudBuild {
    /// A backend running `triggers` (kind to trigger name) in `project`/`region`, with
    /// cloud-platform access tokens from `token`.
    pub fn new(
        project: &str,
        region: &str,
        triggers: BTreeMap<String, String>,
        token: TokenFunc,
    ) -> Result<CloudBuild> {
        Ok(CloudBuild {
            project: project.into(),
            region: region.into(),
            triggers,
            token,
            base: DEFAULT_BASE_URL.into(),
            hc: http::client(TIMEOUT)?,
        })
    }

    /// The same backend calling `base` instead (tests point it at a fake).
    #[must_use]
    pub fn with_base_url(mut self, base: &str) -> CloudBuild {
        self.base = base.trim_end_matches('/').to_string();
        self
    }

    fn url(&self, rest: &str) -> String {
        format!(
            "{}/v1/projects/{}/locations/{}/{rest}",
            self.base, self.project, self.region
        )
    }

    /// An authed JSON call returning body and status; errors only on token or transport
    /// failure so callers can read the status.
    async fn call(
        &self,
        method: Method,
        url: &str,
        payload: Option<Value>,
    ) -> Result<(Vec<u8>, u16)> {
        let tok = self
            .token
            .token()
            .await
            .map_err(|e| Error::msg(format!("cloudbuild: token: {e}")))?;
        let mut req = self.hc.request(method.clone(), url).bearer_auth(tok);
        if let Some(p) = payload {
            req = req
                .header("Content-Type", "application/json")
                .body(p.to_string());
        }
        let resp = req
            .send()
            .await
            .map_err(|e| Error::msg(format!("cloudbuild: {method} {url}: {e}")))?;
        let status = resp.status();
        if !is_2xx(status) {
            http::log_non_2xx("cloudbuild", &method, url, status.as_u16());
        }
        let mut body = resp
            .bytes()
            .await
            .map_err(|e| Error::msg(e.to_string()))?
            .to_vec();
        body.truncate(BODY_LIMIT);
        Ok((body, status.as_u16()))
    }
}

/// Go's `truncate`: the trimmed body, cut at 300 bytes with an ellipsis.
fn truncate(b: &[u8]) -> String {
    let s = String::from_utf8_lossy(b);
    let s = s.trim();
    if s.len() <= 300 {
        return s.to_string();
    }
    let mut end = 300;
    while !s.is_char_boundary(end) {
        end -= 1;
    }
    format!("{}…", &s[..end])
}

#[async_trait]
impl Backend for CloudBuild {
    /// Runs the kind's trigger at the exact commit (or the branch when no SHA is known);
    /// `_EXECUTION_ID` and `_PR_NUMBER` let the build report under serve's execution id.
    async fn start(&self, req: &RunRequest) -> Result<Ref> {
        let trigger = match self.triggers.get(&req.kind) {
            Some(t) if !t.is_empty() => t,
            _ => {
                return Err(Error::msg(format!(
                    "cloudbuild: no trigger configured for kind {:?}",
                    req.kind
                )));
            }
        };
        let mut source = json!({"substitutions": {
            "_EXECUTION_ID": req.execution_id,
            "_PR_NUMBER": req.pr.to_string(),
        }});
        // RepoSource's revision is a oneof: the exact commit wins over the branch.
        if !req.sha.is_empty() {
            source["commitSha"] = req.sha.as_str().into();
        } else if !req.branch.is_empty() {
            source["branchName"] = req.branch.as_str().into();
        }
        let url = self.url(&format!("triggers/{trigger}:run"));
        let (body, status) = self
            .call(Method::POST, &url, Some(json!({"source": source})))
            .await?;
        if status / 100 != 2 {
            return Err(Error::msg(format!(
                "cloudbuild: triggers.run {trigger}: {status}: {}",
                truncate(&body)
            )));
        }
        // A long-running Operation; the build id is metadata.build.id.
        #[derive(Deserialize, Default)]
        struct Build {
            #[serde(default)]
            id: String,
        }
        #[derive(Deserialize, Default)]
        struct Meta {
            #[serde(default)]
            build: Build,
        }
        #[derive(Deserialize)]
        struct Op {
            #[serde(default)]
            metadata: Meta,
        }
        match serde_json::from_slice::<Op>(&body) {
            Ok(op) if !op.metadata.build.id.is_empty() => Ok(Ref {
                id: op.metadata.build.id,
            }),
            _ => Err(Error::msg(format!(
                "cloudbuild: triggers.run {trigger}: no build id in operation: {}",
                truncate(&body)
            ))),
        }
    }

    /// Best effort: an empty ref, an already finished build (400) and an unknown one (404)
    /// are not errors.
    async fn cancel(&self, r: &Ref) -> Result<()> {
        if r.id.is_empty() {
            return Ok(());
        }
        let url = self.url(&format!("builds/{}:cancel", r.id));
        let (body, status) = self.call(Method::POST, &url, Some(json!({}))).await?;
        if status / 100 != 2 && status != 400 && status != 404 {
            return Err(Error::msg(format!(
                "cloudbuild: builds.cancel {}: {status}: {}",
                r.id,
                truncate(&body)
            )));
        }
        Ok(())
    }

    async fn probe(&self, r: &Ref) -> Result<Phase> {
        let url = self.url(&format!("builds/{}", r.id));
        let (body, status) = self.call(Method::GET, &url, None).await?;
        if status == 404 {
            return Ok(Phase::NotFound);
        }
        if status / 100 != 2 {
            return Err(Error::msg(format!(
                "cloudbuild: builds.get {}: {status}: {}",
                r.id,
                truncate(&body)
            )));
        }
        #[derive(Deserialize)]
        struct B {
            #[serde(default)]
            status: String,
        }
        let b: B = serde_json::from_slice(&body)
            .map_err(|e| Error::msg(format!("cloudbuild: builds.get {}: decode: {e}", r.id)))?;
        match b.status.as_str() {
            "PENDING" | "QUEUED" => Ok(Phase::Queued),
            "WORKING" => Ok(Phase::Working),
            "SUCCESS" => Ok(Phase::Done),
            "FAILURE" | "INTERNAL_ERROR" | "TIMEOUT" | "EXPIRED" | "CANCELLED" => Ok(Phase::Failed),
            other => Err(Error::msg(format!(
                "cloudbuild: builds.get {}: unknown status {other:?}",
                r.id
            ))),
        }
    }
}

#[cfg(test)]
mod tests;
