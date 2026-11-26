use wiremock::matchers::{body_string_contains, header, method, path, query_param};
use wiremock::{Mock, MockServer, ResponseTemplate};

use super::*;

async fn blocking<T: Send + 'static>(f: impl FnOnce() -> T + Send + 'static) -> T {
    tokio::task::spawn_blocking(f).await.unwrap()
}

fn not_gce() -> bool {
    false
}

fn gce() -> bool {
    true
}

/// Endpoints with no ambient credentials, pointed at `srv`.
fn endpoints(srv: &str, on_gce: fn() -> bool) -> Endpoints {
    Endpoints {
        iam_credentials: srv.to_string(),
        metadata_host: srv.trim_start_matches("http://").to_string(),
        on_gce,
        credentials_file: String::new(),
        home: String::new(),
    }
}

fn write_creds(dir: &tempfile::TempDir, json: &Value) -> String {
    let p = dir.path().join("creds.json");
    std::fs::write(&p, json.to_string()).unwrap();
    p.to_string_lossy().into_owned()
}

fn user_creds(token_uri: &str) -> Value {
    serde_json::json!({
        "type": "authorized_user",
        "client_id": "cid.apps.example.com",
        "client_secret": "secret",
        "refresh_token": "rt",
        "token_uri": token_uri,
    })
}

/// A throwaway RSA key for a made-up service account, as PKCS#8 PEM.
fn rsa_pem() -> String {
    use aws_lc_rs::encoding::AsDer;
    let kp = aws_lc_rs::rsa::KeyPair::generate(aws_lc_rs::rsa::KeySize::Rsa2048).unwrap();
    let der: aws_lc_rs::encoding::Pkcs8V1Der<'_> = kp.as_der().unwrap();
    let b64 = base64::engine::general_purpose::STANDARD.encode(der.as_ref());
    let lines: Vec<&str> = b64
        .as_bytes()
        .chunks(64)
        .map(|c| std::str::from_utf8(c).unwrap())
        .collect();
    format!(
        "-----BEGIN PRIVATE KEY-----\n{}\n-----END PRIVATE KEY-----\n",
        lines.join("\n")
    )
}

fn claims_of(jwt: &str) -> Value {
    let part = jwt.split('.').nth(1).unwrap();
    serde_json::from_slice(&URL_SAFE_NO_PAD.decode(part).unwrap()).unwrap()
}

// Go: TestSourceErrorsWithoutCredentials (a missing GOOGLE_APPLICATION_CREDENTIALS file).
#[tokio::test(flavor = "multi_thread")]
async fn missing_credentials_file_errors_like_go() {
    let mut ep = endpoints("http://127.0.0.1:9", not_gce);
    ep.credentials_file = "/nonexistent/creds.json".into();
    let err = blocking(move || {
        id_token_source_with("https://srv.example", ep)
            .err()
            .unwrap()
    })
    .await;
    assert_eq!(
        err,
        "google: error getting credentials using GOOGLE_APPLICATION_CREDENTIALS environment variable: open /nonexistent/creds.json: no such file or directory"
    );
}

#[tokio::test(flavor = "multi_thread")]
async fn nothing_found_off_gce() {
    let ep = endpoints("http://127.0.0.1:9", not_gce);
    let err = blocking(move || id_token_source_with("aud", ep).err().unwrap()).await;
    assert_eq!(err, not_found());
    let ep = endpoints("http://127.0.0.1:9", not_gce);
    let err = blocking(move || access_token_source_with(ep).err().unwrap()).await;
    assert_eq!(err, not_found());
}

#[test]
fn credential_type_errors() {
    assert_eq!(
        parse_file(br#"{"client_id":"x"}"#).unwrap_err(),
        "missing 'type' field in credentials"
    );
    assert_eq!(
        parse_file(br#"{"type":"weird"}"#).unwrap_err(),
        "unknown credential type: \"weird\""
    );
}

// Go: TestFromTokenSourcePicksIDToken, via the user refresh grant.
#[tokio::test(flavor = "multi_thread")]
async fn user_credentials_yield_the_refresh_id_token() {
    let srv = MockServer::start().await;
    Mock::given(method("POST"))
        .and(path("/token"))
        .and(body_string_contains("grant_type=refresh_token"))
        .and(body_string_contains("refresh_token=rt"))
        .and(body_string_contains("client_id=cid.apps.example.com"))
        .respond_with(ResponseTemplate::new(200).set_body_json(serde_json::json!({
            "access_token": "at", "id_token": "idt-123", "expires_in": 3600, "token_type": "Bearer"
        })))
        .expect(1)
        .mount(&srv)
        .await;
    let dir = tempfile::tempdir().unwrap();
    let gcloud = dir.path().join(".config/gcloud");
    std::fs::create_dir_all(&gcloud).unwrap();
    std::fs::write(
        gcloud.join("application_default_credentials.json"),
        user_creds(&format!("{}/token", srv.uri())).to_string(),
    )
    .unwrap();
    let mut ep = endpoints(&srv.uri(), not_gce);
    ep.home = dir.path().to_string_lossy().into_owned();
    let (a, b) = blocking(move || {
        let f = id_token_source_with("aud", ep).unwrap();
        (f().unwrap(), f().unwrap())
    })
    .await;
    assert_eq!(
        (a.as_str(), b.as_str()),
        ("idt-123", "idt-123"),
        "second call reuses the token"
    );
}

// Go: TestFromTokenSourceMissingIDToken.
#[tokio::test(flavor = "multi_thread")]
async fn user_credentials_without_id_token() {
    let srv = MockServer::start().await;
    Mock::given(method("POST"))
        .and(path("/token"))
        .respond_with(
            ResponseTemplate::new(200).set_body_json(serde_json::json!({"access_token": "at"})),
        )
        .mount(&srv)
        .await;
    let dir = tempfile::tempdir().unwrap();
    let mut ep = endpoints(&srv.uri(), not_gce);
    ep.credentials_file = write_creds(&dir, &user_creds(&format!("{}/token", srv.uri())));
    let err = blocking(move || id_token_source_with("aud", ep).unwrap()().unwrap_err()).await;
    assert_eq!(
        err,
        "credentials carry no ID token (re-run `gcloud auth application-default login`)"
    );
}

// Go: TestFromTokenSourcePropagatesError, with oauth2's RetrieveError wording.
#[tokio::test(flavor = "multi_thread")]
async fn token_endpoint_errors_read_like_oauth2() {
    let srv = MockServer::start().await;
    Mock::given(method("POST"))
        .and(path("/json"))
        .respond_with(ResponseTemplate::new(400).set_body_json(
            serde_json::json!({"error": "invalid_grant", "error_description": "Bad Request"}),
        ))
        .mount(&srv)
        .await;
    Mock::given(method("POST"))
        .and(path("/plain"))
        .respond_with(ResponseTemplate::new(500).set_body_string("boom"))
        .mount(&srv)
        .await;
    for (p, want) in [
        ("/json", "oauth2: \"invalid_grant\" \"Bad Request\""),
        (
            "/plain",
            "oauth2: cannot fetch token: 500 Internal Server Error\nResponse: boom",
        ),
    ] {
        let dir = tempfile::tempdir().unwrap();
        let mut ep = endpoints(&srv.uri(), not_gce);
        ep.credentials_file = write_creds(&dir, &user_creds(&format!("{}{p}", srv.uri())));
        let err = blocking(move || access_token_source_with(ep).unwrap()().unwrap_err()).await;
        assert_eq!(err, want);
    }
}

#[tokio::test(flavor = "multi_thread")]
async fn service_account_key_mints_id_and_access_tokens() {
    let srv = MockServer::start().await;
    Mock::given(method("POST"))
        .and(path("/token"))
        .and(body_string_contains(
            "grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Ajwt-bearer",
        ))
        .respond_with(move |r: &wiremock::Request| {
            let body = String::from_utf8(r.body.clone()).unwrap();
            let assertion = body
                .split('&')
                .find_map(|kv| kv.strip_prefix("assertion="))
                .unwrap();
            let c = claims_of(assertion);
            assert_eq!(c["iss"], "planner@example-project.iam.gserviceaccount.com");
            if c.get("target_audience").is_some() {
                assert_eq!(c["target_audience"], "https://srv.example");
                ResponseTemplate::new(200).set_body_json(serde_json::json!({"id_token": "sa-id"}))
            } else {
                assert_eq!(c["scope"], CLOUD_PLATFORM);
                ResponseTemplate::new(200)
                    .set_body_json(serde_json::json!({"access_token": "sa-at", "expires_in": 3600}))
            }
        })
        .mount(&srv)
        .await;
    let dir = tempfile::tempdir().unwrap();
    let creds = serde_json::json!({
        "type": "service_account",
        "client_email": "planner@example-project.iam.gserviceaccount.com",
        "private_key_id": "k1",
        "private_key": rsa_pem(),
        "token_uri": format!("{}/token", srv.uri()),
    });
    let mut ep = endpoints(&srv.uri(), not_gce);
    ep.credentials_file = write_creds(&dir, &creds);
    let (id, at) = blocking(move || {
        let id = id_token_source_with("https://srv.example", ep.clone()).unwrap()().unwrap();
        let at = access_token_source_with(ep).unwrap()().unwrap();
        (id, at)
    })
    .await;
    assert_eq!((id.as_str(), at.as_str()), ("sa-id", "sa-at"));
}

#[tokio::test(flavor = "multi_thread")]
async fn metadata_identity_on_gce() {
    let srv = MockServer::start().await;
    Mock::given(method("GET"))
        .and(path(
            "/computeMetadata/v1/instance/service-accounts/default/identity",
        ))
        .and(query_param("audience", "https://srv.example"))
        .and(query_param("format", "full"))
        .and(header("Metadata-Flavor", "Google"))
        .respond_with(ResponseTemplate::new(200).set_body_string("gce-id"))
        .mount(&srv)
        .await;
    Mock::given(method("GET"))
        .and(path(
            "/computeMetadata/v1/instance/service-accounts/default/token",
        ))
        .and(query_param("scopes", CLOUD_PLATFORM))
        .respond_with(
            ResponseTemplate::new(200).set_body_json(serde_json::json!({"access_token": "gce-at"})),
        )
        .mount(&srv)
        .await;
    let ep = endpoints(&srv.uri(), gce);
    let (id, at) = blocking(move || {
        (
            id_token_source_with("https://srv.example", ep.clone()).unwrap()().unwrap(),
            access_token_source_with(ep).unwrap()().unwrap(),
        )
    })
    .await;
    assert_eq!((id.as_str(), at.as_str()), ("gce-id", "gce-at"));
}

// Go: TestSourceCloudBuildFallback and TestSelfIDTokenSource: no identity
// endpoint, so mint through generateIdToken as the instance's own account.
#[tokio::test(flavor = "multi_thread")]
async fn cloud_build_falls_back_to_self_impersonation() {
    let srv = MockServer::start().await;
    Mock::given(method("GET"))
        .and(path(
            "/computeMetadata/v1/instance/service-accounts/default/identity",
        ))
        .respond_with(ResponseTemplate::new(404).set_body_string("not implemented"))
        .mount(&srv)
        .await;
    Mock::given(method("GET"))
        .and(path(
            "/computeMetadata/v1/instance/service-accounts/default/email",
        ))
        .respond_with(
            ResponseTemplate::new(200)
                .set_body_string("tf-planner@example-project.iam.gserviceaccount.com\n"),
        )
        .mount(&srv)
        .await;
    Mock::given(method("GET"))
        .and(path(
            "/computeMetadata/v1/instance/service-accounts/default/token",
        ))
        .respond_with(
            ResponseTemplate::new(200).set_body_json(serde_json::json!({"access_token": "at"})),
        )
        .mount(&srv)
        .await;
    Mock::given(method("POST"))
        .and(path(
            "/v1/projects/-/serviceAccounts/tf-planner@example-project.iam.gserviceaccount.com:generateIdToken",
        ))
        .and(header("Authorization", "Bearer at"))
        .and(wiremock::matchers::body_json(
            serde_json::json!({"audience": "https://serve.example", "includeEmail": true}),
        ))
        .respond_with(ResponseTemplate::new(200).set_body_json(serde_json::json!({"token": "cb-id-token"})))
        .expect(1)
        .mount(&srv)
        .await;
    let ep = endpoints(&srv.uri(), gce);
    let tok =
        blocking(move || id_token_source_with("https://serve.example", ep).unwrap()().unwrap())
            .await;
    assert_eq!(tok, "cb-id-token");
}

#[tokio::test(flavor = "multi_thread")]
async fn impersonated_service_account_file() {
    let srv = MockServer::start().await;
    Mock::given(method("POST"))
        .and(path("/token"))
        .respond_with(
            ResponseTemplate::new(200).set_body_json(serde_json::json!({"access_token": "src-at"})),
        )
        .mount(&srv)
        .await;
    Mock::given(method("POST"))
        .and(path("/v1/projects/-/serviceAccounts/target@example-project.iam.gserviceaccount.com:generateAccessToken"))
        .and(header("Authorization", "Bearer src-at"))
        .respond_with(ResponseTemplate::new(200).set_body_json(serde_json::json!({"accessToken": "imp-at"})))
        .mount(&srv)
        .await;
    Mock::given(method("POST"))
        .and(path("/v1/projects/-/serviceAccounts/target@example-project.iam.gserviceaccount.com:generateIdToken"))
        .and(header("Authorization", "Bearer src-at"))
        .respond_with(ResponseTemplate::new(200).set_body_json(serde_json::json!({"token": "imp-id"})))
        .mount(&srv)
        .await;
    let dir = tempfile::tempdir().unwrap();
    let creds = serde_json::json!({
        "type": "impersonated_service_account",
        "service_account_impersonation_url": format!(
            "{}/v1/projects/-/serviceAccounts/target@example-project.iam.gserviceaccount.com:generateAccessToken",
            srv.uri()
        ),
        "source_credentials": user_creds(&format!("{}/token", srv.uri())),
    });
    let mut ep = endpoints(&srv.uri(), not_gce);
    ep.credentials_file = write_creds(&dir, &creds);
    let (at, id) = blocking(move || {
        (
            access_token_source_with(ep.clone()).unwrap()().unwrap(),
            id_token_source_with("aud", ep).unwrap()().unwrap(),
        )
    })
    .await;
    assert_eq!((at.as_str(), id.as_str()), ("imp-at", "imp-id"));
}

#[test]
fn impersonated_account_from_url() {
    assert_eq!(
        impersonated_account(
            "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/sa@p.iam.gserviceaccount.com:generateAccessToken"
        ),
        "sa@p.iam.gserviceaccount.com"
    );
}

// Go: realMintAccessToken (run apply --impersonate-requester).
#[tokio::test(flavor = "multi_thread")]
async fn impersonated_access_token_for_the_requester() {
    let srv = MockServer::start().await;
    Mock::given(method("GET"))
        .and(path(
            "/computeMetadata/v1/instance/service-accounts/default/token",
        ))
        .respond_with(
            ResponseTemplate::new(200)
                .set_body_json(serde_json::json!({"access_token": "ambient"})),
        )
        .mount(&srv)
        .await;
    Mock::given(method("POST"))
        .and(path("/v1/projects/-/serviceAccounts/requester@example-project.iam.gserviceaccount.com:generateAccessToken"))
        .and(header("Authorization", "Bearer ambient"))
        .respond_with(ResponseTemplate::new(200).set_body_json(serde_json::json!({"accessToken": "elevated"})))
        .mount(&srv)
        .await;
    Mock::given(method("POST"))
        .and(path("/v1/projects/-/serviceAccounts/denied@example-project.iam.gserviceaccount.com:generateAccessToken"))
        .respond_with(ResponseTemplate::new(403).set_body_string("denied"))
        .mount(&srv)
        .await;
    let ep = endpoints(&srv.uri(), gce);
    let (ok, err) = blocking(move || {
        (
            impersonated_access_token_with(
                "requester@example-project.iam.gserviceaccount.com",
                ep.clone(),
            ),
            impersonated_access_token_with("denied@example-project.iam.gserviceaccount.com", ep),
        )
    })
    .await;
    assert_eq!(ok.unwrap(), "elevated");
    assert_eq!(
        err.unwrap_err(),
        "mint access token for denied@example-project.iam.gserviceaccount.com: impersonate: status code 403: denied"
    );
}

/// An unsigned JWT whose payload carries only `exp`.
fn jwt_with_exp(exp: u64) -> String {
    let payload = URL_SAFE_NO_PAD.encode(serde_json::json!({ "exp": exp }).to_string());
    format!("e30.{payload}.sig")
}

#[test]
fn jwt_expiry_reads_exp() {
    let now = now_secs();
    let soon = jwt_expiry(&jwt_with_exp(now + 600)).unwrap();
    let left = soon.saturating_duration_since(Instant::now());
    assert!(left > Duration::from_secs(590) && left <= Duration::from_secs(600));
    let past = jwt_expiry(&jwt_with_exp(now - 60)).unwrap();
    assert!(past <= Instant::now());
    assert!(jwt_expiry("not-a-jwt").is_none());
}

/// A service-account key and a token endpoint that answers every ID-token request with
/// a token expiring `ttl` seconds from now; `want` is how many requests it must see.
async fn sa_id_token_fetches(ttl: u64, calls: usize, want: u64) {
    let srv = MockServer::start().await;
    Mock::given(method("POST"))
        .and(path("/token"))
        .respond_with(move |_: &wiremock::Request| {
            ResponseTemplate::new(200)
                .set_body_json(serde_json::json!({ "id_token": jwt_with_exp(now_secs() + ttl) }))
        })
        .expect(want)
        .mount(&srv)
        .await;
    let dir = tempfile::tempdir().unwrap();
    let creds = serde_json::json!({
        "type": "service_account",
        "client_email": "planner@example-project.iam.gserviceaccount.com",
        "private_key": rsa_pem(),
        "token_uri": format!("{}/token", srv.uri()),
    });
    let mut ep = endpoints(&srv.uri(), not_gce);
    ep.credentials_file = write_creds(&dir, &creds);
    blocking(move || {
        let src = id_token_source_with("https://srv.example", ep).unwrap();
        for _ in 0..calls {
            src().unwrap();
        }
    })
    .await;
    srv.verify().await;
}

// Go (cloud.google.com/go/auth) takes a service account ID token's expiry from its `exp`
// claim and refreshes it 225 s early. A token with an hour left is reused...
#[tokio::test(flavor = "multi_thread")]
async fn service_account_id_token_is_reused_until_exp() {
    sa_id_token_fetches(3600, 3, 1).await;
}

// ...and one inside the 225 s window is fetched again on every call, never kept forever.
#[tokio::test(flavor = "multi_thread")]
async fn service_account_id_token_refreshes_after_exp() {
    sa_id_token_fetches(100, 3, 4).await;
}

// Go's metadata client has no proxy. The parent runs `metadata_ignores_proxy_child` in a
// fresh copy of this test binary with HTTP_PROXY pointing at a closed port; the metadata
// call must still reach the fake server.
#[tokio::test(flavor = "multi_thread")]
async fn metadata_calls_ignore_proxy_env() {
    let srv = MockServer::start().await;
    Mock::given(method("GET"))
        .and(path(
            "/computeMetadata/v1/instance/service-accounts/default/email",
        ))
        .and(header("Metadata-Flavor", "Google"))
        .respond_with(
            ResponseTemplate::new(200).set_body_string("sa@example.iam.gserviceaccount.com"),
        )
        .expect(1)
        .mount(&srv)
        .await;
    let exe = std::env::current_exe().unwrap();
    let host = srv.uri().trim_start_matches("http://").to_string();
    let out = blocking(move || {
        std::process::Command::new(exe)
            .args([
                "--exact",
                "google::tests::metadata_ignores_proxy_child",
                "--ignored",
                "--nocapture",
            ])
            .env("TFSP_TEST_METADATA_HOST", host)
            .env("HTTP_PROXY", "http://127.0.0.1:9")
            .env("http_proxy", "http://127.0.0.1:9")
            .env("ALL_PROXY", "http://127.0.0.1:9")
            .env_remove("NO_PROXY")
            .env_remove("no_proxy")
            .output()
            .unwrap()
    })
    .await;
    let stdout = String::from_utf8_lossy(&out.stdout);
    assert!(
        out.status.success(),
        "child failed:\n{stdout}\n{}",
        String::from_utf8_lossy(&out.stderr)
    );
    assert!(stdout.contains("1 passed"), "child did not run:\n{stdout}");
    srv.verify().await;
}

#[test]
#[ignore = "run by metadata_calls_ignore_proxy_env with a proxy set"]
fn metadata_ignores_proxy_child() {
    let host = std::env::var("TFSP_TEST_METADATA_HOST").unwrap();
    let ep = Endpoints {
        metadata_host: host,
        ..endpoints("http://127.0.0.1:9", gce)
    };
    let email = metadata_get(&ep, "instance/service-accounts/default/email").unwrap();
    assert_eq!(email, "sa@example.iam.gserviceaccount.com");
}

#[test]
fn token_errors_quote_like_go() {
    let err = parse_file(br#"{"type":"weird\u0007"}"#).unwrap_err();
    assert_eq!(err, "unknown credential type: \"weird\\a\"");
}
