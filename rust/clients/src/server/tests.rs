use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};

use wiremock::matchers::{body_json, header, method, path};
use wiremock::{Mock, MockServer, Request, Respond, ResponseTemplate};

use super::*;

/// Runs a blocking client call off the test's async runtime.
async fn blocking<T: Send + 'static>(f: impl FnOnce() -> T + Send + 'static) -> T {
    tokio::task::spawn_blocking(f).await.unwrap()
}

fn token(t: &'static str) -> Option<TokenFn> {
    Some(Box::new(move || Ok(t.to_string())))
}

fn update() -> Update {
    Update {
        id: "e1".into(),
        stack: "stacks/c".into(),
        status: "safe".into(),
        detail: String::new(),
    }
}

// Go: TestClientTokenSource, TestPostsHitRightPathsWithAuth (the calls the CLI faces make).
#[tokio::test(flavor = "multi_thread")]
async fn posts_hit_the_right_paths_with_auth() {
    let srv = MockServer::start().await;
    for p in [
        "/api/update",
        "/api/logs",
        "/api/claims/release",
        "/api/admin/grants/release",
        "/api/admin/executions/cancel",
        "/api/admin/gates/satisfy",
        "/api/admin/checks/override",
    ] {
        Mock::given(method("POST"))
            .and(path(p))
            .and(header("Authorization", "Bearer tok-abc"))
            .and(header("Content-Type", "application/json"))
            .respond_with(ResponseTemplate::new(200))
            .expect(1)
            .mount(&srv)
            .await;
    }
    let url = srv.uri();
    blocking(move || {
        let c = Client::new(&url, token("tok-abc")).unwrap();
        c.update(&update()).unwrap();
        c.log_chunk(&LogChunk {
            id: "e1".into(),
            stack: "a".into(),
            data: "hello".into(),
        })
        .unwrap();
        c.claims_release("dev", 7, "").unwrap();
        c.admin_grants_release(7, "dev", "iam", "proj", "why")
            .unwrap();
        c.admin_executions_cancel("e1", "why").unwrap();
        c.admin_gates_satisfy(7, "dev", "iam", "proj", "why")
            .unwrap();
        c.admin_checks_override(7, "dev", "plan/dev", "success", "why")
            .unwrap();
    })
    .await;
}

// Go: TestClientLogChunk (path and body).
#[tokio::test(flavor = "multi_thread")]
async fn log_chunk_body() {
    let srv = MockServer::start().await;
    Mock::given(method("POST"))
        .and(path("/api/logs"))
        .and(body_json(
            serde_json::json!({"id":"e1","stack":"a/b","data":"line\n"}),
        ))
        .respond_with(ResponseTemplate::new(200))
        .expect(1)
        .mount(&srv)
        .await;
    let url = srv.uri();
    blocking(move || {
        Client::new(&url, None)
            .unwrap()
            .log_chunk(&LogChunk {
                id: "e1".into(),
                stack: "a/b".into(),
                data: "line\n".into(),
            })
            .unwrap();
    })
    .await;
}

// Go: TestOfflineClientIsNoop.
#[test]
fn offline_client_is_a_noop() {
    let c = Client::new("", None).unwrap();
    assert!(!c.enabled());
    c.update(&update()).unwrap();
    assert_eq!(c.claims_list("dev").unwrap(), vec![]);
    c.claims_release("dev", 1, "").unwrap();
}

// Go: TestNewClientTrimsTrailingSlash.
#[test]
fn trims_trailing_slash() {
    let c = Client::new("https://srv.example/", None).unwrap();
    assert_eq!(c.base_url, "https://srv.example");
    assert!(c.enabled());
}

// Go: TestPostReturnsErrorOnNon2xx.
#[tokio::test(flavor = "multi_thread")]
async fn non_2xx_is_an_error_naming_the_path() {
    let srv = MockServer::start().await;
    Mock::given(method("POST"))
        .respond_with(ResponseTemplate::new(500).set_body_string("boom\n"))
        .mount(&srv)
        .await;
    let url = srv.uri();
    let err = blocking(move || {
        let mut c = Client::new(&url, None).unwrap();
        c.set_update_backoff(Duration::from_millis(1));
        c.claims_release("dev", 1, "a").unwrap_err()
    })
    .await;
    assert_eq!(err, "post /api/claims/release: 500: boom\n");
}

struct Codes {
    codes: Vec<u16>,
    calls: Arc<AtomicUsize>,
}

impl Respond for Codes {
    fn respond(&self, _: &Request) -> ResponseTemplate {
        let n = self.calls.fetch_add(1, Ordering::SeqCst);
        ResponseTemplate::new(self.codes[n.min(self.codes.len() - 1)])
    }
}

// Go: TestUpdateRetriesTransient5xx.
#[tokio::test(flavor = "multi_thread")]
async fn update_retries_transient_5xx() {
    for (name, codes, want_calls, want_err) in [
        ("one 500 then ok", vec![500, 200], 2, false),
        ("persistent 500", vec![500, 500, 500], 3, true),
        ("4xx is final", vec![400], 1, true),
    ] {
        let srv = MockServer::start().await;
        let calls = Arc::new(AtomicUsize::new(0));
        Mock::given(method("POST"))
            .and(path("/api/update"))
            .respond_with(Codes {
                codes,
                calls: calls.clone(),
            })
            .mount(&srv)
            .await;
        let url = srv.uri();
        let res = blocking(move || {
            let mut c = Client::new(&url, None).unwrap();
            c.set_update_backoff(Duration::from_millis(1));
            c.update(&update())
        })
        .await;
        assert_eq!(res.is_err(), want_err, "{name}: {res:?}");
        assert_eq!(calls.load(Ordering::SeqCst), want_calls, "{name}");
    }
}

// Go: TestClientEnrichedAuthErrors, with a made-up identity.
#[tokio::test(flavor = "multi_thread")]
async fn auth_errors_name_identity_and_audience() {
    let srv = MockServer::start().await;
    Mock::given(method("POST"))
        .respond_with(ResponseTemplate::new(401).set_body_string("unauthorized\n"))
        .mount(&srv)
        .await;
    let url = srv.uri();
    let err = blocking(move || {
        // {"email":"operator@example.com"}
        let mut c = Client::new(
            &url,
            token("eyJhbGciOiJub25lIn0.eyJlbWFpbCI6Im9wZXJhdG9yQGV4YW1wbGUuY29tIn0."),
        )
        .unwrap();
        c.set_audience("https://audience-example");
        c.claims_list("dev").unwrap_err()
    })
    .await;
    assert!(
        err.starts_with("post /api/claims/list: 401: unauthorized\n"),
        "{err}"
    );
    assert!(err.contains("Identity: operator@example.com"), "{err}");
    assert!(err.contains("Audience: https://audience-example"), "{err}");
    assert!(err.contains("api_admins"), "{err}");
}

#[tokio::test(flavor = "multi_thread")]
async fn claims_list_decodes_claims_and_null() {
    let srv = MockServer::start().await;
    Mock::given(method("POST"))
        .and(path("/api/claims/list"))
        .and(body_json(serde_json::json!({"environment":"dev"})))
        .respond_with(ResponseTemplate::new(200).set_body_string(
            r#"[{"environment":"dev","stack_path":"stacks/a","owner_pr":7,"expires_at":"2026-01-02T03:04:05Z"}]"#,
        ))
        .mount(&srv)
        .await;
    Mock::given(method("POST"))
        .and(path("/api/claims/list"))
        .and(body_json(serde_json::json!({"environment":"prod"})))
        .respond_with(ResponseTemplate::new(200).set_body_string("null"))
        .mount(&srv)
        .await;
    let url = srv.uri();
    let (dev, prod) = blocking(move || {
        let c = Client::new(&url, None).unwrap();
        (
            c.claims_list("dev").unwrap(),
            c.claims_list("prod").unwrap(),
        )
    })
    .await;
    assert_eq!(
        dev,
        vec![Claim {
            environment: "dev".into(),
            stack_path: "stacks/a".into(),
            owner_pr: 7,
            expires_at: "2026-01-02T03:04:05Z".into(),
        }]
    );
    assert!(prod.is_empty());
}
