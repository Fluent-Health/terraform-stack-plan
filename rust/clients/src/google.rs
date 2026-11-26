//! Google credentials the way Go's `golang.org/x/oauth2/google`,
//! `google.golang.org/api/idtoken` and `cloud.google.com/go/compute/metadata`
//! find and use them:
//!
//! - Application Default Credentials: `$GOOGLE_APPLICATION_CREDENTIALS`, else
//!   `$HOME/.config/gcloud/application_default_credentials.json`, else the GCE
//!   metadata server when on Google Cloud.
//! - Credential files of type `authorized_user` (refresh grant at `token_uri`),
//!   `service_account` (RS256 JWT-bearer grant at `token_uri`) and
//!   `impersonated_service_account` (source credentials, then the IAM Credentials
//!   API at `service_account_impersonation_url`).
//! - ID tokens for an audience (`gauth.Source`): a service-account key or
//!   impersonation mints one directly; the metadata server's identity endpoint
//!   on GCE, falling back to `generateIdToken` as the instance's own account (the
//!   Cloud Build path); otherwise the `id_token` riding a user refresh.
//!
//! `external_account` (workload identity federation) is not supported here.
//! Error text follows Go's where the CLI prints it (ADC discovery, token endpoint
//! failures); transport errors carry reqwest's wording.

use std::sync::mpsc;
use std::sync::{Mutex, OnceLock};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use base64::Engine;
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use serde_json::Value;

use crate::TokenFn;

const ADC_SETUP_URL: &str = "https://cloud.google.com/docs/authentication/external/set-up-adc";
const DEFAULT_TOKEN_URL: &str = "https://oauth2.googleapis.com/token";
const IAM_CREDENTIALS_URL: &str = "https://iamcredentials.googleapis.com";
/// The OAuth scope every access token here asks for.
pub const CLOUD_PLATFORM: &str = "https://www.googleapis.com/auth/cloud-platform";
const JWT_BEARER: &str = "urn:ietf:params:oauth:grant-type:jwt-bearer";

/// Go's "nothing found" error.
fn not_found() -> String {
    format!("google: could not find default credentials. See {ADC_SETUP_URL} for more information")
}

/// Where requests go. Production uses [`Endpoints::from_env`]; tests point both
/// at a fake server.
#[derive(Clone, Debug)]
pub struct Endpoints {
    /// Base of the IAM Credentials API.
    pub iam_credentials: String,
    /// The metadata server's host (`$GCE_METADATA_HOST`, else 169.254.169.254).
    pub metadata_host: String,
    /// Whether this process is on Google Cloud (`metadata.OnGCE`).
    pub on_gce: fn() -> bool,
    /// `$GOOGLE_APPLICATION_CREDENTIALS`.
    pub credentials_file: String,
    /// `$HOME`, where the well-known ADC file lives.
    pub home: String,
}

impl Endpoints {
    /// The real endpoints, honouring `$GCE_METADATA_HOST` as Go does.
    pub fn from_env() -> Self {
        let host = std::env::var("GCE_METADATA_HOST").unwrap_or_default();
        Self {
            iam_credentials: IAM_CREDENTIALS_URL.to_string(),
            metadata_host: if host.is_empty() {
                "169.254.169.254".to_string()
            } else {
                host
            },
            on_gce,
            credentials_file: std::env::var("GOOGLE_APPLICATION_CREDENTIALS").unwrap_or_default(),
            home: std::env::var("HOME").unwrap_or_default(),
        }
    }
}

/// A token, when it expires, and how long before that it is refreshed.
#[derive(Clone, Debug)]
struct Token {
    value: String,
    expiry: Option<Instant>,
    id_token: Option<String>,
    early: Duration,
}

/// How early each Go library refreshes: `golang.org/x/oauth2` (access tokens, user
/// credentials) 10 s; `cloud.google.com/go/auth` ID tokens 225 s (`defaultExpiryDelta`);
/// its metadata-server ID tokens 5 min (`ExpireEarly`).
const OAUTH2_EARLY: Duration = Duration::from_secs(10);
const AUTH_ID_TOKEN_EARLY: Duration = Duration::from_secs(225);
const METADATA_ID_TOKEN_EARLY: Duration = Duration::from_secs(5 * 60);
const HOUR: Duration = Duration::from_secs(3600);

/// When a JWT stops being valid, from its `exp` claim (seconds since the epoch). An
/// `exp` already past gives "now"; a missing or unreadable one gives `None`.
fn jwt_expiry(jwt: &str) -> Option<Instant> {
    let payload = jwt.split('.').nth(1)?;
    let claims: Value = serde_json::from_slice(&URL_SAFE_NO_PAD.decode(payload).ok()?).ok()?;
    let exp = claims.get("exp")?.as_u64()?;
    Some(Instant::now() + Duration::from_secs(exp.saturating_sub(now_secs())))
}

/// Wraps a fetch in Go's reuse-until-expiry cache: a token is reused until `early` before
/// its expiry (forever when it has none).
fn reuse(
    fetch: Box<dyn Fn() -> Result<Token, String> + Send + Sync>,
    first: Option<Token>,
    pick: fn(&Token) -> Option<String>,
) -> TokenFn {
    let cache = Mutex::new(first);
    Box::new(move || {
        let mut cur = cache
            .lock()
            .map_err(|e| format!("token cache poisoned: {e}"))?;
        let fresh = cur
            .as_ref()
            .is_some_and(|t| t.expiry.is_none_or(|e| e > Instant::now() + t.early));
        if !fresh {
            *cur = Some(fetch()?);
        }
        let t = cur.as_ref().ok_or("no token")?;
        pick(t).ok_or_else(|| {
            "credentials carry no ID token (re-run `gcloud auth application-default login`)"
                .to_string()
        })
    })
}

fn access(t: &Token) -> Option<String> {
    Some(t.value.clone())
}

fn id_from_extra(t: &Token) -> Option<String> {
    t.id_token.clone().filter(|s| !s.is_empty())
}

/// A credentials file: its parsed JSON.
#[derive(Clone, Debug)]
pub struct CredFile(Value);

impl CredFile {
    fn str(&self, k: &str) -> String {
        self.0
            .get(k)
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_string()
    }

    fn kind(&self) -> String {
        self.str("type")
    }

    fn token_uri(&self) -> String {
        let t = self.str("token_uri");
        if t.is_empty() {
            DEFAULT_TOKEN_URL.to_string()
        } else {
            t
        }
    }
}

/// Reads ADC from the environment variable or the well-known file. `Ok(None)`
/// means neither exists (the metadata server is the caller's next stop).
pub fn find_file(ep: &Endpoints) -> Result<Option<CredFile>, String> {
    let env = &ep.credentials_file;
    if !env.is_empty() {
        let wrap = |e: String| {
            format!(
                "google: error getting credentials using GOOGLE_APPLICATION_CREDENTIALS environment variable: {e}"
            )
        };
        let data = std::fs::read(env).map_err(|e| wrap(os_err("open", env, &e)))?;
        let f = parse_file(&data).map_err(wrap)?;
        return Ok(Some(f));
    }
    if ep.home.is_empty() {
        return Ok(None);
    }
    let path =
        std::path::Path::new(&ep.home).join(".config/gcloud/application_default_credentials.json");
    match std::fs::read(&path) {
        Ok(data) => parse_file(&data).map(Some),
        Err(_) => Ok(None),
    }
}

fn os_err(op: &str, path: &str, e: &std::io::Error) -> String {
    let text = match e.kind() {
        std::io::ErrorKind::NotFound => "no such file or directory".to_string(),
        std::io::ErrorKind::PermissionDenied => "permission denied".to_string(),
        std::io::ErrorKind::IsADirectory => "is a directory".to_string(),
        _ => e.to_string(),
    };
    format!("{op} {path}: {text}")
}

fn parse_file(data: &[u8]) -> Result<CredFile, String> {
    let v: Value = serde_json::from_slice(data).map_err(|e| e.to_string())?;
    let f = CredFile(v);
    match f.kind().as_str() {
        "" => Err("missing 'type' field in credentials".to_string()),
        "service_account" | "authorized_user" | "impersonated_service_account" => Ok(f),
        "external_account" | "external_account_authorized_user" | "gdch_service_account" => {
            Err(format!(
                "credential type {} is not supported by the Rust port",
                tfstackplan_cli::gofmt::quote(&f.kind())
            ))
        }
        other => Err(format!(
            "unknown credential type: {}",
            tfstackplan_cli::gofmt::quote(other)
        )),
    }
}

/// The token endpoint's reply, or Go's `RetrieveError` text.
fn token_post(
    http: &reqwest::blocking::Client,
    url: &str,
    form: &[(&str, &str)],
) -> Result<Value, String> {
    let resp = http.post(url).form(form).send().map_err(|e| {
        format!(
            "oauth2: cannot fetch token: {}",
            crate::server::transport(&e)
        )
    })?;
    let status = resp.status();
    let body = resp.text().unwrap_or_default();
    if !status.is_success() {
        let parsed: Option<Value> = serde_json::from_str(&body).ok();
        let field = |k: &str| {
            parsed
                .as_ref()
                .and_then(|v| v.get(k))
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_string()
        };
        let code = field("error");
        if !code.is_empty() {
            let mut s = format!("oauth2: {}", tfstackplan_cli::gofmt::quote(&code));
            for k in ["error_description", "error_uri"] {
                let v = field(k);
                if !v.is_empty() {
                    s.push_str(&format!(" {}", tfstackplan_cli::gofmt::quote(&v)));
                }
            }
            return Err(s);
        }
        return Err(format!(
            "oauth2: cannot fetch token: {} {}\nResponse: {body}",
            status.as_u16(),
            status.canonical_reason().unwrap_or_default()
        ));
    }
    serde_json::from_str(&body).map_err(|e| format!("oauth2: cannot parse json: {e}"))
}

fn expiry_of(v: &Value) -> Option<Instant> {
    v.get("expires_in")
        .and_then(Value::as_u64)
        .filter(|s| *s > 0)
        .map(|s| Instant::now() + Duration::from_secs(s))
}

fn token_from(v: &Value, key: &str) -> Result<Token, String> {
    let value = v
        .get(key)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_string();
    if value.is_empty() {
        return Err(format!("oauth2: server response missing {key}"));
    }
    Ok(Token {
        value,
        expiry: expiry_of(v),
        id_token: v.get("id_token").and_then(Value::as_str).map(str::to_owned),
        early: OAUTH2_EARLY,
    })
}

/// A service account's ID token from the token endpoint. The reply carries no
/// `expires_in`, so, as `cloud.google.com/go/auth` does, the expiry is the token's own
/// `exp` claim, refreshed 225 s early.
fn id_token_from(v: &Value) -> Result<Token, String> {
    let mut t = token_from(v, "id_token")?;
    if let Some(e) = jwt_expiry(&t.value) {
        t.expiry = Some(e);
    }
    t.early = AUTH_ID_TOKEN_EARLY;
    Ok(t)
}

fn now_secs() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or_default()
}

/// An RS256-signed JWT for a service account key (the JWT-bearer assertion).
fn sign_jwt(f: &CredFile, claims: &Value) -> Result<String, String> {
    use rustls_pki_types::PrivateKeyDer;
    use rustls_pki_types::pem::PemObject;
    let key = PrivateKeyDer::from_pem_slice(f.str("private_key").as_bytes()).map_err(|e| {
        format!("private key should be a PEM or plain PKCS1 or PKCS8; parse error: {e}")
    })?;
    let pair = match &key {
        PrivateKeyDer::Pkcs8(k) => {
            aws_lc_rs::signature::RsaKeyPair::from_pkcs8(k.secret_pkcs8_der())
        }
        PrivateKeyDer::Pkcs1(k) => aws_lc_rs::signature::RsaKeyPair::from_der(k.secret_pkcs1_der()),
        _ => return Err("private key is not an RSA key".to_string()),
    }
    .map_err(|e| format!("private key: {e}"))?;
    let mut header = serde_json::json!({"alg": "RS256", "typ": "JWT"});
    let kid = f.str("private_key_id");
    if !kid.is_empty() {
        header["kid"] = Value::String(kid);
    }
    let msg = format!(
        "{}.{}",
        URL_SAFE_NO_PAD.encode(header.to_string()),
        URL_SAFE_NO_PAD.encode(claims.to_string())
    );
    let mut sig = vec![0u8; pair.public_modulus_len()];
    pair.sign(
        &aws_lc_rs::signature::RSA_PKCS1_SHA256,
        &aws_lc_rs::rand::SystemRandom::new(),
        msg.as_bytes(),
        &mut sig,
    )
    .map_err(|e| format!("sign: {e}"))?;
    Ok(format!("{msg}.{}", URL_SAFE_NO_PAD.encode(sig)))
}

/// One token fetch for a credentials file: `scope` for an access token, or
/// `audience` for a service account's ID token (`target_audience` claim).
fn file_fetch(
    http: &reqwest::blocking::Client,
    f: &CredFile,
    scope: &str,
    audience: Option<&str>,
    ep: &Endpoints,
) -> Result<Token, String> {
    match f.kind().as_str() {
        "authorized_user" => {
            let v = token_post(
                http,
                &f.token_uri(),
                &[
                    ("client_id", &f.str("client_id")),
                    ("client_secret", &f.str("client_secret")),
                    ("grant_type", "refresh_token"),
                    ("refresh_token", &f.str("refresh_token")),
                ],
            )?;
            token_from(&v, "access_token")
        }
        "service_account" => {
            let iat = now_secs();
            let mut claims = serde_json::json!({
                "iss": f.str("client_email"),
                "aud": f.token_uri(),
                "iat": iat,
                "exp": iat + 3600,
            });
            match audience {
                Some(a) => claims["target_audience"] = Value::String(a.to_string()),
                None => claims["scope"] = Value::String(scope.to_string()),
            }
            let assertion = sign_jwt(f, &claims)?;
            let v = token_post(
                http,
                &f.token_uri(),
                &[("grant_type", JWT_BEARER), ("assertion", &assertion)],
            )?;
            match audience {
                Some(_) => id_token_from(&v),
                None => token_from(&v, "access_token"),
            }
        }
        "impersonated_service_account" => {
            let source =
                f.0.get("source_credentials")
                    .cloned()
                    .map(CredFile)
                    .ok_or("impersonated_service_account: missing source_credentials")?;
            let src_tok = file_fetch(http, &source, CLOUD_PLATFORM, None, ep)?;
            let url = f.str("service_account_impersonation_url");
            let delegates =
                f.0.get("delegates")
                    .cloned()
                    .unwrap_or(Value::Array(vec![]));
            match audience {
                None => {
                    let body = serde_json::json!({"scope": [scope], "delegates": delegates, "lifetime": "3600s"});
                    let v = iam_post(http, &url, &src_tok.value, &body)?;
                    let value = v
                        .get("accessToken")
                        .and_then(Value::as_str)
                        .unwrap_or_default()
                        .to_string();
                    Ok(Token {
                        value,
                        expiry: Some(Instant::now() + HOUR),
                        id_token: None,
                        early: OAUTH2_EARLY,
                    })
                }
                Some(a) => {
                    let account = impersonated_account(&url);
                    id_token_via_iam(http, ep, &account, a, &src_tok.value)
                }
            }
        }
        other => Err(format!(
            "unknown credential type: {}",
            tfstackplan_cli::gofmt::quote(other)
        )),
    }
}

/// The service account named by an impersonation URL (Go: `filepath.Base`, up to `:`).
fn impersonated_account(url: &str) -> String {
    let base = url
        .trim_end_matches('/')
        .rsplit('/')
        .next()
        .unwrap_or_default();
    base.split(':').next().unwrap_or_default().to_string()
}

fn iam_post(
    http: &reqwest::blocking::Client,
    url: &str,
    token: &str,
    body: &Value,
) -> Result<Value, String> {
    let resp = http
        .post(url)
        .bearer_auth(token)
        .json(body)
        .send()
        .map_err(|e| {
            format!(
                "impersonate: unable to generate token: {}",
                crate::server::transport(&e)
            )
        })?;
    let status = resp.status().as_u16();
    let text = resp.text().unwrap_or_default();
    if !(200..300).contains(&status) {
        return Err(format!("impersonate: status code {status}: {text}"));
    }
    serde_json::from_str(&text).map_err(|e| format!("impersonate: unable to parse response: {e}"))
}

/// `generateIdToken` for `account` with the bearer `token`.
fn id_token_via_iam(
    http: &reqwest::blocking::Client,
    ep: &Endpoints,
    account: &str,
    audience: &str,
    token: &str,
) -> Result<Token, String> {
    let url = format!(
        "{}/v1/projects/-/serviceAccounts/{account}:generateIdToken",
        ep.iam_credentials
    );
    let v = iam_post(
        http,
        &url,
        token,
        &serde_json::json!({"audience": audience, "includeEmail": true}),
    )?;
    let value = v
        .get("token")
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_string();
    // cloud.google.com/go/auth sets an hour from now and refreshes 225 s early.
    Ok(Token {
        value,
        expiry: Some(Instant::now() + HOUR),
        id_token: None,
        early: AUTH_ID_TOKEN_EARLY,
    })
}

/// `metadata.Get(suffix)`. Goes through [`crate::metadata_client`], which, like Go's
/// metadata client, ignores proxy settings.
fn metadata_get(ep: &Endpoints, suffix: &str) -> Result<String, String> {
    let url = format!("http://{}/computeMetadata/v1/{suffix}", ep.metadata_host);
    let resp = crate::metadata_client()?
        .get(url)
        .header("Metadata-Flavor", "Google")
        .send()
        .map_err(|e| crate::server::transport(&e))?;
    let status = resp.status().as_u16();
    let body = resp.text().unwrap_or_default();
    if status == 404 {
        return Err(format!(
            "metadata: GCE metadata {} not defined",
            tfstackplan_cli::gofmt::quote(suffix)
        ));
    }
    if status != 200 {
        return Err(format!("compute: Received {status} `{body}`"));
    }
    Ok(body)
}

fn metadata_access(ep: &Endpoints) -> Result<Token, String> {
    let suffix = format!(
        "instance/service-accounts/default/token?scopes={}",
        tfstackplan_cli::gourl::query_escape(CLOUD_PLATFORM)
    );
    let body = metadata_get(ep, &suffix)?;
    let v: Value = serde_json::from_str(&body).map_err(|e| e.to_string())?;
    token_from(&v, "access_token")
}

fn metadata_identity(ep: &Endpoints, audience: &str) -> Result<Token, String> {
    let suffix = format!(
        "instance/service-accounts/default/identity?audience={}&format=full",
        tfstackplan_cli::gourl::query_escape(audience)
    );
    let value = metadata_get(ep, &suffix)?;
    if value.is_empty() {
        return Err("idtoken: invalid response from metadata service".to_string());
    }
    // cloud.google.com/go/auth's compute ID tokens: an hour, refreshed 5 min early.
    Ok(Token {
        value,
        expiry: Some(Instant::now() + HOUR),
        id_token: None,
        early: METADATA_ID_TOKEN_EARLY,
    })
}

fn client() -> Result<reqwest::blocking::Client, String> {
    crate::http_client(Duration::from_secs(30))
}

/// cloud-platform access tokens from ADC (Go's `gcpCreds`); errors are prefixed
/// `application default credentials: ` as there.
pub fn access_token_source() -> Result<TokenFn, String> {
    access_token_source_with(Endpoints::from_env())
        .map_err(|e| format!("application default credentials: {e}"))
}

/// [`access_token_source`] against explicit endpoints.
pub fn access_token_source_with(ep: Endpoints) -> Result<TokenFn, String> {
    let http = client()?;
    match find_file(&ep)? {
        Some(f) => Ok(reuse(
            Box::new(move || file_fetch(&http, &f, CLOUD_PLATFORM, None, &ep)),
            None,
            access,
        )),
        None if (ep.on_gce)() => Ok(reuse(Box::new(move || metadata_access(&ep)), None, access)),
        None => Err(not_found()),
    }
}

/// ID tokens for `audience` from ADC, discovery bounded to 10 s (Go's
/// `gauth.SourceTimeout(10*time.Second, audience)`).
pub fn id_token_source(audience: &str) -> Result<TokenFn, String> {
    let (tx, rx) = mpsc::channel();
    let aud = audience.to_string();
    std::thread::spawn(move || {
        let _ = tx.send(id_token_source_with(&aud, Endpoints::from_env()));
    });
    match rx.recv_timeout(Duration::from_secs(10)) {
        Ok(r) => r,
        Err(_) => Err("google credential discovery timed out after 10s".to_string()),
    }
}

/// `gauth.Source`: the ID-token strategies in Go's order.
pub fn id_token_source_with(audience: &str, ep: Endpoints) -> Result<TokenFn, String> {
    let http = client()?;
    let file = find_file(&ep);
    // 1. idtoken.NewTokenSource: a key or impersonation mints directly (fetched
    //    once up front, as Go does); without a file, the metadata identity endpoint.
    if let Ok(Some(f)) = &file {
        if matches!(
            f.kind().as_str(),
            "service_account" | "impersonated_service_account"
        ) {
            let aud = audience.to_string();
            if let Ok(first) = file_fetch(&http, f, CLOUD_PLATFORM, Some(&aud), &ep) {
                let f = f.clone();
                let ep2 = ep.clone();
                let http2 = http.clone();
                return Ok(reuse(
                    Box::new(move || file_fetch(&http2, &f, CLOUD_PLATFORM, Some(&aud), &ep2)),
                    Some(first),
                    access,
                ));
            }
        }
    } else if let Ok(None) = &file
        && (ep.on_gce)()
        && let Ok(first) = metadata_identity(&ep, audience)
    {
        let aud = audience.to_string();
        let ep2 = ep.clone();
        return Ok(reuse(
            Box::new(move || metadata_identity(&ep2, &aud)),
            Some(first),
            access,
        ));
    }
    // 2. On GCE without a usable identity endpoint (Cloud Build): generateIdToken
    //    as the instance's own service account.
    if (ep.on_gce)() {
        let email = metadata_get(&ep, "instance/service-accounts/default/email")
            .map(|s| s.trim().to_string())
            .map_err(|e| {
                format!("metadata identity endpoint unavailable and SA email lookup failed: {e}")
            })?;
        let aud = audience.to_string();
        return Ok(reuse(
            Box::new(move || {
                let tok = match find_file(&ep)? {
                    Some(f) => file_fetch(&http, &f, CLOUD_PLATFORM, None, &ep)?,
                    None => metadata_access(&ep)?,
                };
                id_token_via_iam(&http, &ep, &email, &aud, &tok.value)
            }),
            None,
            access,
        ));
    }
    // 3. Any ADC; the ID token rides the refresh response (user credentials).
    match file? {
        Some(f) => Ok(reuse(
            Box::new(move || file_fetch(&http, &f, "openid email", None, &ep)),
            None,
            id_from_extra,
        )),
        None => Err(not_found()),
    }
}

/// Go's `metadata.OnGCE`, cached for the process: `$GCE_METADATA_HOST` set, or a
/// probe of the metadata IP and its DNS name answers like Google.
pub fn on_gce() -> bool {
    static ON: OnceLock<bool> = OnceLock::new();
    *ON.get_or_init(probe_gce)
}

fn probe_gce() -> bool {
    if !std::env::var("GCE_METADATA_HOST")
        .unwrap_or_default()
        .is_empty()
    {
        return true;
    }
    let (tx, rx) = mpsc::channel::<bool>();
    let tx2 = tx.clone();
    std::thread::spawn(move || {
        let ok = crate::metadata_client().ok().and_then(|c| {
            c.get("http://169.254.169.254").send().ok().map(|r| {
                r.headers()
                    .get("Metadata-Flavor")
                    .is_some_and(|v| v == "Google")
            })
        });
        let _ = tx.send(ok.unwrap_or(false));
    });
    std::thread::spawn(move || {
        use std::net::ToSocketAddrs;
        let ok = ("metadata.google.internal.", 80)
            .to_socket_addrs()
            .map(|mut a| a.any(|s| s.ip().to_string() == "169.254.169.254"))
            .unwrap_or(false);
        let _ = tx2.send(ok);
    });
    let product = std::fs::read_to_string("/sys/class/dmi/id/product_name").unwrap_or_default();
    let product = product.trim();
    if product == "Google" || product == "Google Compute Engine" {
        if rx.recv().unwrap_or(false) {
            return true;
        }
        return rx.recv_timeout(Duration::from_secs(5)).unwrap_or(false);
    }
    rx.recv().unwrap_or(false)
}

#[cfg(test)]
mod tests;
