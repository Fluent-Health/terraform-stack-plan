//! The production GitHub client: a GitHub App (Go: `internal/server/githubreal.go` and
//! `githubauth.go`). It signs an App JWT with the App's RSA key, exchanges it for an
//! installation token, and calls the REST and GraphQL APIs with that token.

use std::sync::{Arc, LazyLock, Mutex};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use async_trait::async_trait;
use aws_lc_rs::rand::SystemRandom;
use aws_lc_rs::signature::{RSA_PKCS1_SHA256, RsaKeyPair};
use base64::Engine as _;
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use reqwest::Method;
use rustls_pki_types::PrivateKeyDer;
use rustls_pki_types::pem::PemObject as _;
use serde::Deserialize;
use serde_json::{Map, Value, json};

use super::{CheckRunUpdate, GitHub, MergeQueueEntry, MergeQueueResult};
use crate::http::{self, is_2xx};
use crate::{Error, Result};

/// Overrides the GitHub REST base, e.g. to point at a local fake.
pub const ENV_GITHUB_API_URL: &str = "TFSTACKPLAN_GITHUB_API_URL";

/// The public GitHub API.
pub const DEFAULT_API_BASE: &str = "https://api.github.com";

/// How long one GitHub call may take.
const TIMEOUT: Duration = Duration::from_secs(30);

/// An installation token is reused until this long before GitHub's expiry.
const TOKEN_REFRESH_MARGIN: Duration = Duration::from_secs(5 * 60);

static API_BASE: LazyLock<String> =
    LazyLock::new(|| github_api_base(std::env::var(ENV_GITHUB_API_URL).ok().as_deref()));

/// The GitHub REST base, read once from `$TFSTACKPLAN_GITHUB_API_URL`.
pub fn api_base() -> &'static str {
    &API_BASE
}

/// Go's `githubAPIBase`: the override without trailing slashes, or the public API when it
/// is unset or empty.
pub fn github_api_base(env: Option<&str>) -> String {
    match env.map(|v| v.trim_end_matches('/')) {
        Some(v) if !v.is_empty() => v.to_string(),
        _ => DEFAULT_API_BASE.to_string(),
    }
}

/// Go's `RealClient`: a GitHub App installation. Installation tokens are cached until five
/// minutes before GitHub's `expires_at` (Go mints one per request).
pub struct RealClient {
    app_id: String,
    installation_id: String,
    key: Arc<RsaKeyPair>,
    base: String,
    http: reqwest::Client,
    token: Mutex<Option<(String, SystemTime)>>,
}

impl std::fmt::Debug for RealClient {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("RealClient")
            .field("app_id", &self.app_id)
            .field("installation_id", &self.installation_id)
            .field("base", &self.base)
            .finish_non_exhaustive()
    }
}

impl RealClient {
    /// Parses the App's PEM private key and returns a client for the App id and
    /// installation id, calling [`api_base`].
    pub fn new(app_id: &str, installation_id: &str, pem_key: &[u8]) -> Result<RealClient> {
        if app_id.is_empty() || installation_id.is_empty() {
            return Err(Error::msg(
                "github: app id and installation id are required",
            ));
        }
        let key = parse_rsa_private_key(pem_key)?;
        Ok(RealClient {
            app_id: app_id.to_string(),
            installation_id: installation_id.to_string(),
            key: Arc::new(key),
            base: api_base().to_string(),
            http: http::client(TIMEOUT)?,
            token: Mutex::new(None),
        })
    }

    /// The same client calling `base` instead (tests point it at a fake).
    #[must_use]
    pub fn with_base_url(mut self, base: &str) -> RealClient {
        self.base = base.trim_end_matches('/').to_string();
        self
    }

    /// An installation token, minted when none is cached or the cached one is near expiry.
    async fn installation_token(&self) -> Result<String> {
        let now = SystemTime::now();
        if let Ok(cached) = self.token.lock()
            && let Some((tok, exp)) = cached.as_ref()
            && now + TOKEN_REFRESH_MARGIN < *exp
        {
            return Ok(tok.clone());
        }
        let (tok, exp) = self.mint_token().await?;
        if let (Some(exp), Ok(mut cached)) = (exp, self.token.lock()) {
            *cached = Some((tok.clone(), exp));
        }
        Ok(tok)
    }

    /// Exchanges a fresh App JWT for an installation access token.
    async fn mint_token(&self) -> Result<(String, Option<SystemTime>)> {
        let jwt = app_jwt(&self.app_id, &self.key, SystemTime::now())?;
        let url = format!(
            "{}/app/installations/{}/access_tokens",
            self.base, self.installation_id
        );
        let resp = self
            .http
            .post(&url)
            .header("Authorization", format!("Bearer {jwt}"))
            .header("Accept", "application/vnd.github+json")
            .header("X-GitHub-Api-Version", "2022-11-28")
            .header("User-Agent", "tfstackplan")
            .send()
            .await
            .map_err(|e| transport("POST", &url, &e))?;
        let status = resp.status();
        let body = resp.bytes().await.unwrap_or_default();
        if !is_2xx(status) {
            http::log_non_2xx("github", &Method::POST, &url, status.as_u16());
            return Err(Error::msg(format!(
                "github: installation token exchange {}: {}",
                status.as_u16(),
                String::from_utf8_lossy(&body)
            )));
        }
        #[derive(Deserialize)]
        struct Out {
            #[serde(default)]
            token: String,
            #[serde(default)]
            expires_at: Option<String>,
        }
        let out: Out = serde_json::from_slice(&body).map_err(decode_err)?;
        let exp = out
            .expires_at
            .and_then(|s| s.parse::<jiff::Timestamp>().ok())
            .and_then(|t| u64::try_from(t.as_second()).ok())
            .map(|s| UNIX_EPOCH + Duration::from_secs(s));
        Ok((out.token, exp))
    }

    /// An authed GitHub call; `None` sends no body.
    async fn call(&self, method: Method, url: &str, payload: Option<&Value>) -> Result<Vec<u8>> {
        let tok = self
            .installation_token()
            .await
            .map_err(|e| Error::msg(format!("github: resolve token: {e}")))?;
        let mut req = self
            .http
            .request(method.clone(), url)
            .header("Authorization", format!("Bearer {tok}"))
            .header("Accept", "application/vnd.github+json")
            .header("X-GitHub-Api-Version", "2022-11-28")
            .header("User-Agent", "tfstackplan");
        if let Some(p) = payload {
            req = req
                .header("Content-Type", "application/json")
                .body(serde_json::to_vec(p).map_err(decode_err)?);
        }
        let resp = req
            .send()
            .await
            .map_err(|e| transport(method.as_str(), url, &e))?;
        let status = resp.status();
        let body = resp.bytes().await.unwrap_or_default().to_vec();
        if !is_2xx(status) {
            http::log_non_2xx("github", &method, url, status.as_u16());
            return Err(Error::msg(format!(
                "github: {} {}: {}: {}",
                method,
                url,
                status.as_u16(),
                String::from_utf8_lossy(&body)
            )));
        }
        Ok(body)
    }

    fn repo_url(&self, repo: &str, rest: &str) -> Result<String> {
        let (owner, name) = split_repo(repo)?;
        Ok(format!("{}/repos/{owner}/{name}/{rest}", self.base))
    }
}

/// Go's transport error text: `Post "url": cause`.
fn transport(method: &str, url: &str, e: &reqwest::Error) -> Error {
    let m = method.to_ascii_lowercase();
    let mut verb = m.clone();
    if let Some(first) = verb.get_mut(0..1) {
        first.make_ascii_uppercase();
    }
    Error::msg(format!("{verb} \"{url}\": {}", without_url(e)))
}

fn without_url(e: &reqwest::Error) -> String {
    let mut s = e.to_string();
    if let Some(u) = e.url() {
        s = s.replace(&format!(" ({u})"), "");
    }
    s
}

fn decode_err(e: serde_json::Error) -> Error {
    Error::msg(e.to_string())
}

/// Splits `owner/name`.
pub fn split_repo(repo: &str) -> Result<(&str, &str)> {
    match repo.split_once('/') {
        Some((o, n)) if !o.is_empty() && !n.is_empty() => Ok((o, n)),
        _ => Err(Error::msg(format!(
            "github: invalid repo {repo:?} (want owner/name)"
        ))),
    }
}

/// Parses a PEM RSA private key in PKCS#1 or PKCS#8 form (GitHub issues PKCS#1).
pub fn parse_rsa_private_key(data: &[u8]) -> Result<RsaKeyPair> {
    let der = PrivateKeyDer::from_pem_slice(data)
        .map_err(|_no_pem| Error::msg("github: could not decode PEM private key"))?;
    let parsed = match &der {
        PrivateKeyDer::Pkcs1(k) => RsaKeyPair::from_der(k.secret_pkcs1_der()),
        PrivateKeyDer::Pkcs8(k) => RsaKeyPair::from_pkcs8(k.secret_pkcs8_der()),
        PrivateKeyDer::Sec1(_) => return Err(Error::msg("github: PKCS8 key is not RSA")),
        _ => return Err(Error::msg("github: could not decode PEM private key")),
    };
    parsed.map_err(|e| Error::msg(format!("github: parse key as PKCS1 or PKCS8: {e}")))
}

/// Signs `signing_input` with RS256 (RSASSA-PKCS1-v1_5 over SHA-256).
pub(crate) fn sign_rs256(key: &RsaKeyPair, signing_input: &[u8]) -> Result<Vec<u8>> {
    let mut sig = vec![0; key.public_modulus_len()];
    key.sign(
        &RSA_PKCS1_SHA256,
        &SystemRandom::new(),
        signing_input,
        &mut sig,
    )
    .map_err(|e| Error::msg(format!("sign: {e}")))?;
    Ok(sig)
}

/// Go's `appJWT`: an RS256 App JWT, `iat` backdated 60 s against clock skew and `exp` 540 s
/// after `iat` (GitHub caps the lifetime at ten minutes).
pub fn app_jwt(app_id: &str, key: &RsaKeyPair, now: SystemTime) -> Result<String> {
    let now = now
        .duration_since(UNIX_EPOCH)
        .map_err(|e| Error::msg(format!("github: sign app jwt: {e}")))?
        .as_secs();
    let iat = now.saturating_sub(60);
    let header = URL_SAFE_NO_PAD.encode(br#"{"alg":"RS256","typ":"JWT"}"#);
    let claims = json!({"iat": iat, "exp": iat + 540, "iss": app_id});
    let claims = URL_SAFE_NO_PAD.encode(claims.to_string());
    let signing_input = format!("{header}.{claims}");
    let sig = sign_rs256(key, signing_input.as_bytes())
        .map_err(|e| Error::msg(format!("github: sign app jwt: {e}")))?;
    Ok(format!("{signing_input}.{}", URL_SAFE_NO_PAD.encode(sig)))
}

/// The check-run output object: the summary, and the report text when there is one.
fn output(title: &str, summary: &str, text: &str) -> Value {
    let mut out = Map::new();
    out.insert("title".into(), title.into());
    out.insert("summary".into(), summary.into());
    if !text.is_empty() {
        out.insert("text".into(), text.into());
    }
    Value::Object(out)
}

#[async_trait]
impl GitHub for RealClient {
    async fn create_check_run(
        &self,
        repo: &str,
        sha: &str,
        name: &str,
        details_url: &str,
    ) -> Result<i64> {
        let url = self.repo_url(repo, "check-runs")?;
        let mut payload = json!({
            "name": name,
            "head_sha": sha,
            "status": "in_progress",
            "output": output("Terraform plan", "Planning…", ""),
        });
        if !details_url.is_empty() {
            payload["details_url"] = details_url.into();
        }
        let body = self.call(Method::POST, &url, Some(&payload)).await?;
        #[derive(Deserialize)]
        struct Out {
            #[serde(default)]
            id: i64,
        }
        let out: Out = serde_json::from_slice(&body).map_err(decode_err)?;
        Ok(out.id)
    }

    async fn update_check_run(
        &self,
        repo: &str,
        check_run_id: i64,
        u: &CheckRunUpdate,
    ) -> Result<()> {
        let url = self.repo_url(repo, &format!("check-runs/{check_run_id}"))?;
        let mut payload = json!({"output": output(&u.title, &u.summary, &u.text)});
        if !u.details_url.is_empty() {
            payload["details_url"] = u.details_url.as_str().into();
        }
        if !u.conclusion.is_empty() {
            payload["status"] = "completed".into();
            payload["conclusion"] = u.conclusion.as_str().into();
        }
        self.call(Method::PATCH, &url, Some(&payload)).await?;
        Ok(())
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
        let url = self.repo_url(repo, &format!("statuses/{sha}"))?;
        let mut payload = json!({"state": state, "context": context, "description": description});
        if !target_url.is_empty() {
            payload["target_url"] = target_url.into();
        }
        self.call(Method::POST, &url, Some(&payload)).await?;
        Ok(())
    }

    async fn pr_head_sha(&self, repo: &str, pr: i64) -> Result<String> {
        let url = self.repo_url(repo, &format!("pulls/{pr}"))?;
        let body = self.call(Method::GET, &url, None).await?;
        #[derive(Deserialize, Default)]
        struct Head {
            #[serde(default)]
            sha: String,
        }
        #[derive(Deserialize)]
        struct Out {
            #[serde(default)]
            head: Head,
        }
        let out: Out = serde_json::from_slice(&body).map_err(decode_err)?;
        if out.head.sha.is_empty() {
            return Err(Error::msg(format!("github: no head sha for PR #{pr}")));
        }
        Ok(out.head.sha)
    }

    async fn pr_abandoned(&self, repo: &str, pr: i64) -> Result<bool> {
        let url = self.repo_url(repo, &format!("pulls/{pr}"))?;
        let body = self.call(Method::GET, &url, None).await?;
        #[derive(Deserialize)]
        struct Out {
            #[serde(default)]
            state: String,
            #[serde(default)]
            merged: bool,
        }
        let out: Out = serde_json::from_slice(&body).map_err(decode_err)?;
        Ok(out.state == "closed" && !out.merged)
    }

    async fn merge_group_prs(&self, repo: &str, head_sha: &str) -> Result<Vec<i64>> {
        if head_sha.is_empty() {
            return Err(Error::msg("headSHA is required"));
        }
        let url = self.repo_url(repo, &format!("commits/{head_sha}/pulls"))?;
        let body = self.call(Method::GET, &url, None).await?;
        #[derive(Deserialize)]
        struct Pull {
            #[serde(default)]
            number: i64,
        }
        let pulls: Vec<Pull> = serde_json::from_slice(&body).map_err(decode_err)?;
        Ok(pulls.into_iter().map(|p| p.number).collect())
    }

    async fn rerequest_check_run(&self, repo: &str, check_run_id: i64) -> Result<()> {
        let url = self.repo_url(repo, &format!("check-runs/{check_run_id}/rerequest"))?;
        self.call(Method::POST, &url, None).await?;
        Ok(())
    }

    /// A null repository (not visible) or null merge queue (none configured) is an empty
    /// result; GraphQL errors arrive as HTTP 200 with a null repository and land there too.
    async fn merge_queue(&self, repo: &str) -> Result<MergeQueueResult> {
        let (owner, name) = split_repo(repo)?;
        const QUERY: &str = "query($owner:String!,$name:String!){repository(owner:$owner,name:$name){defaultBranchRef{name} mergeQueue{entries(first:50){nodes{position state pullRequest{number}}}}}}";
        let payload = json!({"query": QUERY, "variables": {"owner": owner, "name": name}});
        let url = format!("{}/graphql", self.base);
        let body = self.call(Method::POST, &url, Some(&payload)).await?;
        let out: GqlOut = serde_json::from_slice(&body).map_err(decode_err)?;
        let mut res = MergeQueueResult::default();
        let Some(repo) = out.data.repository else {
            return Ok(res);
        };
        if let Some(r) = repo.default_branch_ref {
            res.branch = r.name;
        }
        let Some(mq) = repo.merge_queue else {
            return Ok(res);
        };
        res.entries = mq
            .entries
            .nodes
            .into_iter()
            .map(|n| MergeQueueEntry {
                position: n.position,
                pr: n.pull_request.map_or(0, |p| p.number),
                state: n.state,
            })
            .collect();
        Ok(res)
    }
}

#[derive(Deserialize, Default)]
struct GqlOut {
    #[serde(default)]
    data: GqlData,
}

#[derive(Deserialize, Default)]
struct GqlData {
    #[serde(default)]
    repository: Option<GqlRepo>,
}

#[derive(Deserialize)]
struct GqlRepo {
    #[serde(default, rename = "defaultBranchRef")]
    default_branch_ref: Option<GqlRef>,
    #[serde(default, rename = "mergeQueue")]
    merge_queue: Option<GqlQueue>,
}

#[derive(Deserialize)]
struct GqlRef {
    #[serde(default)]
    name: String,
}

#[derive(Deserialize)]
struct GqlQueue {
    #[serde(default)]
    entries: GqlEntries,
}

#[derive(Deserialize, Default)]
struct GqlEntries {
    #[serde(default)]
    nodes: Vec<GqlNode>,
}

#[derive(Deserialize)]
struct GqlNode {
    #[serde(default)]
    position: i64,
    #[serde(default)]
    state: String,
    #[serde(default, rename = "pullRequest")]
    pull_request: Option<GqlPr>,
}

#[derive(Deserialize)]
struct GqlPr {
    #[serde(default)]
    number: i64,
}

#[cfg(test)]
mod tests;
