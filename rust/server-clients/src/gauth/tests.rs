use std::path::Path;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use serde_json::json;

use super::gauthtest::Issuer;
use super::*;
use crate::testserver::{Req, Resp, TestServer, serve};

const HOUR: Duration = Duration::from_secs(3600);

async fn verifier(issuer: &Issuer, audiences: &[&str]) -> (Verifier, TestServer) {
    let srv = issuer.serve_jwks().await.unwrap();
    let v = Verifier::with_certs_url(audiences.iter().map(|s| s.to_string()).collect(), &srv.url)
        .unwrap();
    (v, srv)
}

fn claims(v: Value) -> Map<String, Value> {
    v.as_object().unwrap().clone()
}

fn now() -> i64 {
    unix_now()
}

// verifier_test.go

#[tokio::test]
async fn verifier_accepts_signed_token() {
    let issuer = Issuer::new().unwrap();
    let (verify, _srv) = verifier(&issuer, &["https://srv.example"]).await;
    let tok = issuer
        .mint_id_token(
            "runner@x.iam.gserviceaccount.com",
            "https://srv.example",
            HOUR,
        )
        .unwrap();
    assert_eq!(
        verify.verify(&tok).await.unwrap(),
        "runner@x.iam.gserviceaccount.com"
    );
}

#[tokio::test]
async fn verifier_audience_allowlist() {
    let issuer = Issuer::new().unwrap();
    let (verify, _srv) = verifier(
        &issuer,
        &[
            "https://srv.example",
            "gcloud-client-id.apps.googleusercontent.com",
        ],
    )
    .await;
    let tok = issuer
        .mint_id_token(
            "user@example.com",
            "gcloud-client-id.apps.googleusercontent.com",
            HOUR,
        )
        .unwrap();
    verify.verify(&tok).await.unwrap();
    let tok2 = issuer
        .mint_id_token("user@example.com", "https://other.example", HOUR)
        .unwrap();
    let err = verify.verify(&tok2).await.unwrap_err();
    assert_eq!(
        err.to_string(),
        r#"unexpected audience "https://other.example""#
    );
}

#[tokio::test]
async fn verifier_rejections() {
    let issuer = Issuer::new().unwrap();
    let (verify, _srv) = verifier(&issuer, &["https://srv.example"]).await;
    let n = now();
    let iss = "https://accounts.google.com";
    let expired = issuer
        .mint_id_token_claims(claims(json!({"iss": iss,
            "aud": "https://srv.example", "email": "a@b.c", "email_verified": true,
            "iat": n - 7200, "exp": n - 3600})))
        .unwrap();
    let no_email = issuer
        .mint_id_token_claims(claims(
            json!({"iss": iss, "aud": "https://srv.example", "iat": n, "exp": n + 3600}),
        ))
        .unwrap();
    let unverified = issuer
        .mint_id_token_claims(claims(json!({"iss": iss,
            "aud": "https://srv.example", "email": "a@b.c", "email_verified": false,
            "iat": n, "exp": n + 3600})))
        .unwrap();
    let other = Issuer::new().unwrap();
    let forged = other
        .mint_id_token("a@b.c", "https://srv.example", HOUR)
        .unwrap();
    for (name, tok, want) in [
        ("expired", expired, "idtoken: token expired"),
        ("no-email", no_email, "oidc token has no email claim"),
        (
            "unverified-email",
            unverified,
            "oidc token email not verified",
        ),
        ("wrong-key", forged, "crypto/rsa: verification error"),
        (
            "garbage",
            "not.a.jwt".to_string(),
            "idtoken: unable to decode JWT header",
        ),
    ] {
        let err = verify.verify(&tok).await.expect_err(name).to_string();
        assert!(err.starts_with(want), "{name}: {err}");
    }
}

// Beyond Go: the checks this port adds or makes explicit.

#[tokio::test]
async fn rejects_foreign_or_missing_issuer() {
    let issuer = Issuer::new().unwrap();
    let (verify, _srv) = verifier(&issuer, &["aud"]).await;
    let n = now();
    for iss in [
        json!("https://evil.example"),
        json!(null),
        json!("https://cloud.google.com/iap"),
    ] {
        let tok = issuer
            .mint_id_token_claims(claims(json!({"iss": iss, "aud": "aud", "email": "a@b.c",
                "exp": n + 3600})))
            .unwrap();
        let err = verify.verify(&tok).await.unwrap_err().to_string();
        assert!(err.starts_with("unexpected issuer"), "{err}");
    }
    let tok = issuer
        .mint_id_token_claims(claims(json!({"iss": "accounts.google.com", "aud": "aud",
            "email": "a@b.c", "exp": n + 3600})))
        .unwrap();
    verify.verify(&tok).await.unwrap();
}

#[tokio::test]
async fn rejects_other_algorithms_and_malformed_claims() {
    let issuer = Issuer::new().unwrap();
    let (verify, _srv) = verifier(&issuer, &["aud"]).await;
    let good = issuer.mint_id_token("a@b.c", "aud", HOUR).unwrap();
    let parts: Vec<&str> = good.split('.').collect();
    for alg in ["ES256", "HS256", "none"] {
        let h = URL_SAFE_NO_PAD.encode(format!(r#"{{"alg":"{alg}","kid":"gauthtest-key-1"}}"#));
        let tok = format!("{h}.{}.{}", parts[1], parts[2]);
        let err = verify.verify(&tok).await.unwrap_err().to_string();
        assert!(
            err.contains("expected JWT signed with RS256"),
            "{alg}: {err}"
        );
    }
    let n = now();
    for bad in [
        json!({"iss": "accounts.google.com", "aud": ["aud"], "email": "a@b.c", "exp": n + 60}),
        json!({"iss": "accounts.google.com", "aud": "aud", "email": "a@b.c", "exp": "soon"}),
        json!({"iss": "accounts.google.com", "aud": "aud", "email": "a@b.c", "exp": 1.5e12}),
    ] {
        let tok = issuer.mint_id_token_claims(claims(bad.clone())).unwrap();
        assert!(verify.verify(&tok).await.is_err(), "{bad}");
    }
    assert!(
        verify
            .verify(&format!("{good}.x"))
            .await
            .unwrap_err()
            .to_string()
            .contains("found 4")
    );
    // A tampered payload fails the signature.
    let p = URL_SAFE_NO_PAD
        .encode(r#"{"iss":"accounts.google.com","aud":"aud","email":"evil@x","exp":99999999999}"#);
    assert!(
        verify
            .verify(&format!("{}.{p}.{}", parts[0], parts[2]))
            .await
            .is_err()
    );
}

#[test]
fn cache_ttl_matches_idtoken() {
    let s = Duration::from_secs;
    assert_eq!(
        cache_ttl("public, max-age=19995, must-revalidate", ""),
        s(19995)
    );
    assert_eq!(cache_ttl("max-age=3600", "600"), s(3000));
    assert_eq!(cache_ttl("max-age=60", "600"), s(0));
    assert_eq!(cache_ttl("no-store", ""), s(0));
    assert_eq!(cache_ttl("max-age", ""), s(0));
    assert_eq!(cache_ttl("max-age=abc", ""), s(0));
    assert_eq!(cache_ttl("max-age=10", "x"), s(0));
    assert_eq!(cache_ttl("", ""), s(0));
}

#[tokio::test]
async fn jwks_is_cached_by_max_age() {
    let issuer = Issuer::new().unwrap();
    let hits = Arc::new(Mutex::new(0));
    let (h, jwks) = (Arc::clone(&hits), issuer.jwks());
    let srv = serve(move |_| {
        *h.lock().unwrap() += 1;
        Resp::ok(jwks.clone()).header("Cache-Control", "public, max-age=3600")
    })
    .await
    .unwrap();
    let v = Verifier::with_certs_url(vec!["aud".into()], &srv.url).unwrap();
    for _ in 0..3 {
        v.verify(&issuer.mint_id_token("a@b.c", "aud", HOUR).unwrap())
            .await
            .unwrap();
    }
    assert_eq!(*hits.lock().unwrap(), 1);
}

#[tokio::test]
async fn certs_fetch_failure_is_an_error() {
    let issuer = Issuer::new().unwrap();
    let srv = serve(|_| Resp::status(503, "down")).await.unwrap();
    let v = Verifier::with_certs_url(vec!["aud".into()], &srv.url).unwrap();
    let err = v
        .verify(&issuer.mint_id_token("a@b.c", "aud", HOUR).unwrap())
        .await
        .unwrap_err();
    assert_eq!(
        err.to_string(),
        "idtoken: unable to retrieve cert, got status code 503"
    );
}

#[tokio::test]
async fn unknown_kid_is_rejected() {
    let issuer = Issuer::new().unwrap();
    let (verify, _srv) = verifier(&issuer, &["aud"]).await;
    let mut other = Issuer::new().unwrap();
    other.key_id = "other-kid".into();
    let err = verify
        .verify(&other.mint_id_token("a@b.c", "aud", HOUR).unwrap())
        .await
        .unwrap_err();
    assert_eq!(
        err.to_string(),
        "idtoken: could not find matching cert keyId for the token provided"
    );
}

#[tokio::test]
async fn claims_verifier_returns_all_claims() {
    let issuer = Issuer::new().unwrap();
    let srv = issuer.serve_jwks().await.unwrap();
    let v = ClaimsVerifier::with_certs_url(vec!["cid".into()], &srv.url).unwrap();
    let n = now();
    let tok = issuer
        .mint_id_token_claims(claims(
            json!({"iss": "https://accounts.google.com", "aud": "cid",
            "email": "u@example.com", "hd": "example.com", "exp": n + 60}),
        ))
        .unwrap();
    let c = v.verify(&tok).await.unwrap();
    assert_eq!(c["hd"], "example.com");
}

// gauth_test.go

fn env_with(metadata: Option<&TestServer>, iam: Option<&TestServer>) -> Env {
    Env {
        credentials_file: None,
        well_known_file: None,
        metadata_host: metadata.map(|m| m.url.trim_start_matches("http://").to_string()),
        iamcredentials_url: iam.map(|s| s.url.clone()).unwrap_or_default(),
    }
}

fn user_creds_file(dir: &Path, token_uri: &str) -> std::path::PathBuf {
    let p = dir.join("adc.json");
    std::fs::write(
        &p,
        json!({"type": "authorized_user", "client_id": "cid", "client_secret": "cs",
            "refresh_token": "rt", "token_uri": token_uri})
        .to_string(),
    )
    .unwrap();
    p
}

#[tokio::test]
async fn from_token_source_picks_id_token() {
    let srv = serve(|r: Req| {
        assert_eq!(r.param("grant_type", true), "refresh_token");
        Resp::ok(r#"{"access_token":"at","id_token":"idt-123","expires_in":3600}"#)
    })
    .await
    .unwrap();
    let dir = tempfile::tempdir().unwrap();
    let mut env = env_with(None, None);
    env.credentials_file = Some(user_creds_file(dir.path(), &format!("{}/token", srv.url)));
    let hc = http::client(TIMEOUT).unwrap();
    let Some(FileCreds::AuthorizedUser(creds)) = find_file_creds(&env).unwrap() else {
        panic!("want user creds")
    };
    let src = PickIdToken(Cached::new(Flow::UserIdToken { creds }, &env, &hc));
    assert_eq!(src.token().await.unwrap(), "idt-123");
}

#[tokio::test]
async fn from_token_source_missing_id_token() {
    let srv = serve(|_| Resp::ok(r#"{"access_token":"at","expires_in":3600}"#))
        .await
        .unwrap();
    let dir = tempfile::tempdir().unwrap();
    let mut env = env_with(None, None);
    env.credentials_file = Some(user_creds_file(dir.path(), &srv.url));
    let hc = http::client(TIMEOUT).unwrap();
    let Some(FileCreds::AuthorizedUser(creds)) = find_file_creds(&env).unwrap() else {
        panic!("want user creds")
    };
    let src = PickIdToken(Cached::new(Flow::UserIdToken { creds }, &env, &hc));
    assert!(
        src.token()
            .await
            .unwrap_err()
            .to_string()
            .contains("no ID token")
    );
}

#[tokio::test]
async fn from_token_source_propagates_error() {
    let srv = serve(|_| Resp::status(400, r#"{"error":"invalid_grant"}"#))
        .await
        .unwrap();
    let dir = tempfile::tempdir().unwrap();
    let mut env = env_with(None, None);
    env.credentials_file = Some(user_creds_file(dir.path(), &srv.url));
    let hc = http::client(TIMEOUT).unwrap();
    let Some(FileCreds::AuthorizedUser(creds)) = find_file_creds(&env).unwrap() else {
        panic!("want user creds")
    };
    let src = PickIdToken(Cached::new(Flow::UserIdToken { creds }, &env, &hc));
    assert!(
        src.token()
            .await
            .unwrap_err()
            .to_string()
            .contains("invalid_grant")
    );
}

/// Go's fakeNonGCE: a "metadata server" that answers without the Metadata-Flavor header.
/// As in Go, GCE_METADATA_HOST being set counts as on-GCE; discovery then fails later.
async fn fake_non_gce() -> TestServer {
    serve(|_| Resp::ok("not gce")).await.unwrap()
}

#[tokio::test]
async fn source_errors_without_credentials() {
    let meta = fake_non_gce().await;
    let dir = tempfile::tempdir().unwrap();
    let mut env = env_with(Some(&meta), None);
    env.credentials_file = Some(dir.path().join("nonexistent.json"));
    assert!(source(&env, "https://srv.example").await.is_err());
}

#[tokio::test]
async fn self_id_token_source() {
    let got = Arc::new(Mutex::new(None::<Req>));
    let g = Arc::clone(&got);
    let iam = serve(move |r| {
        *g.lock().unwrap() = Some(r);
        Resp::ok(r#"{"token":"cb-id-token"}"#)
    })
    .await
    .unwrap();
    let env = env_with(None, Some(&iam));
    let hc = http::client(TIMEOUT).unwrap();
    let base: TokenFunc = Arc::new(|| async { Ok::<_, Error>("ambient".to_string()) });
    let src = super::self_id_token_source(
        &env,
        &hc,
        "https://serve.example",
        "tf-planner@x.iam.gserviceaccount.com",
        base,
    );
    assert_eq!(src.token().await.unwrap(), "cb-id-token");
    let r = got.lock().unwrap().clone().unwrap();
    assert!(
        r.path
            .contains("tf-planner@x.iam.gserviceaccount.com:generateIdToken"),
        "{}",
        r.path
    );
    assert_eq!(r.json()["audience"], "https://serve.example");
    assert_eq!(r.json()["includeEmail"], true);
    assert_eq!(r.header("authorization"), "Bearer ambient");
}

#[tokio::test]
async fn source_cloud_build_fallback() {
    let meta = serve(|r| {
        let resp = if r.path.ends_with("/identity") || r.query.contains("audience") {
            Resp::status(404, "identity endpoint not implemented")
        } else if r.path.ends_with("/email") {
            Resp::ok("tf-planner@x.iam.gserviceaccount.com")
        } else if r.path.ends_with("/token") {
            Resp::ok(r#"{"access_token":"at","token_type":"Bearer","expires_in":3600}"#)
        } else {
            Resp::ok("ok")
        };
        resp.header("Metadata-Flavor", "Google")
    })
    .await
    .unwrap();
    let got = Arc::new(Mutex::new(None::<Req>));
    let g = Arc::clone(&got);
    let iam = serve(move |r| {
        *g.lock().unwrap() = Some(r);
        Resp::ok(r#"{"token":"cb-id-token"}"#)
    })
    .await
    .unwrap();
    let env = env_with(Some(&meta), Some(&iam));
    let fn_ = source(&env, "https://serve.example").await.unwrap();
    // Unlike Go, the IAM Credentials endpoint is injectable, so the mint is exercised too.
    assert_eq!(fn_.token().await.unwrap(), "cb-id-token");
    let r = got.lock().unwrap().clone().unwrap();
    assert!(
        r.path
            .contains("tf-planner@x.iam.gserviceaccount.com:generateIdToken")
    );
    assert_eq!(r.header("authorization"), "Bearer at");
}

#[tokio::test]
async fn source_timeout_propagates_fast_failure() {
    let meta = fake_non_gce().await;
    let dir = tempfile::tempdir().unwrap();
    let mut env = env_with(Some(&meta), None);
    env.credentials_file = Some(dir.path().join("nonexistent.json"));
    let err = source_timeout(&env, Duration::from_secs(5), "https://srv.example")
        .await
        .err()
        .unwrap();
    assert!(!err.to_string().contains("timed out"), "{err}");
}

#[tokio::test]
async fn source_timeout_reports_timeout() {
    // A metadata server that accepts connections and never answers stalls discovery.
    let silent = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let mut env = env_with(None, None);
    env.metadata_host = Some(silent.local_addr().unwrap().to_string());
    let err = source_timeout(&env, Duration::from_millis(200), "a")
        .await
        .err()
        .unwrap();
    assert_eq!(
        err.to_string(),
        "google credential discovery timed out after 200ms"
    );
}

#[tokio::test]
async fn source_mints_through_service_account_key() {
    // gauthtest's TokenEndpoint + WriteServiceAccountKey, through the real JWT-bearer flow.
    let issuer = Issuer::new().unwrap();
    let tokens = issuer.token_endpoint().await.unwrap();
    let dir = tempfile::tempdir().unwrap();
    let key = issuer
        .write_service_account_key(dir.path(), "runner@x.iam.gserviceaccount.com", &tokens.url)
        .unwrap();
    let mut env = env_with(None, None);
    env.credentials_file = Some(key);
    let src = source(&env, "https://srv.example").await.unwrap();
    let tok = src.token().await.unwrap();
    let (verify, _jwks) = verifier(&issuer, &["https://srv.example"]).await;
    assert_eq!(
        verify.verify(&tok).await.unwrap(),
        "runner@x.iam.gserviceaccount.com"
    );
}

#[tokio::test]
async fn source_uses_the_metadata_identity_endpoint() {
    let meta = serve(|r| {
        if r.path.ends_with("/identity") && r.param("format", false) == "full" {
            assert_eq!(r.header("metadata-flavor"), "Google");
            Resp::ok(format!("id-for-{}", r.param("audience", false)))
        } else {
            Resp::status(404, "")
        }
    })
    .await
    .unwrap();
    let env = env_with(Some(&meta), None);
    let src = source(&env, "https://srv.example").await.unwrap();
    assert_eq!(src.token().await.unwrap(), "id-for-https://srv.example");
}

#[test]
fn go_durations() {
    assert_eq!(go_duration(Duration::from_secs(10)), "10s");
    assert_eq!(go_duration(Duration::from_secs(90)), "1m30s");
    assert_eq!(go_duration(Duration::from_millis(1500)), "1.5s");
    assert_eq!(go_duration(Duration::from_millis(20)), "20ms");
}
