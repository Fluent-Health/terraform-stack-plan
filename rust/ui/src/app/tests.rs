use axum::http::Method;

use super::{Route, route};
use crate::testutil::{get, req, send, session_app};

#[test]
fn routes_like_go_servemux() {
    let r = |m: &Method, p: &str| route(m, p).map(|m| (m.route, m.caps, m.exact));
    assert_eq!(r(&Method::GET, "/api/me"), Some((Route::Me, vec![], true)));
    assert_eq!(r(&Method::HEAD, "/api/me").map(|x| x.0), Some(Route::Me));
    assert_eq!(r(&Method::POST, "/api/me"), None);
    // A segment that unescapes to "/" never fills a single wildcard.
    assert_eq!(
        r(&Method::GET, "/api/tiers/%2F/pr/1").map(|x| x.0),
        Some(Route::Spa)
    );
    assert_eq!(r(&Method::GET, "/api/%6De").map(|x| x.0), Some(Route::Me));
    assert_eq!(
        r(&Method::GET, "/api/tiers/t/logs/e/a/b%2Fc"),
        Some((
            Route::Logs,
            vec![b"t".to_vec(), b"e".to_vec(), b"a/b/c".to_vec()],
            false
        ))
    );
    assert_eq!(
        r(&Method::GET, "/api/tiers/t/logs/e/"),
        Some((
            Route::Logs,
            vec![b"t".to_vec(), b"e".to_vec(), Vec::new()],
            true
        ))
    );
    assert_eq!(
        r(&Method::GET, "/api/tiers/t/pr/").map(|x| x.0),
        Some(Route::Spa)
    );
}

#[tokio::test]
async fn redirects_and_405_like_go() {
    let (h, _) = session_app(vec![]);
    let rr = get(&h, "/api/tiers/t/logs/e", None).await;
    assert_eq!(
        (rr.status, rr.header("location")),
        (301, "/api/tiers/t/logs/e/")
    );
    assert_eq!(
        rr.body,
        "<a href=\"/api/tiers/t/logs/e/\">Moved Permanently</a>.\n\n"
    );
    let rr = get(&h, "/api/tiers//pr/1", None).await;
    assert_eq!((rr.status, rr.header("location")), (301, "/api/tiers/pr/1"));
    let rr = get(&h, "/a/../api/me?x=1", None).await;
    assert_eq!((rr.status, rr.header("location")), (301, "/api/me?x=1"));
    // Go escapes the already escaped path again when it redirects.
    let rr = get(&h, "/x/../a%20b", None).await;
    assert_eq!(rr.header("location"), "/a%2520b");
    let rr = send(&h, req("PUT", "/auth/logout", None)).await;
    assert_eq!((rr.status, rr.header("allow")), (405, "GET, HEAD, POST"));
    assert_eq!(rr.body, "Method Not Allowed\n");
    let rr = send(&h, req("POST", "/x", None)).await;
    assert_eq!((rr.status, rr.header("allow")), (405, "GET, HEAD"));
    // GET on a POST-only path is the SPA, as Go's catch-all claims it.
    assert_eq!(get(&h, "/auth/logout", None).await.status, 200);
}

#[tokio::test]
async fn healthz_reports_version() {
    let (h, _) = session_app(vec![]);
    let rr = get(&h, "/healthz", None).await;
    let v: serde_json::Value = serde_json::from_str(&rr.body).expect("json");
    assert_eq!(v["status"], "ok");
    assert!(v["version"].is_string() && v["git_sha"].is_string());
    let rr = get(&h, "/ready", None).await;
    assert_eq!((rr.status, rr.body.as_str()), (200, ""));
}

#[tokio::test]
async fn session_cookie_checks() {
    let (h, sess) = session_app(vec![]);
    assert_eq!(get(&h, "/api/me", Some(&sess)).await.status, 200);
    // Quoted values are unquoted, as Go reads cookies.
    let (name, val) = sess.split_once('=').expect("cookie");
    assert_eq!(
        get(&h, "/api/me", Some(&format!("{name}=\"{val}\"")))
            .await
            .status,
        200
    );
    for bad in ["tfsp_ui_session=", "tfsp_ui_session=garbage", "other=1"] {
        let rr = get(&h, "/api/me", Some(bad)).await;
        assert_eq!(
            (rr.status, rr.body.as_str()),
            (401, "unauthorized\n"),
            "{bad}"
        );
    }
    // A session sealed under another secret does not open.
    let (other, _) = {
        let app = crate::app::App::new(crate::app::Config {
            session_secret: "other".into(),
            ..crate::app::Config::default()
        })
        .expect("app");
        let c = crate::testutil::session_for(&app, "ivan@example.com");
        (c, app)
    };
    assert_eq!(get(&h, "/api/me", Some(&other)).await.status, 401);
}

/// The routes that answer without a session: health, the login round trip, logout, the
/// webhook relay (HMAC) and the SPA's static files.
const SESSION_EXEMPT: &[&str] = &[
    "/healthz",
    "/ready",
    "/auth/login",
    "/auth/callback",
    "/auth/logout",
    "/github/webhook",
    "/",
];

#[tokio::test]
async fn every_route_but_the_exempt_ones_needs_a_session() {
    let (h, _) = session_app(vec![crate::testutil::tier("prod", "http://127.0.0.1:9")]);
    for (method, pattern, _) in super::PATTERNS {
        if SESSION_EXEMPT.contains(pattern) {
            continue;
        }
        let path: Vec<&str> = pattern
            .split('/')
            .map(|s| match s {
                "{tier}" => "prod",
                "{stack...}" => "stacks/a",
                s if s.starts_with('{') => "1",
                s => s,
            })
            .collect();
        let mut path = path.join("/");
        if path.ends_with("/lifecycle") {
            path.push_str("?pr=1");
        }
        for cookie in [None, Some("tfsp_ui_session=garbage")] {
            let rr = send(&h, req(method, &path, cookie)).await;
            assert_eq!(
                (rr.status, rr.body.as_str()),
                (401, "unauthorized\n"),
                "{method} {path} cookie {cookie:?}"
            );
        }
    }
    for p in SESSION_EXEMPT {
        assert!(
            super::PATTERNS.iter().any(|(_, pat, _)| pat == p),
            "{p} is not routed"
        );
    }
}
