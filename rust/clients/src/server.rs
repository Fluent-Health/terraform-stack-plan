//! The control-plane server client (Go's `internal/runner` Client): JSON posts to
//! `/api/*` with an optional bearer token, disabled when no server is configured.
//!
//! Errors read like Go's: `post <path>: <status>: <body>`, with the
//! authentication hint appended on 401/403. A transport error carries reqwest's
//! wording after `post <path>: `, which differs from Go's.

use std::time::Duration;

use serde::{Deserialize, Serialize};

use tfstackplan_cli::run;

use crate::TokenFn;

/// One apply-lock claim (`events.Claim`).
#[derive(Clone, Debug, Deserialize, PartialEq, Eq)]
pub struct Claim {
    /// The environment.
    #[serde(default)]
    pub environment: String,
    /// The claimed stack.
    #[serde(default)]
    pub stack_path: String,
    /// The PR holding it.
    #[serde(default)]
    pub owner_pr: i64,
    /// RFC 3339 expiry, as the server sent it.
    #[serde(default)]
    pub expires_at: String,
}

/// A stack status tick (`events.Update`).
#[derive(Clone, Debug, Serialize, PartialEq, Eq)]
pub struct Update {
    /// Execution id.
    pub id: String,
    /// Stack path.
    pub stack: String,
    /// Status name.
    pub status: String,
    /// Failure detail.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub detail: String,
}

/// A slice of one stack's output (`events.LogChunk`).
#[derive(Clone, Debug, Serialize, PartialEq, Eq)]
pub struct LogChunk {
    /// Execution id.
    pub id: String,
    /// Stack path.
    pub stack: String,
    /// The output.
    pub data: String,
}

/// The server client. Every call on a disabled client is a no-op.
pub struct Client {
    base_url: String,
    audience: String,
    token: Option<TokenFn>,
    http: Option<reqwest::blocking::Client>,
    update_attempts: u32,
    update_backoff: Duration,
}

const TIMEOUT: Duration = Duration::from_secs(10);

impl Client {
    /// A client for `base_url` (trailing slashes trimmed; empty disables it),
    /// authenticating with `token` when given.
    pub fn new(base_url: &str, token: Option<TokenFn>) -> Result<Self, String> {
        let base_url = base_url.trim_end_matches('/').to_string();
        let http = if base_url.is_empty() {
            None
        } else {
            Some(crate::http_client(TIMEOUT)?)
        };
        Ok(Self {
            base_url,
            audience: String::new(),
            token,
            http,
            update_attempts: 3,
            update_backoff: Duration::from_millis(500),
        })
    }

    /// The audience named in authentication-failure hints.
    pub fn set_audience(&mut self, aud: &str) {
        self.audience = aud.to_string();
    }

    /// Shortens [`Client::update`]'s retry backoff (tests).
    pub fn set_update_backoff(&mut self, backoff: Duration) {
        self.update_backoff = backoff;
    }

    /// Whether a server is configured.
    pub fn enabled(&self) -> bool {
        !self.base_url.is_empty()
    }

    fn send<B: Serialize>(
        &self,
        path: &str,
        body: &B,
    ) -> Result<Option<reqwest::blocking::Response>, String> {
        let Some(http) = &self.http else {
            return Ok(None);
        };
        let mut req = http
            .post(format!("{}{path}", self.base_url))
            .header("Content-Type", "application/json")
            .json(body);
        if let Some(tok) = &self.token {
            let t = tok().map_err(|e| format!("post {path}: api token: {e}"))?;
            req = req.bearer_auth(t);
        }
        req.send()
            .map(Some)
            .map_err(|e| format!("post {path}: {}", transport(&e)))
    }

    /// Collapses a response to Go's convention: `Ok(body)` on 2xx.
    fn finish(&self, path: &str, resp: reqwest::blocking::Response) -> Result<Vec<u8>, String> {
        let status = resp.status().as_u16();
        let body = resp
            .bytes()
            .map(|b| b.to_vec())
            .map_err(|e| format!("post {path}: read body: {e}"))?;
        if status / 100 != 2 {
            if status == 401 || status == 403 {
                return Err(self.auth_error(path, status, &body));
            }
            return Err(format!(
                "post {path}: {status}: {}",
                String::from_utf8_lossy(&body)
            ));
        }
        Ok(body)
    }

    fn post<B: Serialize>(&self, path: &str, body: &B) -> Result<Vec<u8>, String> {
        match self.send(path, body)? {
            Some(resp) => self.finish(path, resp),
            None => Ok(Vec::new()),
        }
    }

    fn auth_error(&self, path: &str, status: u16, body: &[u8]) -> String {
        let mut identity = "anonymous (unauthenticated)".to_string();
        if let Some(tok) = &self.token
            && let Ok(t) = tok()
            && !t.is_empty()
        {
            identity = tfstackplan_cli::jwt::subject(&t)
                .unwrap_or_else(|_| "unknown (invalid/unparseable token)".to_string());
        }
        format!(
            "post {path}: {status}: {}\n\n\
             --- Authentication Failure Details ---\n\
             Identity: {identity}\n\
             Audience: {}\n\
             Hint: Ensure that your identity is on the `api_admins` allowlist in the server's `.tfstackplan.hcl` configuration.\n\
             Note: If you are using user ADC, ensure the client-side CLI version matches the server-side CLI version, and that TFSTACKPLAN_AUDIENCE is set to the server base URL.",
            String::from_utf8_lossy(body),
            self.audience
        )
    }

    /// `/api/claims/list`: the apply-lock claims for an environment.
    pub fn claims_list(&self, env: &str) -> Result<Vec<Claim>, String> {
        let path = "/api/claims/list";
        let body = self.post(path, &serde_json::json!({ "environment": env }))?;
        if body.is_empty() {
            return Ok(Vec::new());
        }
        let claims: Option<Vec<Claim>> =
            serde_json::from_slice(&body).map_err(|e| format!("post {path}: decode: {e}"))?;
        Ok(claims.unwrap_or_default())
    }

    /// `/api/claims/release`: one stack's claim, or all of a PR's when `stack` is empty.
    pub fn claims_release(&self, env: &str, pr: i64, stack: &str) -> Result<(), String> {
        self.post(
            "/api/claims/release",
            &serde_json::json!({ "environment": env, "pr": pr, "stack": stack }),
        )
        .map(drop)
    }

    /// `/api/admin/grants/release`.
    pub fn admin_grants_release(
        &self,
        pr: i64,
        env: &str,
        class: &str,
        target: &str,
        reason: &str,
    ) -> Result<(), String> {
        self.post(
            "/api/admin/grants/release",
            &serde_json::json!({ "class": class, "environment": env, "pr": pr, "reason": reason, "target": target }),
        )
        .map(drop)
    }

    /// `/api/admin/executions/cancel`.
    pub fn admin_executions_cancel(&self, id: &str, reason: &str) -> Result<(), String> {
        self.post(
            "/api/admin/executions/cancel",
            &serde_json::json!({ "id": id, "reason": reason }),
        )
        .map(drop)
    }

    /// `/api/admin/gates/satisfy`.
    pub fn admin_gates_satisfy(
        &self,
        pr: i64,
        env: &str,
        class: &str,
        target: &str,
        reason: &str,
    ) -> Result<(), String> {
        self.post(
            "/api/admin/gates/satisfy",
            &serde_json::json!({ "class": class, "environment": env, "pr": pr, "reason": reason, "target": target }),
        )
        .map(drop)
    }

    /// `/api/admin/checks/override`.
    pub fn admin_checks_override(
        &self,
        pr: i64,
        env: &str,
        check: &str,
        conclusion: &str,
        reason: &str,
    ) -> Result<(), String> {
        self.post(
            "/api/admin/checks/override",
            &serde_json::json!({ "check": check, "conclusion": conclusion, "environment": env, "pr": pr, "reason": reason }),
        )
        .map(drop)
    }

    /// `/api/update`, retrying transport errors and 5xx (a 4xx will not heal).
    pub fn update(&self, u: &Update) -> Result<(), String> {
        let path = "/api/update";
        let mut last = Ok(());
        for i in 0..self.update_attempts {
            if i > 0 {
                wait(self.update_backoff * i);
            }
            match self.send(path, u) {
                Ok(None) => return Ok(()),
                Ok(Some(resp)) => {
                    let s = resp.status().as_u16();
                    let r = self.finish(path, resp).map(drop);
                    if r.is_ok() || s / 100 == 4 {
                        return r;
                    }
                    last = r;
                }
                Err(e) => last = Err(e),
            }
        }
        last
    }

    /// `/api/logs` (best effort; callers ignore the error).
    pub fn log_chunk(&self, lc: &LogChunk) -> Result<(), String> {
        self.post("/api/logs", lc).map(drop)
    }
}

impl Client {
    /// `/api/init`: registers the execution and its stack subgraph.
    pub fn init(&self, i: &run::Init) -> Result<(), String> {
        self.post("/api/init", i).map(drop)
    }

    /// `/api/phase`: narrates a lifecycle phase.
    pub fn phase(&self, p: &run::PhaseEvent) -> Result<(), String> {
        self.post("/api/phase", p).map(drop)
    }

    /// `/api/finalize`: the run's terminal state.
    pub fn finalize(&self, f: &run::Finalize) -> Result<(), String> {
        self.post("/api/finalize", f).map(drop)
    }

    /// `/api/gate/revoke`: best-effort grant cleanup after an apply.
    pub fn gate_revoke(&self, g: &run::GateKey) -> Result<(), String> {
        self.post("/api/gate/revoke", g).map(drop)
    }

    /// `/api/gate/check`, fail-closed: no server is satisfied, a transport
    /// failure is unreachable, a non-2xx is the verdict its code names.
    pub fn gate_check(&self, g: &run::GateKey) -> run::Verdict {
        let path = "/api/gate/check";
        match self.send(path, g) {
            Ok(None) => run::Verdict {
                kind: run::VerdictKind::Satisfied,
                requester: String::new(),
                err: String::new(),
            },
            Err(e) => run::Verdict {
                kind: run::VerdictKind::Unreachable,
                requester: String::new(),
                err: e,
            },
            Ok(Some(resp)) => {
                let status = resp.status().as_u16();
                match resp.bytes() {
                    Ok(body) => run::gate_verdict(status, &body),
                    Err(e) => run::Verdict {
                        kind: run::VerdictKind::Unreachable,
                        requester: String::new(),
                        err: format!("post {path}: read body: {e}"),
                    },
                }
            }
        }
    }
}

/// Blocks the calling thread; this client is synchronous and never runs on an
/// async runtime.
#[expect(
    clippy::disallowed_methods,
    reason = "a blocking CLI client: no Tokio worker to block"
)]
fn wait(d: Duration) {
    std::thread::sleep(d);
}

/// reqwest's error with its source chain, closer to Go's single-line wording.
pub(crate) fn transport(e: &reqwest::Error) -> String {
    let mut s = e.to_string();
    let mut src = std::error::Error::source(e);
    while let Some(inner) = src {
        s.push_str(": ");
        s.push_str(&inner.to_string());
        src = inner.source();
    }
    s
}

#[cfg(test)]
mod tests;
