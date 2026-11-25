//! Google credentials for the GCP clients (Go: `cmd/tfstackplan/gcpcreds.go`, plus the parts
//! of `golang.org/x/oauth2/google` and `cloud.google.com/go/compute/metadata` it leans on).
//!
//! Application Default Credentials are found the way Go finds them: the file named by
//! `GOOGLE_APPLICATION_CREDENTIALS`, else gcloud's well-known file, else the metadata server.
//! Supported files are `service_account`, `authorized_user` and
//! `impersonated_service_account`; `external_account` (workload identity federation) is not.
//! Every endpoint is a field of [`Env`], so tests point them at local fakes.

use std::path::PathBuf;
use std::sync::Arc;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use async_trait::async_trait;
use aws_lc_rs::signature::RsaKeyPair;
use base64::Engine as _;
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use reqwest::Method;
use rustls_pki_types::PrivateKeyDer;
use rustls_pki_types::pem::PemObject as _;
use serde::Deserialize;
use serde_json::{Value, json};

use crate::github::app::sign_rs256;
use crate::http::{self, is_2xx, query_escape};
use crate::{Error, Impersonator, Result, TokenFunc, TokenSource};

/// The OAuth scope every GCP client here asks for.
pub const CLOUD_PLATFORM_SCOPE: &str = "https://www.googleapis.com/auth/cloud-platform";

/// The public IAM Credentials API.
pub const DEFAULT_IAMCREDENTIALS_URL: &str = "https://iamcredentials.googleapis.com";

/// The metadata server's documented address.
const METADATA_IP: &str = "169.254.169.254";

/// gcloud's OAuth token endpoint, for `authorized_user` files without a `token_uri`.
const DEFAULT_TOKEN_URI: &str = "https://oauth2.googleapis.com/token";

/// A cached token is refreshed this long before it expires (oauth2's `expiryDelta`).
const EXPIRY_DELTA: Duration = Duration::from_secs(10);

/// How long one credential call may take.
const TIMEOUT: Duration = Duration::from_secs(30);

/// Where credentials come from. [`Env::from_env`] reads the process environment; tests build
/// one by hand.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Env {
    /// `GOOGLE_APPLICATION_CREDENTIALS`, when set and non-empty.
    pub credentials_file: Option<PathBuf>,
    /// gcloud's `application_default_credentials.json`, used when it exists.
    pub well_known_file: Option<PathBuf>,
    /// `GCE_METADATA_HOST`. Set means "on GCE" without probing, as in Go.
    pub metadata_host: Option<String>,
    /// The IAM Credentials API base; empty means [`DEFAULT_IAMCREDENTIALS_URL`].
    pub iamcredentials_url: String,
}

impl Env {
    /// The process environment.
    pub fn from_env() -> Env {
        let non_empty = |k: &str| std::env::var(k).ok().filter(|v| !v.is_empty());
        let gcloud_dir = non_empty("CLOUDSDK_CONFIG")
            .map(PathBuf::from)
            .or_else(|| non_empty("HOME").map(|h| PathBuf::from(h).join(".config/gcloud")));
        Env {
            credentials_file: non_empty("GOOGLE_APPLICATION_CREDENTIALS").map(PathBuf::from),
            well_known_file: gcloud_dir.map(|d| d.join("application_default_credentials.json")),
            metadata_host: non_empty("GCE_METADATA_HOST"),
            iamcredentials_url: String::new(),
        }
    }

    pub(crate) fn iam_base(&self) -> &str {
        if self.iamcredentials_url.is_empty() {
            DEFAULT_IAMCREDENTIALS_URL
        } else {
            self.iamcredentials_url.trim_end_matches('/')
        }
    }

    pub(crate) fn metadata_base(&self) -> String {
        format!(
            "http://{}/computeMetadata/v1",
            self.metadata_host.as_deref().unwrap_or(METADATA_IP)
        )
    }
}

/// A parsed credentials file.
#[derive(Clone)]
pub(crate) enum FileCreds {
    ServiceAccount(Arc<ServiceAccountKey>),
    AuthorizedUser(Arc<UserCreds>),
    /// Acts as `target` using `source`'s token.
    Impersonated {
        source: Box<FileCreds>,
        target: String,
    },
}

pub(crate) struct ServiceAccountKey {
    pub email: String,
    pub key_id: String,
    pub key: RsaKeyPair,
    pub token_uri: String,
}

#[derive(Deserialize)]
pub(crate) struct UserCreds {
    #[serde(default)]
    client_id: String,
    #[serde(default)]
    client_secret: String,
    #[serde(default)]
    refresh_token: String,
    #[serde(default)]
    token_uri: String,
}

/// Where ADC resolved to.
#[derive(Clone)]
pub(crate) enum Adc {
    File(FileCreds),
    Metadata,
}

/// Parses a credentials file's JSON.
pub(crate) fn parse_creds(data: &[u8]) -> Result<FileCreds> {
    let v: Value = serde_json::from_slice(data).map_err(|e| Error::msg(e.to_string()))?;
    let typ = v.get("type").and_then(Value::as_str).unwrap_or("");
    match typ {
        "service_account" => {
            let s = |k: &str| v.get(k).and_then(Value::as_str).unwrap_or("").to_string();
            let der = PrivateKeyDer::from_pem_slice(s("private_key").as_bytes()).map_err(|e| {
                Error::msg(format!("google: could not parse service account key: {e}"))
            })?;
            let key = match &der {
                PrivateKeyDer::Pkcs8(k) => RsaKeyPair::from_pkcs8(k.secret_pkcs8_der()),
                PrivateKeyDer::Pkcs1(k) => RsaKeyPair::from_der(k.secret_pkcs1_der()),
                _ => return Err(Error::msg("google: service account key is not RSA")),
            }
            .map_err(|e| Error::msg(format!("google: could not parse service account key: {e}")))?;
            let mut token_uri = s("token_uri");
            if token_uri.is_empty() {
                token_uri = DEFAULT_TOKEN_URI.to_string();
            }
            Ok(FileCreds::ServiceAccount(Arc::new(ServiceAccountKey {
                email: s("client_email"),
                key_id: s("private_key_id"),
                key,
                token_uri,
            })))
        }
        "authorized_user" => {
            let mut u: UserCreds =
                serde_json::from_value(v).map_err(|e| Error::msg(e.to_string()))?;
            if u.token_uri.is_empty() {
                u.token_uri = DEFAULT_TOKEN_URI.to_string();
            }
            Ok(FileCreds::AuthorizedUser(Arc::new(u)))
        }
        "impersonated_service_account" => {
            // Only the account is taken from the URL (as Go's idtoken does); the call always
            // goes to the configured IAM Credentials endpoint, never to a host the file names.
            let url = v
                .get("service_account_impersonation_url")
                .and_then(Value::as_str)
                .unwrap_or("");
            let base = url.rsplit('/').next().unwrap_or("");
            let target = base.split(':').next().unwrap_or("").to_string();
            let src = v.get("source_credentials").cloned().unwrap_or(Value::Null);
            let source = parse_creds(src.to_string().as_bytes())?;
            if target.is_empty() {
                return Err(Error::msg(
                    "google: impersonated credentials name no service account",
                ));
            }
            Ok(FileCreds::Impersonated {
                source: Box::new(source),
                target,
            })
        }
        other => Err(Error::msg(format!(
            "google: unsupported credentials type: {other:?}"
        ))),
    }
}

/// The ADC credentials file, if any: `Ok(None)` when no file applies.
pub(crate) fn find_file_creds(env: &Env) -> Result<Option<FileCreds>> {
    if let Some(path) = &env.credentials_file {
        let data = std::fs::read(path).map_err(|e| {
            Error::msg(format!(
                "google: error getting credentials using GOOGLE_APPLICATION_CREDENTIALS environment variable: open {}: {e}",
                path.display()
            ))
        })?;
        return parse_creds(&data).map(Some).map_err(|e| {
            Error::msg(format!(
                "google: error getting credentials using GOOGLE_APPLICATION_CREDENTIALS environment variable: {e}"
            ))
        });
    }
    if let Some(path) = &env.well_known_file
        && let Ok(data) = std::fs::read(path)
    {
        return parse_creds(&data).map(Some).map_err(|e| {
            Error::msg(format!(
                "google: error getting credentials using well-known file ({}): {e}",
                path.display()
            ))
        });
    }
    Ok(None)
}

/// Go's `google.FindDefaultCredentials`.
pub(crate) async fn find_default(env: &Env, hc: &reqwest::Client) -> Result<Adc> {
    if let Some(f) = find_file_creds(env)? {
        return Ok(Adc::File(f));
    }
    if on_gce(env, hc).await {
        return Ok(Adc::Metadata);
    }
    Err(Error::msg(
        "google: could not find default credentials. See https://cloud.google.com/docs/authentication/external/set-up-adc for more information",
    ))
}

/// Go's `metadata.OnGCE`: true when `GCE_METADATA_HOST` is set; otherwise an HTTP probe of
/// the metadata IP and a DNS lookup of `metadata.google.internal` race. When the BIOS says
/// Google, a "no" waits up to five seconds for the other probe; otherwise the first answer
/// wins.
pub(crate) async fn on_gce(env: &Env, hc: &reqwest::Client) -> bool {
    if env.metadata_host.is_some() {
        return true;
    }
    let http_probe = async {
        hc.get(format!("http://{METADATA_IP}"))
            .timeout(Duration::from_secs(2))
            .send()
            .await
            .ok()
            .and_then(|r| {
                r.headers()
                    .get("Metadata-Flavor")
                    .map(|v| v.as_bytes() == b"Google")
            })
            .unwrap_or(false)
    };
    let dns_probe = async {
        tokio::net::lookup_host("metadata.google.internal.:80")
            .await
            .map(|mut a| a.any(|s| s.ip().to_string() == METADATA_IP))
            .unwrap_or(false)
    };
    let bios_says_gce = std::fs::read_to_string("/sys/class/dmi/id/product_name")
        .map(|s| s.trim().starts_with("Google"))
        .unwrap_or(false);
    tokio::pin!(http_probe, dns_probe);
    let (first, other_is_http) = tokio::select! {
        r = &mut http_probe => (r, false),
        r = &mut dns_probe => (r, true),
    };
    if first || !bios_says_gce {
        return first;
    }
    let other = async {
        if other_is_http {
            http_probe.await
        } else {
            dns_probe.await
        }
    };
    tokio::time::timeout(Duration::from_secs(5), other)
        .await
        .unwrap_or(false)
}

/// A fetched token and when it stops being valid (`None`: no known expiry).
pub(crate) struct Fetched {
    pub value: String,
    pub expires: Option<Instant>,
}

/// A token flow. [`Cached`] wraps one.
#[derive(Clone)]
pub(crate) enum Flow {
    /// An access token for `scope` from a credentials file.
    FileAccess { creds: FileCreds, scope: String },
    /// An access token for `scope` from the metadata server.
    MetadataAccess { scope: String },
    /// An access token for `target` via IAM Credentials `generateAccessToken`.
    ImpersonateAccess { base: TokenFunc, target: String },
    /// An ID token for `audience` from a service-account key (JWT-bearer, `target_audience`).
    SaIdToken {
        key: Arc<ServiceAccountKey>,
        audience: String,
    },
    /// An ID token for `audience` from the metadata server's identity endpoint.
    MetadataIdToken { audience: String },
    /// An ID token for `target` via IAM Credentials `generateIdToken`, `includeEmail` on.
    ImpersonateIdToken {
        base: TokenFunc,
        target: String,
        audience: String,
    },
    /// The `id_token` that rides a user refresh grant.
    UserIdToken { creds: Arc<UserCreds> },
}

/// A [`Flow`] whose result is reused until [`EXPIRY_DELTA`] before it expires (Go's
/// `oauth2.ReuseTokenSource`). Concurrent callers wait for one fetch.
pub(crate) struct Cached {
    flow: Flow,
    env: Env,
    hc: reqwest::Client,
    slot: tokio::sync::Mutex<Option<(String, Option<Instant>)>>,
}

impl Cached {
    pub(crate) fn new(flow: Flow, env: &Env, hc: &reqwest::Client) -> Cached {
        Cached {
            flow,
            env: env.clone(),
            hc: hc.clone(),
            slot: tokio::sync::Mutex::new(None),
        }
    }

    /// Seeds the cache with an already fetched token.
    pub(crate) fn seeded(self, t: Fetched) -> Cached {
        Cached {
            slot: tokio::sync::Mutex::new(Some((t.value, t.expires))),
            ..self
        }
    }
}

#[async_trait]
impl TokenSource for Cached {
    async fn token(&self) -> Result<String> {
        let mut slot = self.slot.lock().await;
        if let Some((v, exp)) = slot.as_ref() {
            let fresh = exp.is_none_or(|e| Instant::now() + EXPIRY_DELTA < e);
            if fresh {
                return Ok(v.clone());
            }
        }
        let t = fetch(&self.flow, &self.env, &self.hc).await?;
        *slot = Some((t.value.clone(), t.expires));
        Ok(t.value)
    }
}

/// Runs one flow once.
pub(crate) async fn fetch(flow: &Flow, env: &Env, hc: &reqwest::Client) -> Result<Fetched> {
    match flow {
        Flow::FileAccess { creds, scope } => file_access(creds, scope, env, hc).await,
        Flow::MetadataAccess { scope } => {
            let body = metadata_get(
                env,
                hc,
                &format!(
                    "instance/service-accounts/default/token?scopes={}",
                    query_escape(scope)
                ),
            )
            .await?;
            let r: TokenResponse =
                serde_json::from_str(&body).map_err(|e| Error::msg(e.to_string()))?;
            Ok(r.access())
        }
        Flow::ImpersonateAccess { base, target } => {
            let tok = base.token().await?;
            let url = format!(
                "{}/v1/projects/-/serviceAccounts/{target}:generateAccessToken",
                env.iam_base()
            );
            let v = google_post(hc, &url, &tok, &json!({"scope": [CLOUD_PLATFORM_SCOPE]})).await?;
            let value = v.get("accessToken").and_then(Value::as_str).unwrap_or("");
            let expires = v
                .get("expireTime")
                .and_then(Value::as_str)
                .and_then(|s| s.parse::<jiff::Timestamp>().ok())
                .and_then(|t| instant_at(t.as_second()));
            Ok(Fetched {
                value: value.to_string(),
                expires,
            })
        }
        Flow::SaIdToken { key, audience } => {
            let r = jwt_bearer(hc, key, "", Some(audience)).await?;
            if r.id_token.is_empty() {
                return Err(Error::msg("oauth2: response doesn't have JWT token"));
            }
            let expires = jwt_expiry(&r.id_token);
            Ok(Fetched {
                value: r.id_token,
                expires,
            })
        }
        Flow::MetadataIdToken { audience } => {
            let suffix = format!(
                "instance/service-accounts/default/identity?audience={}&format=full",
                query_escape(audience)
            );
            let tok = metadata_get(env, hc, &suffix).await?;
            if tok.is_empty() {
                return Err(Error::msg(
                    "idtoken: invalid response from metadata service",
                ));
            }
            // Compute tokens live an hour; leave a little buffer, as Go does.
            Ok(Fetched {
                value: tok,
                expires: Some(Instant::now() + Duration::from_secs(55 * 60)),
            })
        }
        Flow::ImpersonateIdToken {
            base,
            target,
            audience,
        } => {
            let tok = base.token().await?;
            let url = format!(
                "{}/v1/projects/-/serviceAccounts/{target}:generateIdToken",
                env.iam_base()
            );
            let v = google_post(
                hc,
                &url,
                &tok,
                &json!({"audience": audience, "includeEmail": true}),
            )
            .await?;
            let value = v
                .get("token")
                .and_then(Value::as_str)
                .unwrap_or("")
                .to_string();
            let expires = jwt_expiry(&value);
            Ok(Fetched { value, expires })
        }
        Flow::UserIdToken { creds } => {
            let r = refresh_grant(hc, creds).await?;
            let expires = r
                .expires_in
                .map(|s| Instant::now() + Duration::from_secs(s));
            Ok(Fetched {
                value: r.id_token,
                expires,
            })
        }
    }
}

fn instant_at(unix_secs: i64) -> Option<Instant> {
    let at = UNIX_EPOCH + Duration::from_secs(u64::try_from(unix_secs).ok()?);
    let left = at.duration_since(SystemTime::now()).unwrap_or_default();
    Some(Instant::now() + left)
}

/// When a JWT's `exp` claim says it expires (unverified; only for cache timing).
pub(crate) fn jwt_expiry(jwt: &str) -> Option<Instant> {
    let payload = jwt.split('.').nth(1)?;
    let v: Value = serde_json::from_slice(&URL_SAFE_NO_PAD.decode(payload).ok()?).ok()?;
    instant_at(v.get("exp")?.as_i64()?)
}

async fn file_access(
    creds: &FileCreds,
    scope: &str,
    env: &Env,
    hc: &reqwest::Client,
) -> Result<Fetched> {
    match creds {
        FileCreds::ServiceAccount(key) => Ok(jwt_bearer(hc, key, scope, None).await?.access()),
        FileCreds::AuthorizedUser(u) => Ok(refresh_grant(hc, u).await?.access()),
        FileCreds::Impersonated { source, target } => {
            let base: TokenFunc = Arc::new(Cached::new(
                Flow::FileAccess {
                    creds: (**source).clone(),
                    scope: CLOUD_PLATFORM_SCOPE.to_string(),
                },
                env,
                hc,
            ));
            Box::pin(fetch(
                &Flow::ImpersonateAccess {
                    base,
                    target: target.clone(),
                },
                env,
                hc,
            ))
            .await
        }
    }
}

#[derive(Deserialize, Default)]
pub(crate) struct TokenResponse {
    #[serde(default)]
    access_token: String,
    #[serde(default)]
    id_token: String,
    #[serde(default)]
    expires_in: Option<u64>,
}

impl TokenResponse {
    fn access(self) -> Fetched {
        Fetched {
            expires: self
                .expires_in
                .map(|s| Instant::now() + Duration::from_secs(s)),
            value: self.access_token,
        }
    }
}

/// The OAuth2 JWT-bearer grant with a service-account key: `scope` for an access token, or
/// `target_audience` for an ID token.
async fn jwt_bearer(
    hc: &reqwest::Client,
    sa: &ServiceAccountKey,
    scope: &str,
    target_audience: Option<&str>,
) -> Result<TokenResponse> {
    let now = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_err(|e| Error::msg(e.to_string()))?
        .as_secs();
    let header = json!({"alg": "RS256", "typ": "JWT", "kid": sa.key_id});
    let mut claims = json!({"iss": sa.email, "aud": sa.token_uri, "iat": now, "exp": now + 3600});
    if !scope.is_empty() {
        claims["scope"] = scope.into();
    }
    if let Some(aud) = target_audience {
        claims["target_audience"] = aud.into();
    }
    let input = format!(
        "{}.{}",
        URL_SAFE_NO_PAD.encode(header.to_string()),
        URL_SAFE_NO_PAD.encode(claims.to_string())
    );
    let sig = sign_rs256(&sa.key, input.as_bytes())?;
    let assertion = format!("{input}.{}", URL_SAFE_NO_PAD.encode(sig));
    token_endpoint(
        hc,
        &sa.token_uri,
        &[
            ("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer"),
            ("assertion", &assertion),
        ],
    )
    .await
}

async fn refresh_grant(hc: &reqwest::Client, u: &UserCreds) -> Result<TokenResponse> {
    token_endpoint(
        hc,
        &u.token_uri,
        &[
            ("grant_type", "refresh_token"),
            ("client_id", &u.client_id),
            ("client_secret", &u.client_secret),
            ("refresh_token", &u.refresh_token),
        ],
    )
    .await
}

async fn token_endpoint(
    hc: &reqwest::Client,
    url: &str,
    form: &[(&str, &str)],
) -> Result<TokenResponse> {
    let resp = hc
        .post(url)
        .form(form)
        .send()
        .await
        .map_err(|e| Error::msg(format!("oauth2: cannot fetch token: {e}")))?;
    let status = resp.status();
    let body = resp.bytes().await.unwrap_or_default();
    if !is_2xx(status) {
        http::log_non_2xx("google-oauth2", &Method::POST, url, status.as_u16());
        return Err(Error::msg(format!(
            "oauth2: cannot fetch token: {status}\nResponse: {}",
            String::from_utf8_lossy(&body)
        )));
    }
    serde_json::from_slice(&body).map_err(|e| Error::msg(format!("oauth2: cannot parse json: {e}")))
}

/// A metadata server GET, retried on transport errors and 5xx like Go's client.
pub(crate) async fn metadata_get(env: &Env, hc: &reqwest::Client, suffix: &str) -> Result<String> {
    let url = format!("{}/{}", env.metadata_base(), suffix.trim_start_matches('/'));
    let mut delay = Duration::from_millis(100);
    let mut attempt = 0;
    loop {
        attempt += 1;
        let res = hc
            .get(&url)
            .header("Metadata-Flavor", "Google")
            .send()
            .await;
        let retry = match &res {
            Err(_) => true,
            Ok(r) => r.status().is_server_error(),
        };
        if retry && attempt < 5 {
            tokio::time::sleep(delay).await;
            delay *= 2;
            continue;
        }
        let r = res.map_err(|e| Error::msg(format!("metadata: {e}")))?;
        let status = r.status().as_u16();
        let body = r.text().await.unwrap_or_default();
        if status == 404 {
            return Err(Error::msg(format!(
                "metadata: GCE metadata {suffix:?} not defined"
            )));
        }
        if status != 200 {
            http::log_non_2xx("gce-metadata", &Method::GET, &url, status);
            return Err(Error::msg(format!(
                "metadata: GCE metadata {suffix:?} returned status {status}"
            )));
        }
        return Ok(body);
    }
}

/// A Google JSON API POST with a bearer token. A non-2xx answer is Go's `googleapi.Error`
/// text: the API's own error message when the body carries one.
pub(crate) async fn google_post(
    hc: &reqwest::Client,
    url: &str,
    bearer: &str,
    payload: &Value,
) -> Result<Value> {
    let resp = hc
        .post(url)
        .bearer_auth(bearer)
        .json_body(payload)
        .send()
        .await
        .map_err(|e| Error::msg(format!("Post {url:?}: {e}")))?;
    let status = resp.status();
    let body = resp.bytes().await.unwrap_or_default();
    if !is_2xx(status) {
        http::log_non_2xx("google", &Method::POST, url, status.as_u16());
        return Err(Error::msg(googleapi_error(status.as_u16(), &body)));
    }
    serde_json::from_slice(&body).map_err(|e| Error::msg(e.to_string()))
}

/// Go's `googleapi.Error` text.
pub(crate) fn googleapi_error(status: u16, body: &[u8]) -> String {
    let msg = serde_json::from_slice::<Value>(body).ok().and_then(|v| {
        v.pointer("/error/message")
            .and_then(Value::as_str)
            .map(str::to_string)
    });
    match msg {
        Some(m) => format!("googleapi: Error {status}: {m}"),
        None => format!(
            "googleapi: got HTTP response code {status} with body: {}",
            String::from_utf8_lossy(body)
        ),
    }
}

/// `reqwest` JSON bodies without its `json` feature.
trait JsonBody {
    fn json_body(self, v: &Value) -> Self;
}

impl JsonBody for reqwest::RequestBuilder {
    fn json_body(self, v: &Value) -> Self {
        self.header("Content-Type", "application/json")
            .body(v.to_string())
    }
}

/// An access-token source for ADC.
pub(crate) fn adc_access(adc: Adc, scope: &str, env: &Env, hc: &reqwest::Client) -> TokenFunc {
    let flow = match adc {
        Adc::File(creds) => Flow::FileAccess {
            creds,
            scope: scope.to_string(),
        },
        Adc::Metadata => Flow::MetadataAccess {
            scope: scope.to_string(),
        },
    };
    Arc::new(Cached::new(flow, env, hc))
}

/// Go's `gcpCreds`: the ADC access-token source (PAM list and revoke, Cloud Build, GCS) and
/// the service-account impersonation used for PAM create.
pub async fn gcp_creds(env: &Env) -> Result<(TokenFunc, Arc<dyn Impersonator>)> {
    let hc = http::client(TIMEOUT)?;
    let adc = find_default(env, &hc)
        .await
        .map_err(|e| Error::msg(format!("application default credentials: {e}")))?;
    let token = adc_access(adc, CLOUD_PLATFORM_SCOPE, env, &hc);
    let imp = IamImpersonator {
        base: Arc::clone(&token),
        env: env.clone(),
        hc,
    };
    Ok((token, Arc::new(imp)))
}

/// Go's `realMintAccessToken`: an access token for `sa` via IAM Credentials, using ADC.
pub async fn mint_access_token(env: &Env, sa: &str) -> Result<String> {
    let hc = http::client(TIMEOUT)?;
    let wrap = |e: Error| Error::msg(format!("mint access token for {sa}: {e}"));
    let adc = find_default(env, &hc).await.map_err(wrap)?;
    let base = adc_access(adc, CLOUD_PLATFORM_SCOPE, env, &hc);
    let flow = Flow::ImpersonateAccess {
        base,
        target: sa.to_string(),
    };
    fetch(&flow, env, &hc).await.map(|t| t.value).map_err(wrap)
}

/// Impersonates a service account through IAM Credentials with the ADC token.
struct IamImpersonator {
    base: TokenFunc,
    env: Env,
    hc: reqwest::Client,
}

#[async_trait]
impl Impersonator for IamImpersonator {
    async fn impersonate(&self, service_account: &str) -> Result<String> {
        let flow = Flow::ImpersonateAccess {
            base: Arc::clone(&self.base),
            target: service_account.to_string(),
        };
        fetch(&flow, &self.env, &self.hc)
            .await
            .map(|t| t.value)
            .map_err(|e| Error::msg(format!("impersonate {service_account}: {e}")))
    }
}

#[cfg(test)]
mod tests;
