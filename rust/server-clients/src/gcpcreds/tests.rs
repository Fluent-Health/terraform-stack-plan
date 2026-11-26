use std::sync::Mutex;

use serde_json::json;

use super::*;
use crate::gauth::gauthtest::Issuer;
use crate::testserver::{Req, Resp, TestServer, serve};

fn env_with(metadata: Option<&TestServer>, iam: Option<&TestServer>) -> Env {
    Env {
        metadata_host: metadata.map(|m| m.url.trim_start_matches("http://").to_string()),
        iamcredentials_url: iam.map(|s| s.url.clone()).unwrap_or_default(),
        ..Env::default()
    }
}

async fn recording(resp: &'static str) -> (TestServer, Arc<Mutex<Vec<Req>>>) {
    let seen = Arc::new(Mutex::new(Vec::new()));
    let s = Arc::clone(&seen);
    let srv = serve(move |r| {
        s.lock().unwrap().push(r);
        Resp::ok(resp)
    })
    .await
    .unwrap();
    (srv, seen)
}

#[tokio::test]
async fn metadata_access_token_is_cached() {
    let (meta, seen) =
        recording(r#"{"access_token":"meta-at","token_type":"Bearer","expires_in":3600}"#).await;
    let env = env_with(Some(&meta), None);
    let (token, _) = gcp_creds(&env).await.unwrap();
    assert_eq!(token.token().await.unwrap(), "meta-at");
    assert_eq!(token.token().await.unwrap(), "meta-at");
    let seen = seen.lock().unwrap();
    assert_eq!(seen.len(), 1);
    assert_eq!(
        seen[0].path,
        "/computeMetadata/v1/instance/service-accounts/default/token"
    );
    assert_eq!(seen[0].param("scopes", false), CLOUD_PLATFORM_SCOPE);
    assert_eq!(seen[0].header("metadata-flavor"), "Google");
}

#[tokio::test]
async fn impersonate_calls_generate_access_token() {
    let (meta, _) = recording(r#"{"access_token":"meta-at","expires_in":3600}"#).await;
    let (iam, seen) =
        recording(r#"{"accessToken":"sa-at","expireTime":"2099-01-01T00:00:00Z"}"#).await;
    let env = env_with(Some(&meta), Some(&iam));
    let (_, imp) = gcp_creds(&env).await.unwrap();
    assert_eq!(
        imp.impersonate("req-0@p.iam.gserviceaccount.com")
            .await
            .unwrap(),
        "sa-at"
    );
    let r = seen.lock().unwrap()[0].clone();
    assert_eq!(
        r.path,
        "/v1/projects/-/serviceAccounts/req-0@p.iam.gserviceaccount.com:generateAccessToken"
    );
    assert_eq!(r.header("authorization"), "Bearer meta-at");
    assert_eq!(r.json()["scope"], json!([CLOUD_PLATFORM_SCOPE]));
    assert_eq!(
        mint_access_token(&env, "x@p.iam.gserviceaccount.com")
            .await
            .unwrap(),
        "sa-at"
    );
}

#[tokio::test]
async fn impersonate_error_text() {
    let (meta, _) = recording(r#"{"access_token":"meta-at","expires_in":3600}"#).await;
    let iam = serve(|_| {
        Resp::status(
            403,
            r#"{"error":{"code":403,"message":"Permission denied"}}"#,
        )
    })
    .await
    .unwrap();
    let env = env_with(Some(&meta), Some(&iam));
    let (_, imp) = gcp_creds(&env).await.unwrap();
    assert_eq!(
        imp.impersonate("sa").await.unwrap_err().to_string(),
        "impersonate sa: googleapi: Error 403: Permission denied"
    );
    assert_eq!(
        mint_access_token(&env, "sa").await.unwrap_err().to_string(),
        "mint access token for sa: googleapi: Error 403: Permission denied"
    );
}

#[tokio::test]
async fn missing_credentials_file_is_an_error() {
    let dir = tempfile::tempdir().unwrap();
    let env = Env {
        credentials_file: Some(dir.path().join("nope.json")),
        ..Env::default()
    };
    let err = gcp_creds(&env).await.err().unwrap().to_string();
    assert!(err.starts_with("application default credentials: google: error getting credentials using GOOGLE_APPLICATION_CREDENTIALS"), "{err}");
}

#[tokio::test]
async fn service_account_key_access_token() {
    let issuer = Issuer::new().unwrap();
    let seen = Arc::new(Mutex::new(Vec::new()));
    let s = Arc::clone(&seen);
    let tokens = serve(move |r: Req| {
        s.lock().unwrap().push(r);
        Resp::ok(r#"{"access_token":"sa-at","expires_in":3600}"#)
    })
    .await
    .unwrap();
    let dir = tempfile::tempdir().unwrap();
    let key = issuer
        .write_service_account_key(dir.path(), "svc@p.iam.gserviceaccount.com", &tokens.url)
        .unwrap();
    let env = Env {
        credentials_file: Some(key),
        ..Env::default()
    };
    let (token, _) = gcp_creds(&env).await.unwrap();
    assert_eq!(token.token().await.unwrap(), "sa-at");
    let r = seen.lock().unwrap()[0].clone();
    assert_eq!(
        r.param("grant_type", true),
        "urn:ietf:params:oauth:grant-type:jwt-bearer"
    );
    let assertion = r.param("assertion", true);
    let claims: Value = serde_json::from_slice(
        &URL_SAFE_NO_PAD
            .decode(assertion.split('.').nth(1).unwrap())
            .unwrap(),
    )
    .unwrap();
    assert_eq!(claims["iss"], "svc@p.iam.gserviceaccount.com");
    assert_eq!(claims["scope"], CLOUD_PLATFORM_SCOPE);
    assert_eq!(claims["aud"], tokens.url);
}

#[test]
fn impersonated_file_takes_only_the_account() {
    let doc = json!({
        "type": "impersonated_service_account",
        "service_account_impersonation_url": "https://evil.example/v1/projects/-/serviceAccounts/target@p.iam.gserviceaccount.com:generateAccessToken",
        "source_credentials": {"type": "authorized_user", "client_id": "c", "client_secret": "s", "refresh_token": "r"}
    });
    let FileCreds::Impersonated { target, source } =
        parse_creds(doc.to_string().as_bytes()).unwrap()
    else {
        panic!("want impersonated")
    };
    assert_eq!(target, "target@p.iam.gserviceaccount.com");
    assert!(matches!(*source, FileCreds::AuthorizedUser(_)));
}

#[test]
fn unsupported_types_are_rejected() {
    for t in ["external_account", "", "gdch_service_account"] {
        let doc = json!({"type": t});
        assert!(parse_creds(doc.to_string().as_bytes()).is_err(), "{t}");
    }
}

#[test]
fn env_defaults() {
    let e = Env::default();
    assert_eq!(e.iam_base(), DEFAULT_IAMCREDENTIALS_URL);
    assert_eq!(
        e.metadata_base(),
        "http://169.254.169.254/computeMetadata/v1"
    );
}

#[test]
fn googleapi_error_text() {
    assert_eq!(
        googleapi_error(404, br#"{"error":{"message":"not found"}}"#),
        "googleapi: Error 404: not found"
    );
    assert_eq!(
        googleapi_error(502, b"bad gateway"),
        "googleapi: got HTTP response code 502 with body: bad gateway"
    );
}
