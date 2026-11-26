//! The approval backend over GCP Privileged Access Manager (Go: `internal/approval/gcppam`).
//! Serve requests a time-bound grant per (class, target) entitlement, lists grant state and
//! revokes after apply; humans approve in PAM. Everything deployment-specific is [`Config`].
//! Credentials are injected: an ADC token for list and revoke, and an impersonated requester
//! token for create.

use std::collections::{BTreeMap, BTreeSet};
use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use reqwest::Method;
use serde::Deserialize;
use serde_json::json;

use super::{Backend as ApprovalBackend, Grant, GrantState, Request, SlotCollisionError};
use crate::http::{self, is_2xx, query_escape};
use crate::{Error, Impersonator, Result, TokenFunc};

/// The public PAM endpoint.
pub const DEFAULT_BASE_URL: &str = "https://privilegedaccessmanager.googleapis.com/v1";

/// How long one PAM call may take.
const TIMEOUT: Duration = Duration::from_secs(30);

/// Go's `gcppam.Config`.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Config {
    /// The PAM REST base; empty means [`DEFAULT_BASE_URL`].
    pub base_url: String,
    /// The entitlement location; empty means "global".
    pub location: String,
    /// Class (e.g. "iam") to the PAM entitlement id requested for it.
    pub entitlements: BTreeMap<String, String>,
    /// Class to entitlement scope: "projects" (the default), "folders" or "organizations".
    pub entitlement_scopes: BTreeMap<String, String>,
    /// The service accounts the backend impersonates to request grants, one leased per PR.
    pub requester_pool: Vec<String>,
    /// The requested grant duration; empty means "28800s" (eight hours).
    pub duration: String,
}

impl Config {
    pub(crate) fn base_url(&self) -> &str {
        if self.base_url.is_empty() {
            DEFAULT_BASE_URL
        } else {
            &self.base_url
        }
    }

    pub(crate) fn location(&self) -> &str {
        if self.location.is_empty() {
            "global"
        } else {
            &self.location
        }
    }

    pub(crate) fn duration(&self) -> &str {
        if self.duration.is_empty() {
            "28800s"
        } else {
            &self.duration
        }
    }

    /// `<scope>/<target>/locations/<location>/entitlements/<entitlement>`, or "" when the
    /// class has no entitlement.
    pub(crate) fn entitlement_name(&self, class: &str, target: &str) -> String {
        let Some(e) = self.entitlements.get(class).filter(|e| !e.is_empty()) else {
            return String::new();
        };
        let scope = self
            .entitlement_scopes
            .get(class)
            .filter(|s| !s.is_empty())
            .map_or("projects", String::as_str);
        format!(
            "{scope}/{target}/locations/{}/entitlements/{e}",
            self.location()
        )
    }

    /// The pool identity in the PR's modulo slot; "" for an empty pool.
    pub(crate) fn requester(&self, pr: i64) -> String {
        let Ok(n) = i64::try_from(self.requester_pool.len()) else {
            return String::new();
        };
        if n == 0 {
            return String::new();
        }
        let i = usize::try_from(pr.rem_euclid(n)).unwrap_or(0);
        self.requester_pool.get(i).cloned().unwrap_or_default()
    }

    /// The first pool identity not holding an open grant, else the PR's modulo slot.
    pub(crate) fn lease_requester(&self, pr: i64, leased: &BTreeSet<String>) -> String {
        self.requester_pool
            .iter()
            .find(|sa| !leased.contains(*sa))
            .cloned()
            .unwrap_or_else(|| self.requester(pr))
    }
}

/// The grant justification that carries the correlation: `PR #<n> env=<env>`.
pub(crate) fn justification(req: &Request) -> String {
    format!("PR #{} env={}", req.pr, req.environment)
}

/// RE2's `\s`: ASCII tab, newline, form feed, carriage return and space.
fn re2_space(c: char) -> bool {
    matches!(c, '\t' | '\n' | '\x0c' | '\r' | ' ')
}

/// Go's `parsePRenv`: the leftmost match of `PR #(\d+) env=(\S+)`.
pub(crate) fn parse_pr_env(j: &str) -> Option<(i64, String)> {
    for (i, _) in j.match_indices("PR #") {
        let rest = &j[i + 4..];
        let digits_end = rest
            .find(|c: char| !c.is_ascii_digit())
            .unwrap_or(rest.len());
        let digits = &rest[..digits_end];
        let Some(env_rest) = rest[digits_end..].strip_prefix(" env=") else {
            continue;
        };
        let env_end = env_rest.find(re2_space).unwrap_or(env_rest.len());
        if digits.is_empty() || env_end == 0 {
            continue;
        }
        return digits
            .parse::<i64>()
            .ok()
            .map(|n| (n, env_rest[..env_end].to_string()));
    }
    None
}

/// Go's `validEnvironment`: non-empty and no Unicode whitespace, so it round-trips through
/// the justification.
pub(crate) fn valid_environment(env: &str) -> Result<()> {
    if env.is_empty() {
        return Err(Error::msg("gcppam: environment must not be empty"));
    }
    if env.chars().any(char::is_whitespace) {
        return Err(Error::msg(format!(
            "gcppam: environment {env:?} must not contain whitespace (breaks grant correlation)"
        )));
    }
    Ok(())
}

/// Go's `mapState`. An unknown PAM state is closed; Go keeps its raw text, which the gate
/// state type here cannot hold, so it reads as no grant.
pub(crate) fn map_state(pam: &str) -> GrantState {
    match pam {
        "APPROVAL_AWAITED" | "SCHEDULED" => GrantState::Awaiting,
        "ACTIVATING" => GrantState::Activating,
        "ACTIVE" => GrantState::Active,
        "DENIED" => GrantState::Denied,
        "REVOKED" => GrantState::Revoked,
        "EXPIRED" | "ENDED" => GrantState::Expired,
        _ => GrantState::Absent,
    }
}

/// Go's `gcppam.Backend`.
pub struct Backend {
    cfg: Config,
    token: TokenFunc,
    impersonate: Arc<dyn Impersonator>,
    hc: reqwest::Client,
}

impl std::fmt::Debug for Backend {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Backend")
            .field("cfg", &self.cfg)
            .finish_non_exhaustive()
    }
}

#[derive(Deserialize, Default)]
struct PamJustification {
    #[serde(default, rename = "unstructuredJustification")]
    unstructured: String,
}

#[derive(Deserialize)]
struct PamGrant {
    #[serde(default)]
    name: String,
    #[serde(default)]
    state: String,
    #[serde(default)]
    requester: String,
    #[serde(default)]
    justification: PamJustification,
}

#[derive(Deserialize)]
struct GrantsList {
    #[serde(default)]
    grants: Vec<PamGrant>,
    #[serde(default, rename = "nextPageToken")]
    next_page_token: String,
}

impl Backend {
    /// A backend with ADC tokens from `token` (list, revoke) and requester tokens from
    /// `impersonate` (create).
    pub fn new(
        cfg: Config,
        token: TokenFunc,
        impersonate: Arc<dyn Impersonator>,
    ) -> Result<Backend> {
        Ok(Backend {
            cfg,
            token,
            impersonate,
            hc: http::client(TIMEOUT)?,
        })
    }

    async fn call(
        &self,
        bearer: &str,
        method: Method,
        url: &str,
        body: Option<String>,
    ) -> Result<Vec<u8>> {
        let mut req = self.hc.request(method.clone(), url).bearer_auth(bearer);
        if let Some(b) = body {
            req = req.header("Content-Type", "application/json").body(b);
        }
        let resp = req.send().await.map_err(|e| Error::msg(e.to_string()))?;
        let status = resp.status();
        let rb = resp.bytes().await.unwrap_or_default().to_vec();
        if !is_2xx(status) {
            http::log_non_2xx("pam", &method, url, status.as_u16());
            return Err(Error::msg(format!(
                "gcppam: {method} {url} -> {}: {}",
                status.as_u16(),
                String::from_utf8_lossy(&rb)
            )));
        }
        Ok(rb)
    }

    /// The first open grant held by another (PR, environment), if listing works.
    async fn find_foreign_open_grant(&self, req: &Request) -> Option<Grant> {
        let grants = self.list_grants(&req.class, &req.target).await.ok()?;
        grants.into_iter().find(|g| {
            g.state.open() && (g.request.pr != req.pr || g.request.environment != req.environment)
        })
    }
}

#[async_trait]
impl ApprovalBackend for Backend {
    /// Reuses an open grant whose justification matches; otherwise leases a requester,
    /// impersonates it and creates one. A failed create while another PR holds the slot is
    /// [`Error::SlotCollision`].
    async fn request_grant(&self, req: &Request) -> Result<Grant> {
        valid_environment(&req.environment)?;
        let ent = self.cfg.entitlement_name(&req.class, &req.target);
        if ent.is_empty() {
            return Err(Error::msg(format!(
                "gcppam: no entitlement configured for class {:?}",
                req.class
            )));
        }
        let grants = self.list_grants(&req.class, &req.target).await?;
        let mut leased = BTreeSet::new();
        for g in grants {
            if !g.state.open() {
                continue;
            }
            if g.request.pr == req.pr && g.request.environment == req.environment {
                return Ok(g);
            }
            if !g.requester.is_empty() {
                leased.insert(g.requester);
            }
        }
        let mut requester = req.requester.clone();
        if requester.is_empty() {
            requester = self.cfg.lease_requester(req.pr, &leased);
        }
        if requester.is_empty() {
            return Err(Error::msg("gcppam: empty requester pool"));
        }
        let tok = self
            .impersonate
            .impersonate(&requester)
            .await
            .map_err(|e| Error::msg(format!("gcppam: impersonate {requester}: {e}")))?;
        let body = json!({
            "requestedDuration": self.cfg.duration(),
            "justification": {"unstructuredJustification": justification(req)},
        });
        let url = format!("{}/{ent}/grants", self.cfg.base_url());
        let rb = match self
            .call(&tok, Method::POST, &url, Some(body.to_string()))
            .await
        {
            Ok(rb) => rb,
            Err(e) => {
                if let Some(blocking_grant) = self.find_foreign_open_grant(req).await {
                    return Err(SlotCollisionError { blocking_grant }.into());
                }
                return Err(e);
            }
        };
        #[derive(Deserialize)]
        struct Created {
            #[serde(default)]
            name: String,
        }
        let created: Created = serde_json::from_slice(&rb)
            .map_err(|e| Error::msg(format!("gcppam: unmarshal create: {e}")))?;
        Ok(Grant {
            name: created.name,
            state: GrantState::Awaiting,
            request: req.clone(),
            requester,
        })
    }

    /// The entitlement's grants, state normalised and (PR, environment) parsed from each
    /// justification. Uses the ADC token and follows pagination.
    async fn list_grants(&self, class: &str, target: &str) -> Result<Vec<Grant>> {
        let ent = self.cfg.entitlement_name(class, target);
        if ent.is_empty() {
            return Err(Error::msg(format!(
                "gcppam: no entitlement configured for class {class:?}"
            )));
        }
        let tok = self
            .token
            .token()
            .await
            .map_err(|e| Error::msg(format!("gcppam: ADC token: {e}")))?;
        let mut out = Vec::new();
        let mut page_token = String::new();
        loop {
            let mut url = format!("{}/{ent}/grants", self.cfg.base_url());
            if !page_token.is_empty() {
                url.push_str("?pageToken=");
                url.push_str(&query_escape(&page_token));
            }
            let rb = self.call(&tok, Method::GET, &url, None).await?;
            let page: GrantsList = serde_json::from_slice(&rb)
                .map_err(|e| Error::msg(format!("gcppam: unmarshal grants: {e}")))?;
            for g in page.grants {
                let mut request = Request {
                    class: class.into(),
                    target: target.into(),
                    ..Request::default()
                };
                if let Some((pr, env)) = parse_pr_env(&g.justification.unstructured) {
                    request.pr = pr;
                    request.environment = env;
                }
                out.push(Grant {
                    name: g.name,
                    state: map_state(&g.state),
                    request,
                    requester: g.requester,
                });
            }
            if page.next_page_token.is_empty() {
                return Ok(out);
            }
            page_token = page.next_page_token;
        }
    }

    /// Revokes every open grant matching `req` with the ADC token (the requester cannot
    /// revoke). A no-op when none match.
    async fn revoke(&self, req: &Request) -> Result<()> {
        let grants = self.list_grants(&req.class, &req.target).await?;
        let tok = self
            .token
            .token()
            .await
            .map_err(|e| Error::msg(format!("gcppam: ADC token: {e}")))?;
        for g in grants {
            if !g.state.open() || g.request.pr != req.pr || g.request.environment != req.environment
            {
                continue;
            }
            let url = format!("{}/{}:revoke", self.cfg.base_url(), g.name);
            let body = r#"{"reason":"post-apply cleanup"}"#.to_string();
            self.call(&tok, Method::POST, &url, Some(body)).await?;
        }
        Ok(())
    }
}

/// A failed [`decide_grant`].
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum DecideError {
    /// The name is not a grant, or the call never got an answer.
    #[error("{0}")]
    Other(String),
    /// PAM's own rejection, verbatim (e.g. 403 for a user without approver IAM).
    #[error("pam: {status}: {body}")]
    Rejected { status: u16, body: String },
}

impl From<DecideError> for Error {
    fn from(e: DecideError) -> Error {
        Error::msg(e.to_string())
    }
}

/// Go's `DecideGrant`: approves or denies one grant as `bearer`, a human's short-lived
/// OAuth token, never a service identity. GCP enforces who may approve and audits the
/// human. An empty `base_url` means [`DEFAULT_BASE_URL`]; a non-empty `quota_project` rides
/// `x-goog-user-project`.
pub async fn decide_grant(
    base_url: &str,
    quota_project: &str,
    bearer: &str,
    grant_name: &str,
    approve: bool,
    reason: &str,
) -> Result<(), DecideError> {
    validate_grant_name(grant_name).map_err(|e| DecideError::Other(e.to_string()))?;
    let base = if base_url.is_empty() {
        DEFAULT_BASE_URL
    } else {
        base_url
    };
    let verb = if approve { ":approve" } else { ":deny" };
    let url = format!("{}/{grant_name}{verb}", base.trim_end_matches('/'));
    let hc = http::client(TIMEOUT).map_err(|e| DecideError::Other(e.to_string()))?;
    let mut req = hc
        .post(&url)
        .bearer_auth(bearer)
        .header("Content-Type", "application/json")
        .body(json!({"reason": reason}).to_string());
    if !quota_project.is_empty() {
        req = req.header("x-goog-user-project", quota_project);
    }
    let resp = req
        .send()
        .await
        .map_err(|e| DecideError::Other(format!("pam {verb}: {e}")))?;
    let status = resp.status();
    if !is_2xx(status) {
        http::log_non_2xx("pam", &Method::POST, &url, status.as_u16());
        let mut b = resp.bytes().await.unwrap_or_default().to_vec();
        b.truncate(4096);
        return Err(DecideError::Rejected {
            status: status.as_u16(),
            body: String::from_utf8_lossy(&b).into_owned(),
        });
    }
    Ok(())
}

/// Go's `ValidateGrantName`: `^(projects|folders|organizations)/[^/]+/locations/[^/]+/`
/// `entitlements/[^/]+/grants/[^/]+$`.
pub fn validate_grant_name(name: &str) -> Result<()> {
    let segs: Vec<&str> = name.split('/').collect();
    let ok = matches!(
        segs[..],
        [scope, a, "locations", b, "entitlements", c, "grants", d]
            if matches!(scope, "projects" | "folders" | "organizations")
                && [a, b, c, d].iter().all(|s| !s.is_empty())
    );
    if ok {
        Ok(())
    } else {
        Err(Error::msg(format!(
            "not a PAM grant resource name: {name:?}"
        )))
    }
}

#[cfg(test)]
mod tests;
