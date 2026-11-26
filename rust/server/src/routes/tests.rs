// Ported from internal/server/app_test.go (auth, naming, retired routes), plus
// the spec check that every operation of api/openapi.yaml is routed with its
// scopes, and Go ServeMux's 404/405 shapes.
use std::sync::Arc;

use tfstackplan_gate::events::{Init, StackState};
use tfstackplan_server_clients::github::MockGitHub;

use super::*;
use crate::testutil::{FakeExecutor, app, app_with, fake_oidc, seed_init, send};
use crate::{App, Config};

#[test]
fn status_context_names() {
    assert_eq!(crate::render::status_context("staging"), "plan/staging");
    assert_eq!(crate::render::status_context(""), "plan");
}

#[test]
fn consolidated_check_naming() {
    let (_d, s) = crate::testutil::store();
    let mut a = App::new(
        s,
        Arc::new(MockGitHub::default()),
        Config {
            environment: "nonprod".into(),
            ..Config::default()
        },
    );
    assert_eq!(a.plan_check_name("nonprod"), "plan/nonprod");
    assert_eq!(a.merge_gate_check_name("nonprod"), "apply-lock/nonprod");
    a.executor = Some(Arc::new(FakeExecutor::default()));
    assert_eq!(a.plan_check_name("nonprod"), "terraform/nonprod");
    assert_eq!(a.merge_gate_check_name("nonprod"), "terraform/nonprod");
}

#[test]
fn ui_url() {
    let (_d, a) = app_with(
        Arc::new(MockGitHub::default()),
        Config {
            ui_base_url: "https://ui.example.test".into(),
            environment: "nonprod".into(),
            ..Config::default()
        },
    );
    assert_eq!(a.ui_url(7, "exec-123"), "https://ui.example.test/pr/7");
    assert_eq!(
        a.ui_url(0, "exec-123"),
        "https://ui.example.test/t/nonprod/e/exec-123"
    );
    let (_d2, b) = app();
    assert_eq!(b.ui_url(7, "exec-123"), "");
}

#[tokio::test]
async fn ready() {
    let (_d, a) = app();
    assert_eq!(send(&a, "GET", "/ready", "", "").await.status, 200);
}

const LEGACY_HS256: &str = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJydW5uZXIiLCJhdWQiOiJhcGkifQ.3vJ0X1Zb6nYFhZ0m9m0v3wJ8vHc0d9XoJb5r8sQxYQU";

fn with_verifier(
    principals: &[(&str, &[&str])],
    tokens: &[(&str, &str)],
) -> (tempfile::TempDir, Arc<App>) {
    let (d, s) = crate::testutil::store();
    let mut a = App::new(
        s,
        Arc::new(MockGitHub::default()),
        Config {
            api_principals: principals
                .iter()
                .map(|(e, sc)| {
                    (
                        (*e).to_string(),
                        sc.iter().map(|x| (*x).to_string()).collect(),
                    )
                })
                .collect(),
            ..Config::default()
        },
    );
    a.api_verifier = Some(fake_oidc(tokens));
    (d, Arc::new(a))
}

#[tokio::test]
async fn bearer_auth_rejects_malformed_and_legacy_tokens() {
    let (_d, a) = with_verifier(&[], &[("good-token", "runner@x.iam.gserviceaccount.com")]);
    let legacy = format!("Bearer {LEGACY_HS256}");
    for h in [
        "",
        "Bearer notajwt",
        "s3cret",
        "Bearer wrong-token",
        legacy.as_str(),
    ] {
        let headers: Vec<(&str, &str)> = if h.is_empty() {
            vec![]
        } else {
            vec![("Authorization", h)]
        };
        let r = crate::testutil::send_with(&a, "POST", "/api/init", "", "", &headers).await;
        assert_eq!(r.status, 401, "auth {h:?}");
    }
}

#[tokio::test]
async fn auth_disabled_without_verifier() {
    let (_d, a) = app();
    assert_ne!(send(&a, "POST", "/api/init", "", "").await.status, 401);
}

#[tokio::test]
async fn oidc_auth_scopes() {
    let (_d, a) = with_verifier(
        &[
            ("runner@x.iam.gserviceaccount.com", &["report"]),
            ("viewer@example.com", &["read"]),
            ("ops@example.com", &["read", "admin"]),
        ],
        &[
            ("tok-runner", "runner@x.iam.gserviceaccount.com"),
            ("tok-viewer", "viewer@example.com"),
            ("tok-ops", "ops@example.com"),
            ("tok-nobody", "stranger@example.com"),
        ],
    );
    let cases: &[(&str, &str, &str, u16)] = &[
        ("tok-runner", "POST", "/api/init", 0),
        ("tok-runner", "POST", "/api/claims/release", 0),
        ("tok-runner", "POST", "/api/claims/list", 0),
        ("tok-runner", "GET", "/api/execution/nope", 0),
        ("tok-viewer", "POST", "/api/init", 403),
        ("tok-viewer", "POST", "/api/claims/release", 403),
        ("tok-viewer", "GET", "/api/execution/nope", 0),
        ("tok-ops", "POST", "/api/claims/release", 0),
        ("tok-ops", "POST", "/api/init", 403),
        ("tok-nobody", "POST", "/api/init", 403),
        ("garbage", "POST", "/api/init", 401),
        ("", "POST", "/api/init", 401),
    ];
    for &(tok, method, path, want) in cases {
        let got = send(&a, method, path, tok, "").await.status;
        if want == 0 {
            assert!(
                got != 401 && got != 403,
                "{tok} {method} {path} = {got}, want pass-through"
            );
        } else {
            assert_eq!(got, want, "{tok} {method} {path}");
        }
    }
}

#[tokio::test]
async fn hs256_rejected_oidc_accepted() {
    let (_d, a) = with_verifier(
        &[("runner@x.iam.gserviceaccount.com", &["report"])],
        &[("tok-runner", "runner@x.iam.gserviceaccount.com")],
    );
    let ok = send(&a, "POST", "/api/init", "tok-runner", "").await.status;
    assert!(ok != 401 && ok != 403, "oidc token = {ok}");
    assert_eq!(
        send(&a, "POST", "/api/init", LEGACY_HS256, "").await.status,
        401
    );
}

#[tokio::test]
async fn auth_actor_is_the_lowered_email() {
    let (_d, a) = with_verifier(
        &[("runner@x.iam.gserviceaccount.com", &["report"])],
        &[("tok-runner", "Runner@x.iam.gserviceaccount.com")],
    );
    let mut req = Request::new(Body::empty());
    req.headers_mut().insert(
        header::AUTHORIZATION,
        HeaderValue::from_static("Bearer tok-runner"),
    );
    assert_eq!(
        a.auth(&req, REPORT).await.ok(),
        Some("runner@x.iam.gserviceaccount.com".into())
    );
    let mut req2 = Request::new(Body::empty());
    req2.headers_mut().insert(
        header::AUTHORIZATION,
        HeaderValue::from_str(&format!("Bearer {LEGACY_HS256}")).unwrap(),
    );
    assert_eq!(
        a.auth(&req2, REPORT)
            .await
            .err()
            .map(|r| r.status().as_u16()),
        Some(401)
    );
}

#[tokio::test]
async fn oidc_only_enforced_without_secret() {
    let (_d, a) = with_verifier(&[], &[]);
    assert_eq!(send(&a, "POST", "/api/init", "", "").await.status, 401);
}

#[tokio::test]
async fn retired_viewer_routes_are_gone() {
    let (_d, a) = app();
    seed_init(
        &a,
        Init {
            id: "e1".into(),
            repo: "o/r".into(),
            environment: "staging".into(),
            stacks: vec![StackState {
                path: "stacks/a".into(),
                ..StackState::default()
            }],
            ..Init::default()
        },
    )
    .await;
    for path in [
        "/",
        "/live/e1",
        "/live/e1/events",
        "/pr/7",
        "/assets/app.css",
        "/img/e1.svg",
    ] {
        let r = send(&a, "GET", path, "", "").await;
        assert_eq!(r.status, 404, "{path}");
        assert_eq!(r.text(), "404 page not found\n");
        assert_eq!(r.content_type, "text/plain; charset=utf-8");
    }
}

#[tokio::test]
async fn wrong_method_is_gos_405() {
    let (_d, a) = app();
    let r = send(&a, "GET", "/api/init", "", "").await;
    assert_eq!(r.status, 405);
    assert_eq!(r.text(), "Method Not Allowed\n");
    assert_eq!(
        r.headers.get("allow").and_then(|v| v.to_str().ok()),
        Some("POST")
    );
    let r = send(&a, "POST", "/api/executions", "", "").await;
    assert_eq!(
        r.headers.get("allow").and_then(|v| v.to_str().ok()),
        Some("GET, HEAD")
    );
}

#[tokio::test]
async fn bad_path_parameter_is_the_generated_wrappers_400() {
    let (_d, a) = app();
    let r = send(&a, "GET", "/api/pr/abc", "", "").await;
    assert_eq!(r.status, 400);
    assert_eq!(
        r.text(),
        "Invalid format for parameter n: error binding string parameter: strconv.ParseInt: parsing \"abc\": invalid syntax\n"
    );
}

/// The operations of api/openapi.yaml: (method, path, scopes), read without a
/// YAML library from the spec's fixed layout.
fn spec_operations() -> Vec<(String, String, Vec<String>)> {
    let spec = std::fs::read_to_string(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../../api/openapi.yaml"),
    )
    .expect("read spec");
    let mut out = Vec::new();
    let (mut path, mut method) = (String::new(), String::new());
    let mut in_paths = false;
    for line in spec.lines() {
        if line == "paths:" {
            in_paths = true;
            continue;
        }
        if in_paths && !line.starts_with(' ') && !line.is_empty() {
            break;
        }
        if let Some(p) = line.strip_prefix("  /").and_then(|p| p.strip_suffix(':')) {
            path = format!("/{p}");
        } else if let Some(m) = line.strip_prefix("    ").and_then(|m| m.strip_suffix(':'))
            && ["get", "post", "put", "delete", "patch"].contains(&m)
        {
            method = m.to_uppercase();
        } else if let Some(sc) = line
            .trim()
            .strip_prefix("- oidc: [")
            .and_then(|s| s.strip_suffix(']'))
        {
            let scopes = sc.split(',').map(|s| s.trim().to_string()).collect();
            out.push((method.clone(), path.clone(), scopes));
        }
    }
    out
}

#[test]
fn every_spec_operation_is_routed_with_its_scopes() {
    let spec = spec_operations();
    assert_eq!(
        spec.len(),
        API_ROUTES.len(),
        "operation count differs from the spec"
    );
    for (method, path, scopes) in spec {
        let r = API_ROUTES
            .iter()
            .find(|(m, p, _)| *m == method && *p == path)
            .unwrap_or_else(|| panic!("{method} {path} is in the spec but not routed"));
        let mut want = scopes.clone();
        let mut got: Vec<String> = r.2.iter().map(|s| (*s).to_string()).collect();
        want.sort();
        got.sort();
        assert_eq!(got, want, "scopes of {method} {path}");
    }
}

/// The routes that answer without the `/api` bearer: health, Pub/Sub (its own OIDC
/// push token) and the GitHub webhook (HMAC).
const AUTH_EXEMPT: &[(&str, &str)] = &[
    ("GET", "/ready"),
    ("GET", "/healthz"),
    ("POST", "/pubsub/push"),
    ("POST", "/pubsub/cloud-builds"),
    ("POST", "/github/webhook"),
];

/// A concrete request path for a route pattern; parameters that bind before auth get
/// values that bind.
fn concrete(pattern: &str) -> String {
    let p: Vec<String> = pattern
        .split('/')
        .map(|s| match s {
            "{*stack}" => "stacks/a".to_string(),
            s if s.starts_with('{') => "1".to_string(),
            s => s.to_string(),
        })
        .collect();
    let p = p.join("/");
    if p == "/api/lifecycle" {
        format!("{p}?pr=1")
    } else {
        p
    }
}

#[tokio::test]
async fn every_route_but_the_exempt_ones_needs_a_scoped_token() {
    let (_d, a) = with_verifier(
        &[
            ("report@example.com", &["report"]),
            ("read@example.com", &["read"]),
            ("admin@example.com", &["admin"]),
        ],
        &[
            ("tok-report", "report@example.com"),
            ("tok-read", "read@example.com"),
            ("tok-admin", "admin@example.com"),
            ("tok-nobody", "stranger@example.com"),
        ],
    );
    let mut routes: Vec<(&str, &str, &[&str])> = API_ROUTES.to_vec();
    routes.extend([
        ("GET", "/logs/{exec}/{*stack}", ANY),
        ("GET", "/plan/{exec}/{*stack}", ANY),
        ("GET", "/api/execution/{id}/events", ANY),
    ]);
    for (method, pattern, scopes) in routes {
        assert!(
            !AUTH_EXEMPT.iter().any(|(_, p)| *p == pattern),
            "{pattern} is both gated and exempt"
        );
        let path = concrete(pattern);
        for tok in ["", "garbage"] {
            let got = send(&a, method, &path, tok, "").await.status;
            assert_eq!(got, 401, "{method} {path} with token {tok:?}");
        }
        assert_eq!(
            send(&a, method, &path, "tok-nobody", "").await.status,
            403,
            "{method} {path} with no scope"
        );
        for (tok, scope) in [
            ("tok-report", SCOPE_REPORT),
            ("tok-read", SCOPE_READ),
            ("tok-admin", SCOPE_ADMIN),
        ] {
            if !scopes.contains(&scope) {
                assert_eq!(
                    send(&a, method, &path, tok, "").await.status,
                    403,
                    "{method} {path} with scope {scope}"
                );
            }
        }
    }
    // The exempt routes skip the bearer middleware (whose texts are "unauthorized" and
    // "forbidden"); their own checks answer instead.
    for (method, path) in AUTH_EXEMPT {
        let r = send(&a, method, path, "", "").await;
        let text = r.text();
        assert!(
            text != "unauthorized\n" && text != "forbidden\n",
            "{method} {path} = {} {text:?}: exempt routes skip the bearer check",
            r.status
        );
    }
}

#[tokio::test]
async fn api_body_over_the_limit_is_413() {
    let (_d, a) = app();
    let big = "x".repeat(API_BODY_LIMIT + 1);
    let r = send(&a, "POST", "/api/logs", "", &big).await;
    assert_eq!(r.status, 413);
    assert_eq!(r.text(), "request body too large\n");
}

#[tokio::test]
async fn push_bodies_over_2_mib_are_413() {
    let (_d, a) = app();
    let big = "x".repeat(PUSH_BODY_LIMIT + 1);
    for path in ["/pubsub/push", "/pubsub/cloud-builds", "/github/webhook"] {
        assert_eq!(send(&a, "POST", path, "", &big).await.status, 413, "{path}");
    }
}

#[tokio::test]
async fn unreadable_api_body_is_400() {
    use tower::ServiceExt as _;
    let (_d, a) = app();
    let body = Body::from_stream(futures_util::stream::iter([Err::<Bytes, _>(
        std::io::Error::other("connection reset"),
    )]));
    let req = Request::builder()
        .method("POST")
        .uri("/api/logs")
        .body(body)
        .unwrap();
    let resp = a.router().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 400);
}
