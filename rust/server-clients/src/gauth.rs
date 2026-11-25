//! Google OIDC ID tokens (Go: `internal/gauth`): minting them to call the control plane
//! ([`source`]) and verifying them on the server ([`Verifier`], [`ClaimsVerifier`]).
//!
//! Verification mirrors Go's `google.golang.org/api/idtoken` `Validate` check for check,
//! and is stricter in three places, each of which only rejects tokens Go would accept:
//!
//! - the issuer must be `accounts.google.com` or `https://accounts.google.com` (idtoken
//!   does not look at `iss`);
//! - only RS256 is accepted. idtoken also takes ES256 against IAP's keys, but IAP-signed
//!   tokens carry IAP's issuer, so the issuer rule would reject them anyway;
//! - RSA keys must be 2048 to 8192 bits (Google's are 2048).
//!
//! As in idtoken there is no clock-skew allowance: a token is expired once `now > exp`,
//! and `iat`/`nbf` are not checked. The JWKS is cached for the response's
//! `Cache-Control: max-age` less its `Age`, exactly as idtoken computes it, and in addition
//! is refetched once (at most every minute) when a token names a key id it does not hold.

use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use aws_lc_rs::signature::{RSA_PKCS1_2048_8192_SHA256, RsaPublicKeyComponents};
use base64::Engine as _;
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use reqwest::Method;
use serde::Deserialize;
use serde_json::{Map, Value};

use crate::gcpcreds::{
    self, Adc, Cached, Env, FileCreds, Flow, adc_access, find_default, find_file_creds,
    metadata_get, on_gce,
};
use crate::http::{self};
use crate::{Error, Result, TokenFunc, TokenSource};

#[cfg(any(test, feature = "test-support"))]
pub mod gauthtest;

/// Google's JWKS for the RS256 keys that sign service-account and user ID tokens.
pub const GOOGLE_CERTS_URL: &str = "https://www.googleapis.com/oauth2/v3/certs";

/// The issuers Google writes into ID tokens.
pub const GOOGLE_ISSUERS: [&str; 2] = ["accounts.google.com", "https://accounts.google.com"];

/// A token naming an unknown key id refetches the JWKS at most this often.
const KID_REFETCH_INTERVAL: Duration = Duration::from_secs(60);

/// How long a JWKS fetch or a token mint may take.
const TIMEOUT: Duration = Duration::from_secs(30);

/// The text Go returns when the credentials yield no ID token.
const NO_ID_TOKEN: &str =
    "credentials carry no ID token (re-run `gcloud auth application-default login`)";

/// Go's `ClaimsVerifier`: signature and expiry as idtoken checks them, the issuer, the
/// audience against an allowlist, and the full claims out. Email, `email_verified` and `hd`
/// are the caller's to enforce. Cloning shares the key cache.
#[derive(Clone)]
pub struct ClaimsVerifier {
    inner: Arc<Inner>,
}

struct Inner {
    audiences: Vec<String>,
    certs_url: String,
    hc: reqwest::Client,
    cache: Mutex<Option<CachedCerts>>,
}

#[derive(Clone)]
struct CachedCerts {
    keys: Vec<Jwk>,
    expires: Instant,
    fetched: Instant,
}

#[derive(Clone, Deserialize)]
struct Jwk {
    #[serde(default)]
    kid: String,
    #[serde(default)]
    n: String,
    #[serde(default)]
    e: String,
}

#[derive(Deserialize)]
struct CertResponse {
    #[serde(default)]
    keys: Vec<Jwk>,
}

impl std::fmt::Debug for ClaimsVerifier {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("ClaimsVerifier")
            .field("audiences", &self.inner.audiences)
            .field("certs_url", &self.inner.certs_url)
            .finish_non_exhaustive()
    }
}

impl ClaimsVerifier {
    /// A verifier accepting tokens for any of `audiences`, checked against Google's keys.
    pub fn new(audiences: Vec<String>) -> Result<ClaimsVerifier> {
        ClaimsVerifier::with_certs_url(audiences, GOOGLE_CERTS_URL)
    }

    /// The same, fetching keys from `certs_url` (tests serve their own JWKS).
    pub fn with_certs_url(audiences: Vec<String>, certs_url: &str) -> Result<ClaimsVerifier> {
        Ok(ClaimsVerifier {
            inner: Arc::new(Inner {
                audiences,
                certs_url: certs_url.to_string(),
                hc: http::client(TIMEOUT)?,
                cache: Mutex::new(None),
            }),
        })
    }

    /// Verifies `token` and returns its claims.
    pub async fn verify(&self, token: &str) -> Result<Map<String, Value>> {
        let t = parse(token)?;
        let now = unix_now();
        if now > t.exp {
            return Err(Error::msg(format!(
                "idtoken: token expired: now={now}, expires={}",
                t.exp
            )));
        }
        if t.alg != "RS256" {
            return Err(Error::msg(format!(
                "idtoken: expected JWT signed with RS256 but found {:?}",
                t.alg
            )));
        }
        let key = self.key(&t.kid).await?;
        let n = decode(&key.n)?;
        let e = decode(&key.e)?;
        RsaPublicKeyComponents { n: &n, e: &e }
            .verify(&RSA_PKCS1_2048_8192_SHA256, t.signed.as_bytes(), &t.sig)
            .map_err(|_bad_sig| Error::msg("crypto/rsa: verification error"))?;
        if !GOOGLE_ISSUERS.contains(&t.iss.as_str()) {
            return Err(Error::msg(format!("unexpected issuer {:?}", t.iss)));
        }
        if !self.inner.audiences.contains(&t.aud) {
            return Err(Error::msg(format!("unexpected audience {:?}", t.aud)));
        }
        Ok(t.claims)
    }

    /// The JWK for `kid`, from the cache or a fetch.
    async fn key(&self, kid: &str) -> Result<Jwk> {
        let cached = self.inner.cache.lock().ok().and_then(|c| c.clone());
        let now = Instant::now();
        let certs = match cached {
            Some(c) if now <= c.expires => {
                let stale_for_kid = !c.keys.iter().any(|k| k.kid == kid)
                    && now.duration_since(c.fetched) >= KID_REFETCH_INTERVAL;
                if stale_for_kid {
                    self.fetch_certs().await?
                } else {
                    c
                }
            }
            _ => self.fetch_certs().await?,
        };
        certs
            .keys
            .into_iter()
            .find(|k| k.kid == kid)
            .ok_or_else(|| {
                Error::msg("idtoken: could not find matching cert keyId for the token provided")
            })
    }

    async fn fetch_certs(&self) -> Result<CachedCerts> {
        let url = &self.inner.certs_url;
        let resp = self
            .inner
            .hc
            .get(url)
            .send()
            .await
            .map_err(|e| Error::msg(format!("Get {url:?}: {e}")))?;
        let status = resp.status().as_u16();
        if status != 200 {
            http::log_non_2xx("google-certs", &Method::GET, url, status);
            return Err(Error::msg(format!(
                "idtoken: unable to retrieve cert, got status code {status}"
            )));
        }
        let header = |n: &str| {
            resp.headers()
                .get(n)
                .and_then(|v| v.to_str().ok())
                .unwrap_or("")
                .to_string()
        };
        let ttl = cache_ttl(&header("cache-control"), &header("age"));
        let body = resp.bytes().await.map_err(|e| Error::msg(e.to_string()))?;
        let certs: CertResponse =
            serde_json::from_slice(&body).map_err(|e| Error::msg(e.to_string()))?;
        let now = Instant::now();
        let c = CachedCerts {
            keys: certs.keys,
            expires: now + ttl,
            fetched: now,
        };
        if let Ok(mut slot) = self.inner.cache.lock() {
            *slot = Some(c.clone());
        }
        Ok(c)
    }
}

/// idtoken's `calculateExpireTime` as a duration from now: `max-age` minus `Age`. Anything
/// unreadable expires the entry at once; so does a negative result.
fn cache_ttl(cache_control: &str, age: &str) -> Duration {
    let mut max_age: i64 = 0;
    for v in cache_control.split(',') {
        if v.contains("max-age") {
            let ss: Vec<&str> = v.split('=').collect();
            let Some(n) = ss.get(1).and_then(|s| s.parse::<i64>().ok()) else {
                return Duration::ZERO;
            };
            max_age = n;
        }
    }
    let secs = if age.is_empty() {
        max_age
    } else {
        match age.parse::<i64>() {
            Ok(a) => max_age - a,
            Err(_) => return Duration::ZERO,
        }
    };
    Duration::from_secs(u64::try_from(secs).unwrap_or(0))
}

/// A parsed, unverified token.
struct Parsed {
    alg: String,
    kid: String,
    iss: String,
    aud: String,
    exp: i64,
    claims: Map<String, Value>,
    signed: String,
    sig: Vec<u8>,
}

fn decode(s: &str) -> Result<Vec<u8>> {
    URL_SAFE_NO_PAD
        .decode(s)
        .map_err(|e| Error::msg(e.to_string()))
}

/// idtoken's `parseJWT`, `parsedHeader`, `parsedPayload` and `decodedSignature`, with its
/// error text. Claims typed in Go's `Payload` must have that type when present.
fn parse(token: &str) -> Result<Parsed> {
    let segs: Vec<&str> = token.split('.').collect();
    let [h, p, s] = segs[..] else {
        return Err(Error::msg(format!(
            "idtoken: invalid token, token must have three segments; found {}",
            segs.len()
        )));
    };
    let hb =
        decode(h).map_err(|e| Error::msg(format!("idtoken: unable to decode JWT header: {e}")))?;
    let header: Map<String, Value> = serde_json::from_slice(&hb)
        .map_err(|e| Error::msg(format!("idtoken: unable to unmarshal JWT header: {e}")))?;
    let pb =
        decode(p).map_err(|e| Error::msg(format!("idtoken: unable to decode JWT payload: {e}")))?;
    let claims: Map<String, Value> = serde_json::from_slice(&pb)
        .map_err(|e| Error::msg(format!("idtoken: unable to unmarshal JWT payload: {e}")))?;
    let sig = decode(s)
        .map_err(|e| Error::msg(format!("idtoken: unable to decode JWT signature: {e}")))?;

    let string = |m: &Map<String, Value>, k: &str, what: &str| -> Result<String> {
        match m.get(k) {
            None | Some(Value::Null) => Ok(String::new()),
            Some(Value::String(s)) => Ok(s.clone()),
            Some(_) => Err(Error::msg(format!(
                "idtoken: unable to unmarshal JWT {what}: {k} is not a string"
            ))),
        }
    };
    let int = |k: &str| -> Result<i64> {
        match claims.get(k) {
            None | Some(Value::Null) => Ok(0),
            Some(v) => v.as_i64().ok_or_else(|| {
                Error::msg(format!(
                    "idtoken: unable to unmarshal JWT payload: {k} is not an integer"
                ))
            }),
        }
    };
    let alg = string(&header, "alg", "header")?;
    let kid = string(&header, "kid", "header")?;
    string(&header, "typ", "header")?;
    let iss = string(&claims, "iss", "payload")?;
    let aud = string(&claims, "aud", "payload")?;
    string(&claims, "sub", "payload")?;
    let exp = int("exp")?;
    int("iat")?;
    Ok(Parsed {
        alg,
        kid,
        iss,
        aud,
        exp,
        signed: format!("{h}.{p}"),
        sig,
        claims,
    })
}

fn unix_now() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |d| i64::try_from(d.as_secs()).unwrap_or(i64::MAX))
}

/// Go's `Verifier`: a [`ClaimsVerifier`] that also requires a non-empty `email` claim and
/// rejects `email_verified: false`, returning the email.
#[derive(Clone, Debug)]
pub struct Verifier {
    claims: ClaimsVerifier,
}

impl Verifier {
    /// A verifier accepting tokens for any of `audiences`, checked against Google's keys.
    pub fn new(audiences: Vec<String>) -> Result<Verifier> {
        Ok(Verifier {
            claims: ClaimsVerifier::new(audiences)?,
        })
    }

    /// The same, fetching keys from `certs_url`.
    pub fn with_certs_url(audiences: Vec<String>, certs_url: &str) -> Result<Verifier> {
        Ok(Verifier {
            claims: ClaimsVerifier::with_certs_url(audiences, certs_url)?,
        })
    }

    /// Verifies `token` and returns its verified email.
    pub async fn verify(&self, token: &str) -> Result<String> {
        let claims = self.claims.verify(token).await?;
        let email = claims.get("email").and_then(Value::as_str).unwrap_or("");
        if email.is_empty() {
            return Err(Error::msg("oidc token has no email claim"));
        }
        if claims.get("email_verified") == Some(&Value::Bool(false)) {
            return Err(Error::msg("oidc token email not verified"));
        }
        Ok(email.to_string())
    }
}

/// Go's `Source`: Google ID tokens for `audience` from Application Default Credentials.
///
/// 1. idtoken's own sources: a service-account key file (JWT-bearer with
///    `target_audience`, fetched once now), an impersonated-service-account file (IAM
///    Credentials `generateIdToken`), or the metadata server's identity endpoint (fetched
///    once now).
/// 2. On GCE without a working identity endpoint (Cloud Build): `generateIdToken` for the
///    metadata server's own service account, with its access token.
/// 3. Otherwise the `id_token` riding a user refresh grant (`gcloud auth
///    application-default login`); its audience is gcloud's client id.
pub async fn source(env: &Env, audience: &str) -> Result<TokenFunc> {
    let hc = http::client(TIMEOUT)?;
    if let Ok(ts) = idtoken_source(env, &hc, audience).await {
        return Ok(ts);
    }
    if on_gce(env, &hc).await {
        let email = metadata_get(env, &hc, "instance/service-accounts/default/email")
            .await
            .map_err(|e| {
                Error::msg(format!(
                    "metadata identity endpoint unavailable and SA email lookup failed: {e}"
                ))
            })?;
        let base = find_default(env, &hc)
            .await
            .map(|adc| adc_access(adc, gcpcreds::CLOUD_PLATFORM_SCOPE, env, &hc))
            .map_err(|e| Error::msg(format!("iamcredentials id-token source for {email}: {e}")))?;
        return Ok(self_id_token_source(env, &hc, audience, &email, base));
    }
    match find_default(env, &hc).await? {
        Adc::File(FileCreds::AuthorizedUser(creds)) => Ok(Arc::new(PickIdToken(Cached::new(
            Flow::UserIdToken { creds },
            env,
            &hc,
        )))),
        // Service-account and compute credentials carry no id_token on their grants.
        _ => Ok(Arc::new(|| async {
            Err::<String, _>(Error::msg(NO_ID_TOKEN))
        })),
    }
}

/// Go's `SourceTimeout`: [`source`], giving up after `timeout`.
pub async fn source_timeout(env: &Env, timeout: Duration, audience: &str) -> Result<TokenFunc> {
    tokio::time::timeout(timeout, source(env, audience))
        .await
        .unwrap_or_else(|_elapsed| {
            Err(Error::msg(format!(
                "google credential discovery timed out after {}",
                go_duration(timeout)
            )))
        })
}

/// idtoken's `NewTokenSource`.
async fn idtoken_source(env: &Env, hc: &reqwest::Client, audience: &str) -> Result<TokenFunc> {
    if audience.is_empty() {
        return Err(Error::msg("idtoken: must supply a non-empty audience"));
    }
    let audience = audience.to_string();
    match find_file_creds(env)? {
        Some(FileCreds::ServiceAccount(key)) => {
            let flow = Flow::SaIdToken { key, audience };
            let first = gcpcreds::fetch(&flow, env, hc).await?;
            Ok(Arc::new(Cached::new(flow, env, hc).seeded(first)))
        }
        Some(FileCreds::Impersonated { source, target }) => {
            let base: TokenFunc = Arc::new(Cached::new(
                Flow::FileAccess {
                    creds: *source,
                    scope: gcpcreds::CLOUD_PLATFORM_SCOPE.to_string(),
                },
                env,
                hc,
            ));
            Ok(self_id_token_source(env, hc, &audience, &target, base))
        }
        Some(FileCreds::AuthorizedUser(_)) => Err(Error::msg(
            "idtoken: unsupported credentials type: \"authorized_user\"",
        )),
        None if on_gce(env, hc).await => {
            let flow = Flow::MetadataIdToken { audience };
            let first = gcpcreds::fetch(&flow, env, hc).await?;
            Ok(Arc::new(Cached::new(flow, env, hc).seeded(first)))
        }
        None => Err(Error::msg("idtoken: couldn't find any credentials")),
    }
}

/// Go's `selfIDTokenSource`: ID tokens for `audience` from IAM Credentials
/// `generateIdToken` as `email`, authorised by `base`.
pub(crate) fn self_id_token_source(
    env: &Env,
    hc: &reqwest::Client,
    audience: &str,
    email: &str,
    base: TokenFunc,
) -> TokenFunc {
    Arc::new(PickIdToken(Cached::new(
        Flow::ImpersonateIdToken {
            base,
            target: email.to_string(),
            audience: audience.to_string(),
        },
        env,
        hc,
    )))
}

/// Go's `fromTokenSource`: an empty token is the "no ID token" error.
struct PickIdToken(Cached);

#[async_trait::async_trait]
impl TokenSource for PickIdToken {
    async fn token(&self) -> Result<String> {
        let t = self.0.token().await?;
        if t.is_empty() {
            return Err(Error::msg(NO_ID_TOKEN));
        }
        Ok(t)
    }
}

/// Go's `time.Duration.String` for the durations used here.
fn go_duration(d: Duration) -> String {
    if d < Duration::from_secs(1) {
        return format!("{}ms", d.as_millis());
    }
    let total = d.as_secs();
    let (h, m, s) = (total / 3600, (total % 3600) / 60, total % 60);
    let frac = d.subsec_nanos();
    let mut secs = s.to_string();
    if frac > 0 {
        let f = format!("{frac:09}");
        secs = format!("{s}.{}", f.trim_end_matches('0'));
    }
    match (h, m) {
        (0, 0) => format!("{secs}s"),
        (0, m) => format!("{m}m{secs}s"),
        (h, m) => format!("{h}h{m}m{secs}s"),
    }
}

#[cfg(test)]
mod tests;
