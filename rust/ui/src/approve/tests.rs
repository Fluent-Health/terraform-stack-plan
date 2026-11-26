use std::sync::{Arc, Mutex};
use std::time::Duration;

use tfstackplan_server_clients::testserver::{Resp, TestServer, serve};

use super::{CLOUD_PLATFORM_SCOPE, approve_result};
use crate::app::{App, Config};
use crate::oauth::OAuth;
use crate::testutil::{Got, TEST_CLIENT_ID, get, req, send, session_for, tier, url_query};

const APPROVE_GRANT: &str = "projects/proj-1/locations/global/entitlements/iam/grants/g1";

#[derive(Debug, Default)]
struct Pam {
    status: u16,
    body: String,
    path: String,
    auth: String,
    quota: String,
    reason: String,
    calls: usize,
}

struct Fixture {
    app: Arc<App>,
    sess: String,
    pam: Arc<Mutex<Pam>>,
    tier_paths: Arc<Mutex<Vec<String>>>,
    _servers: Vec<TestServer>,
}

impl Fixture {
    /// Go's `tierRecorder.waitFor`.
    async fn wait_for(&self, path: &str, d: Duration) -> bool {
        let deadline = tokio::time::Instant::now() + d;
        while tokio::time::Instant::now() < deadline {
            if self
                .tier_paths
                .lock()
                .expect("lock")
                .iter()
                .any(|p| p == path)
            {
                return true;
            }
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
        false
    }

    /// The SPA's popup: a same-origin navigation.
    async fn start(&self, query: &str) -> Got {
        self.start_with(query, &[("sec-fetch-site", "same-origin")])
            .await
    }

    async fn start_with(&self, query: &str, headers: &[(&str, &str)]) -> Got {
        let mut r = req("GET", &format!("/auth/approve?{query}"), Some(&self.sess));
        for (k, v) in headers {
            r.headers_mut().insert(
                axum::http::HeaderName::from_bytes(k.as_bytes()).unwrap(),
                axum::http::HeaderValue::from_str(v).unwrap(),
            );
        }
        send(&self.app, r).await
    }

    async fn callback(&self, query: &str, sess: &str) -> Got {
        get(
            &self.app,
            &format!("/auth/approve/callback?{query}"),
            Some(sess),
        )
        .await
    }
}

/// Go's `approveTestApp`: a fake Google token endpoint, a fake PAM and a fake tier that
/// records the reconcile nudge.
async fn fixture() -> Fixture {
    let token_srv = serve(|_| {
        Resp::ok(r#"{"access_token":"user-cloud-token","token_type":"Bearer","expires_in":3600}"#)
            .header("Content-Type", "application/json")
    })
    .await
    .expect("token");
    let pam = Arc::new(Mutex::new(Pam {
        status: 200,
        ..Pam::default()
    }));
    let p = Arc::clone(&pam);
    let pam_srv = serve(move |r| {
        let mut p = p.lock().expect("lock");
        p.calls += 1;
        p.path = r.path.clone();
        p.auth = r.header("authorization").into();
        p.quota = r.header("x-goog-user-project").into();
        p.reason = String::from_utf8_lossy(&r.body).into_owned();
        Resp::status(p.status, p.body.clone())
    })
    .await
    .expect("pam");
    let tier_paths: Arc<Mutex<Vec<String>>> = Arc::default();
    let tp = Arc::clone(&tier_paths);
    let tier_srv = serve(move |r| {
        tp.lock()
            .expect("lock")
            .push(format!("{} {}", r.method, r.path));
        Resp::status(202, "")
    })
    .await
    .expect("tier");
    let app = App::new(Config {
        public_base_url: "https://ui.example.com".into(),
        session_secret: "s3cret".into(),
        allowed_domain: "example.com".into(),
        oauth: Some(OAuth {
            client_id: TEST_CLIENT_ID.into(),
            client_secret: "shh".into(),
            auth_url: format!("{}/auth", token_srv.url),
            token_url: format!("{}/token", token_srv.url),
            redirect_url: "https://ui.example.com/auth/callback".into(),
            scopes: vec!["openid".into(), "email".into(), "profile".into()],
        }),
        quota_project: "svc-proj".into(),
        pam_base_url: pam_srv.url.clone(),
        tiers: vec![tier("prod", &tier_srv.url)],
        ..Config::default()
    })
    .expect("app");
    let sess = session_for(&app, "ivan@example.com");
    Fixture {
        app,
        sess,
        pam,
        tier_paths,
        _servers: vec![token_srv, pam_srv, tier_srv],
    }
}

fn q(s: &str) -> String {
    crate::gohttp::escape(s.as_bytes(), crate::gohttp::Mode::QueryComponent)
}

/// Go's `mustState`.
fn must_state(rr: &Got) -> String {
    assert_eq!(rr.status, 302, "start: {}", rr.body);
    url_query(rr.header("location"), "state")
}

// Go: TestApproveFlow.
#[tokio::test]
async fn approve_flow() {
    let f = fixture().await;
    // Start: a redirect to Google with the cloud-platform scope, incremental consent and
    // the sealed intent as state.
    let rr = f
        .start(&format!(
            "tier=prod&decision=approve&reason=lgtm&grant={}",
            q(APPROVE_GRANT)
        ))
        .await;
    assert_eq!(rr.status, 302, "{}", rr.body);
    let loc = rr.header("location");
    assert_eq!(url_query(loc, "scope"), CLOUD_PLATFORM_SCOPE);
    assert_eq!(url_query(loc, "include_granted_scopes"), "true");
    assert_eq!(
        url_query(loc, "redirect_uri"),
        "https://ui.example.com/auth/approve/callback"
    );
    let state = url_query(loc, "state");

    // Callback: code exchange, then PAM :approve with the user's token.
    let rr2 = f
        .callback(&format!("code=c0de&state={}", q(&state)), &f.sess)
        .await;
    assert_eq!(rr2.status, 200);
    assert!(rr2.body.contains(r#""ok":true"#), "{}", rr2.body);
    {
        let p = f.pam.lock().expect("lock");
        assert_eq!(p.calls, 1);
        assert_eq!(p.path, format!("/{APPROVE_GRANT}:approve"));
        assert_eq!(
            (p.auth.as_str(), p.quota.as_str()),
            ("Bearer user-cloud-token", "svc-proj")
        );
        assert!(p.reason.contains("lgtm"), "{}", p.reason);
    }
    assert!(rr2.body.contains("postMessage"));
    // A decision nudges the tier's gate reconcile so watching pages update.
    assert!(
        f.wait_for("POST /api/gate/reconcile", Duration::from_secs(2))
            .await,
        "expected a gate-reconcile nudge; saw {:?}",
        f.tier_paths.lock().expect("lock")
    );
}

// Go: TestApproveEmptyReasonDefaults.
#[tokio::test]
async fn approve_empty_reason_defaults() {
    let f = fixture().await;
    let state = must_state(
        &f.start(&format!(
            "tier=prod&decision=approve&grant={}",
            q(APPROVE_GRANT)
        ))
        .await,
    );
    let rr = f
        .callback(&format!("code=c&state={}", q(&state)), &f.sess)
        .await;
    assert!(rr.body.contains(r#""ok":true"#), "{}", rr.body);
    let reason = f.pam.lock().expect("lock").reason.clone();
    assert!(reason.contains("approved via tfstackplan UI"), "{reason}");
}

// Go: TestApprovePAM403Verbatim.
#[tokio::test]
async fn approve_pam_403_verbatim() {
    let f = fixture().await;
    {
        let mut p = f.pam.lock().expect("lock");
        p.status = 403;
        p.body =
            r#"{"error":{"message":"Permission 'privilegedaccessmanager.grants.approve' denied"}}"#
                .into();
    }
    let state = must_state(
        &f.start(&format!(
            "tier=prod&decision=deny&grant={}",
            q(APPROVE_GRANT)
        ))
        .await,
    );
    let rr = f
        .callback(&format!("code=c&state={}", q(&state)), &f.sess)
        .await;
    assert!(rr.body.contains(r#""ok":false"#), "{}", rr.body);
    assert!(
        rr.body.contains("privilegedaccessmanager.grants.approve"),
        "{}",
        rr.body
    );
    assert!(f.pam.lock().expect("lock").path.ends_with(":deny"));
    // A failed decision changes nothing, so no nudge.
    assert!(
        !f.wait_for("POST /api/gate/reconcile", Duration::from_millis(200))
            .await
    );
}

// Go: TestApproveGuards.
#[tokio::test]
async fn approve_guards() {
    let f = fixture().await;
    let g = q(APPROVE_GRANT);
    assert_eq!(
        f.start(&format!("tier=nope&decision=approve&grant={g}"))
            .await
            .status,
        404
    );
    assert_eq!(
        f.start(&format!("tier=prod&decision=maybe&grant={g}"))
            .await
            .status,
        400
    );
    let rr = f
        .start("tier=prod&decision=approve&grant=not-a-grant")
        .await;
    assert_eq!(
        (rr.status, rr.body.as_str()),
        (400, "not a PAM grant resource name: \"not-a-grant\"\n")
    );
    let long = "x".repeat(513);
    let rr = f
        .start(&format!(
            "tier=prod&decision=approve&grant={g}&reason={long}"
        ))
        .await;
    assert_eq!((rr.status, rr.body.as_str()), (400, "reason too long\n"));
    // No session: 401 before anything happens.
    let rr = get(
        &f.app,
        &format!("/auth/approve?tier=prod&decision=approve&grant={g}"),
        None,
    )
    .await;
    assert_eq!(rr.status, 401);

    // Callback guards: tampered state, user cancel, foreign session; none reaches PAM.
    let valid = must_state(
        &f.start(&format!("tier=prod&decision=approve&grant={g}"))
            .await,
    );
    // Another user's session (same secret) must not finish this intent.
    let foreign = session_for(&f.app, "other@example.com");
    let cases = [
        ("code=c&state=bogus".to_string(), f.sess.clone(), "invalid"),
        (
            format!("error=access_denied&state={}", q(&valid)),
            f.sess.clone(),
            "consent not granted",
        ),
        (
            format!("code=c&state={}", q(&valid)),
            foreign,
            "different session",
        ),
    ];
    for (query, sess, want) in cases {
        let rr = f.callback(&query, &sess).await;
        assert!(rr.body.contains(want), "want {want:?} in {}", rr.body);
    }
    assert_eq!(
        f.pam.lock().expect("lock").calls,
        0,
        "no guard case may reach PAM"
    );
}

#[tokio::test]
async fn approve_without_config_is_501() {
    let (app, sess) = crate::testutil::session_app(vec![tier("prod", "http://127.0.0.1:9")]);
    let rr = get(&app, "/auth/approve?tier=prod", Some(&sess)).await;
    assert_eq!(
        (rr.status, rr.body.as_str()),
        (
            501,
            "approvals are not configured (oauth + public_base_url required)\n"
        )
    );
    let rr = get(&app, "/auth/approve/callback", Some(&sess)).await;
    assert_eq!(
        (rr.status, rr.body.as_str()),
        (501, "approvals are not configured\n")
    );
}

/// Byte-for-byte what Go's html/template renders for this message.
#[tokio::test]
async fn approve_result_matches_go_template() {
    let msg =
        "pam: 403: {\"error\":{\"message\":\"Permission 'x' denied <b>&</b> + \u{2028} \u{e9}\"}}";
    let r = approve_result(false, msg);
    assert_eq!(r.status().as_u16(), 200);
    let body = axum::body::to_bytes(r.into_body(), usize::MAX)
        .await
        .expect("body");
    let body = String::from_utf8_lossy(&body);
    assert!(body.contains("<p>pam: 403: {&#34;error&#34;:{&#34;message&#34;:&#34;Permission &#39;x&#39; denied &lt;b&gt;&amp;&lt;/b&gt; &#43; \u{2028} \u{e9}&#34;}}</p>"), "{body}");
    assert!(body.contains("var payload = {\"message\":\"pam: 403: {\\\"error\\\":{\\\"message\\\":\\\"Permission 'x' denied \\u003cb\\u003e\\u0026\\u003c/b\\u003e + \\u2028 \u{e9}\\\"}}\",\"ok\":false,\"type\":\"tfsp-approval\"};"), "{body}");
}

#[tokio::test]
async fn approve_start_refuses_cross_site_requests() {
    let f = fixture().await;
    let g = q(APPROVE_GRANT);
    let query = format!("tier=prod&decision=approve&grant={g}");
    let refused: &[&[(&str, &str)]] = &[
        &[("sec-fetch-site", "cross-site")],
        &[("sec-fetch-site", "same-site")],
        &[("sec-fetch-site", "none")],
        // Sec-Fetch-Site wins over a matching Origin.
        &[
            ("sec-fetch-site", "cross-site"),
            ("origin", "https://ui.example.com"),
        ],
        &[],
        &[("referer", "https://evil.example/https://ui.example.com/")],
        &[("referer", "https://ui.example.com.evil.example/")],
        &[("referer", "http://ui.example.com/")],
        &[("referer", "https://ui.example.com:8443/")],
        &[("origin", "null")],
        // A present Origin is decisive; the Referer is not consulted.
        &[
            ("origin", "https://evil.example"),
            ("referer", "https://ui.example.com/"),
        ],
    ];
    for h in refused {
        let rr = f.start_with(&query, h).await;
        assert_eq!(
            (rr.status, rr.body.as_str()),
            (403, "cross-site approval request refused\n"),
            "{h:?}"
        );
    }
    let accepted: &[&[(&str, &str)]] = &[
        &[("sec-fetch-site", "same-origin")],
        &[("origin", "https://ui.example.com")],
        &[("referer", "https://ui.example.com/pr/7")],
        &[("referer", "https://ui.example.com:443/")],
    ];
    for h in accepted {
        let rr = f.start_with(&query, h).await;
        assert_eq!(rr.status, 302, "{h:?}");
    }
    assert_eq!(f.pam.lock().unwrap().calls, 0);
}
