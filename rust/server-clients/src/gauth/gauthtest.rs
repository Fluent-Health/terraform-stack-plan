//! A fake Google OIDC issuer for offline tests (Go: `internal/gauth/gauthtest`): no network,
//! no credentials. Enabled by the `test-support` feature.
//!
//! - [`Issuer::mint_id_token`] signs RS256 ID tokens with a throwaway key;
//! - [`Issuer::serve_jwks`] serves this issuer's JWKS, so a verifier built with
//!   `with_certs_url(.., &server.url)` checks those tokens for real;
//! - [`Issuer::token_endpoint`] and [`Issuer::write_service_account_key`] fake the OAuth2
//!   JWT-bearer grant: a fabricated key file whose `token_uri` points at the local endpoint
//!   lets [`super::source`] mint this issuer's tokens through the real flow.

use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use aws_lc_rs::encoding::AsDer;
use aws_lc_rs::rsa::KeySize;
use aws_lc_rs::signature::{KeyPair as _, RsaKeyPair};
use base64::Engine as _;
use base64::engine::general_purpose::{STANDARD, URL_SAFE_NO_PAD};
use serde_json::{Map, Value, json};

use crate::github::app::sign_rs256;
use crate::testserver::{Resp, TestServer, serve};
use crate::{Error, Result};

/// A fake Google token issuer backed by a throwaway RSA key.
#[derive(Clone, Debug)]
pub struct Issuer {
    pub key: Arc<RsaKeyPair>,
    pub key_id: String,
}

fn b64(b: &[u8]) -> String {
    URL_SAFE_NO_PAD.encode(b)
}

impl Issuer {
    /// Generates the issuer's 2048-bit signing key.
    pub fn new() -> Result<Issuer> {
        let key = RsaKeyPair::generate(KeySize::Rsa2048)
            .map_err(|e| Error::msg(format!("gauthtest: generate key: {e}")))?;
        Ok(Issuer {
            key: Arc::new(key),
            key_id: "gauthtest-key-1".into(),
        })
    }

    /// The issuer's public key in the JWKS shape Google's certs endpoint serves.
    pub fn jwks(&self) -> Vec<u8> {
        let pk = self.key.public_key();
        json!({"keys": [{
            "kty": "RSA", "alg": "RS256", "use": "sig",
            "kid": self.key_id,
            "n": b64(pk.modulus().big_endian_without_leading_zero()),
            "e": b64(pk.exponent().big_endian_without_leading_zero()),
        }]})
        .to_string()
        .into_bytes()
    }

    /// Serves [`Issuer::jwks`] on every path with `Cache-Control: max-age=3600`.
    pub async fn serve_jwks(&self) -> std::io::Result<TestServer> {
        let jwks = self.jwks();
        serve(move |_| {
            Resp::ok(jwks.clone())
                .header("Content-Type", "application/json")
                .header("Cache-Control", "max-age=3600")
        })
        .await
    }

    /// An ID token for `email` and `audience`, issued by `https://accounts.google.com` and
    /// valid for `ttl`.
    pub fn mint_id_token(&self, email: &str, audience: &str, ttl: Duration) -> Result<String> {
        let now = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map_err(|e| Error::msg(e.to_string()))?
            .as_secs();
        let claims = json!({
            "iss": "https://accounts.google.com",
            "aud": audience,
            "sub": "1234567890",
            "email": email,
            "email_verified": true,
            "iat": now,
            "exp": now + ttl.as_secs(),
        });
        self.mint_id_token_claims(claims.as_object().cloned().unwrap_or_default())
    }

    /// A token with exactly `claims`; edge cases (expired, unverified, missing claims) set
    /// them explicitly.
    pub fn mint_id_token_claims(&self, claims: Map<String, Value>) -> Result<String> {
        let header = json!({"alg": "RS256", "typ": "JWT", "kid": self.key_id});
        let signing = format!(
            "{}.{}",
            b64(header.to_string().as_bytes()),
            b64(Value::Object(claims).to_string().as_bytes())
        );
        let sig = sign_rs256(&self.key, signing.as_bytes())?;
        Ok(format!("{signing}.{}", b64(&sig)))
    }

    /// A fake OAuth2 token endpoint for the JWT-bearer grant, as Google answers it for ID
    /// tokens: it reads the (unverified) assertion's `iss` as the service account and
    /// `target_audience` as the audience, and returns an `id_token` from this issuer.
    pub async fn token_endpoint(&self) -> std::io::Result<TestServer> {
        let issuer = self.clone();
        serve(move |r| {
            let assertion = r.param("assertion", true);
            let parts: Vec<&str> = assertion.split('.').collect();
            let Some(payload) = parts.get(1).filter(|_| parts.len() == 3) else {
                return Resp::status(400, "malformed assertion");
            };
            let Some(claims) = URL_SAFE_NO_PAD
                .decode(payload)
                .ok()
                .and_then(|b| serde_json::from_slice::<Value>(&b).ok())
            else {
                return Resp::status(400, "bad assertion payload");
            };
            let get = |k: &str| claims.get(k).and_then(Value::as_str).unwrap_or("");
            let (iss, aud) = (get("iss"), get("target_audience"));
            if iss.is_empty() || aud.is_empty() {
                return Resp::status(400, "assertion missing iss/target_audience");
            }
            match issuer.mint_id_token(iss, aud, Duration::from_secs(3600)) {
                Ok(tok) => Resp::ok(
                    json!({"id_token": tok, "access_token": tok, "token_type": "Bearer", "expires_in": 3600})
                        .to_string(),
                )
                .header("Content-Type", "application/json"),
                Err(e) => Resp::status(500, e.to_string()),
            }
        })
        .await
    }

    /// Writes a fabricated service-account key file whose `token_uri` is `token_url` into
    /// `dir` and returns its path.
    pub fn write_service_account_key(
        &self,
        dir: &Path,
        email: &str,
        token_url: &str,
    ) -> Result<PathBuf> {
        let der = self
            .key
            .as_der()
            .map_err(|e| Error::msg(format!("gauthtest: encode key: {e}")))?;
        let mut pem = String::from("-----BEGIN PRIVATE KEY-----\n");
        for line in STANDARD.encode(der.as_ref()).as_bytes().chunks(64) {
            pem.push_str(&String::from_utf8_lossy(line));
            pem.push('\n');
        }
        pem.push_str("-----END PRIVATE KEY-----\n");
        let doc = json!({
            "type": "service_account",
            "project_id": "gauthtest",
            "private_key_id": self.key_id,
            "private_key": pem,
            "client_email": email,
            "client_id": "000000000000000000000",
            "token_uri": token_url,
        });
        let user = email.split('@').next().unwrap_or(email);
        let path = dir.join(format!("sa-{user}.json"));
        std::fs::write(&path, doc.to_string()).map_err(|e| Error::msg(e.to_string()))?;
        Ok(path)
    }
}
