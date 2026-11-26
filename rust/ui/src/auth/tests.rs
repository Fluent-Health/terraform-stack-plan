use std::sync::Arc;

use serde_json::{Map, Value, json};
use tfstackplan_server_clients::gauth::ClaimsVerifier;
use tfstackplan_server_clients::gauth::gauthtest::Issuer;
use tfstackplan_server_clients::testserver::{Resp, TestServer, serve};

use super::{NEXT_COOKIE, STATE_COOKIE, safe_local_path, signed_in_page};
use crate::app::{App, Config};
use crate::oauth::OAuth;
use crate::session::SESSION_COOKIE;
use crate::testutil::{Got, TEST_CLIENT_ID, get, req, send, url_query};

/// Go's `fakeGoogle`: a token endpoint whose code exchange returns an id_token with
/// `claims`, and a verifier checking this issuer's signatures for real.
async fn fake_google(
    claims: Map<String, Value>,
) -> (OAuth, ClaimsVerifier, TestServer, TestServer) {
    let issuer = Issuer::new().expect("issuer");
    let jwks = issuer.serve_jwks().await.expect("jwks");
    let iss = issuer.clone();
    let token_srv = serve(move |_| match iss.mint_id_token_claims(claims.clone()) {
        Ok(id) => Resp::ok(
            json!({"access_token":"at","token_type":"Bearer","expires_in":3600,"id_token":id})
                .to_string(),
        )
        .header("Content-Type", "application/json"),
        Err(e) => Resp::status(500, e.to_string()),
    })
    .await
    .expect("token server");
    let verify =
        ClaimsVerifier::with_certs_url(vec![TEST_CLIENT_ID.into()], &jwks.url).expect("verifier");
    let oauth = OAuth {
        client_id: TEST_CLIENT_ID.into(),
        client_secret: "shh".into(),
        auth_url: format!("{}/auth", token_srv.url),
        token_url: format!("{}/token", token_srv.url),
        redirect_url: "https://ui.example.com/auth/callback".into(),
        scopes: vec!["openid".into(), "email".into(), "profile".into()],
    };
    (oauth, verify, token_srv, jwks)
}

fn id_claims(email: &str, hd: &str) -> Map<String, Value> {
    let now = jiff::Timestamp::now().as_second();
    json!({
        "iss": "https://accounts.google.com", "aud": TEST_CLIENT_ID, "sub": "42",
        "email": email, "email_verified": true, "hd": hd,
        "iat": now, "exp": now + 3600,
    })
    .as_object()
    .cloned()
    .expect("object")
}

async fn login_app(claims: Map<String, Value>) -> (Arc<App>, TestServer, TestServer) {
    let (oauth, verify, t, j) = fake_google(claims).await;
    let app = App::new(Config {
        session_secret: "s3cret".into(),
        allowed_domain: "example.com".into(),
        oauth: Some(oauth),
        verify_id_token: Some(verify),
        ..Config::default()
    })
    .expect("app");
    (app, t, j)
}

/// Go's `driveLogin`: the whole browser flow; the session cookie from the callback (None
/// when it failed) and the callback response.
async fn drive_login(app: &Arc<App>) -> (Option<String>, Got) {
    let rr = get(app, "/auth/login?next=/pr/7", None).await;
    assert_eq!(rr.status, 302, "login: {}", rr.body);
    let state = rr.cookie(STATE_COOKIE).expect("no state cookie set");
    let next = rr.cookie(NEXT_COOKIE);
    assert_eq!(url_query(rr.header("location"), "state"), state);
    let mut cookie = format!("{STATE_COOKIE}={state}");
    if let Some(n) = next {
        cookie = format!("{cookie}; {NEXT_COOKIE}={n}");
    }
    let uri = format!(
        "/auth/callback?code=c0de&state={}",
        crate::gohttp::escape(state.as_bytes(), crate::gohttp::Mode::QueryComponent)
    );
    let rr2 = send(app, req("GET", &uri, Some(&cookie))).await;
    let sess = rr2.cookie(SESSION_COOKIE).filter(|v| !v.is_empty());
    (sess, rr2)
}

// Go: TestLoginFlow.
#[tokio::test]
async fn login_flow() {
    let (app, _t, _j) = login_app(id_claims("ivan@example.com", "example.com")).await;
    let (sess, rr) = drive_login(&app).await;
    let sess =
        sess.unwrap_or_else(|| panic!("no session cookie; callback: {} {}", rr.status, rr.body));
    assert_eq!(rr.status, 200);
    assert!(
        rr.body.contains(r#"location.replace("/pr/7")"#),
        "{}",
        rr.body
    );

    // The session opens /api/me with the verified identity.
    let me = get(&app, "/api/me", Some(&format!("{SESSION_COOKIE}={sess}"))).await;
    assert_eq!(me.status, 200);
    assert!(
        me.body.contains(r#""email":"ivan@example.com""#),
        "{}",
        me.body
    );

    // No cookie: 401.
    assert_eq!(get(&app, "/api/me", None).await.status, 401);

    // A wrong state: 400, no session.
    let bad = get(
        &app,
        "/auth/callback?code=c&state=forged",
        Some(&format!("{STATE_COOKIE}=different")),
    )
    .await;
    assert_eq!(bad.status, 400);
    assert_eq!(bad.body, "state mismatch\n");
}

// Go: TestLoginRejectsForeignWorkspace.
#[tokio::test]
async fn login_rejects_foreign_workspace() {
    let (app, _t, _j) = login_app(id_claims("evil@other.com", "other.com")).await;
    let (sess, rr) = drive_login(&app).await;
    assert!(sess.is_none(), "foreign-workspace account got a session");
    assert_eq!(rr.status, 403, "{}", rr.body);
    assert_eq!(rr.body, "account is not in the example.com workspace\n");
}

// Go: TestLoginRejectsMissingHD.
#[tokio::test]
async fn login_rejects_missing_hd() {
    let mut claims = id_claims("gmail-user@gmail.com", "");
    claims.remove("hd"); // consumer accounts carry no hd claim at all
    let (app, _t, _j) = login_app(claims).await;
    let (sess, rr) = drive_login(&app).await;
    assert!(
        sess.is_none() && rr.status == 403,
        "consumer account should be 403: {}",
        rr.status
    );
}

#[tokio::test]
async fn login_rejects_unverified_email_and_hd_case_folds() {
    let mut claims = id_claims("ivan@example.com", "EXAMPLE.com");
    claims.insert("email_verified".into(), Value::Bool(false));
    let (app, _t, _j) = login_app(claims).await;
    let (sess, rr) = drive_login(&app).await;
    assert!(sess.is_none());
    assert_eq!(
        (rr.status, rr.body.as_str()),
        (401, "identity verification failed\n")
    );

    // The hd comparison ignores case, as Go's EqualFold; the email is lowercased.
    let (app, _t, _j) = login_app(id_claims("Ivan@Example.com", "EXAMPLE.com")).await;
    let (sess, _) = drive_login(&app).await;
    let me = get(
        &app,
        "/api/me",
        Some(&format!("{SESSION_COOKIE}={}", sess.expect("session"))),
    )
    .await;
    assert_eq!(me.body, "{\"email\":\"ivan@example.com\"}\n");
}

#[tokio::test]
async fn login_cookies_and_redirect_match_go() {
    let (app, _t, _j) = login_app(id_claims("ivan@example.com", "example.com")).await;
    let rr = get(&app, "/auth/login?next=/pr/7", None).await;
    let cookies = rr.set_cookies();
    assert!(cookies[0].starts_with(&format!("{STATE_COOKIE}=")));
    assert!(
        cookies[0].ends_with("; Path=/auth; Max-Age=600; HttpOnly; Secure; SameSite=Lax"),
        "{}",
        cookies[0]
    );
    assert_eq!(
        cookies[1],
        format!("{NEXT_COOKIE}=/pr/7; Path=/auth; Max-Age=600; HttpOnly; Secure; SameSite=Lax")
    );
    let loc = rr.header("location");
    assert!(loc.contains("?client_id=test-client.apps.googleusercontent.com&redirect_uri=https%3A%2F%2Fui.example.com%2Fauth%2Fcallback&response_type=code&scope=openid+email+profile&state="), "{loc}");
    assert_eq!(rr.header("content-type"), "text/html; charset=utf-8");
    assert!(rr.body.starts_with("<a href=\""), "{}", rr.body);

    // An off-site next is dropped.
    for bad in ["//evil.com", "/%5Cevil.com", "https://evil.com"] {
        let rr = get(&app, &format!("/auth/login?next={bad}"), None).await;
        assert!(rr.cookie(NEXT_COOKIE).is_none(), "{bad}");
    }

    // A successful callback clears state and next and sets the session.
    let (_, rr) = drive_login(&app).await;
    let c = rr.set_cookies();
    assert_eq!(
        c[0],
        format!("{STATE_COOKIE}=; Path=/auth; Max-Age=0; HttpOnly; Secure; SameSite=Lax")
    );
    assert!(c[1].starts_with(&format!("{SESSION_COOKIE}=")));
    assert!(c[1].ends_with("; Path=/; Max-Age=604800; HttpOnly; Secure; SameSite=Lax"));
    assert_eq!(
        c[2],
        format!("{NEXT_COOKIE}=; Path=/auth; Max-Age=0; HttpOnly; Secure; SameSite=Lax")
    );
}

#[tokio::test]
async fn login_without_oauth_is_501() {
    let (app, _) = crate::testutil::session_app(vec![]);
    let rr = get(&app, "/auth/login", None).await;
    assert_eq!(
        (rr.status, rr.body.as_str()),
        (501, "login is not configured\n")
    );
    let rr = get(&app, "/auth/callback", None).await;
    assert_eq!(rr.status, 501);
}

#[test]
fn safe_local_paths() {
    for ok in ["/", "/x", "/pr/7?a=b"] {
        assert_eq!(safe_local_path(ok), Some(ok));
    }
    for bad in ["", "x", "//evil", "/\\evil", "https://evil"] {
        assert_eq!(safe_local_path(bad), None, "{bad}");
    }
}

/// Byte-for-byte what Go's html/template renders for these inputs.
#[test]
fn signed_in_page_matches_go_template() {
    let page = signed_in_page("/a b,c'<>&+=%20?x=1#f!$()*@~`{}|^[]");
    assert!(
        page.contains(r#"content="1;url=/a b,c&#39;&lt;&gt;&amp;&#43;=%20?x=1#f!$()*@~`{}|^[]">"#),
        "{page}"
    );
    assert!(page.contains(r#"<script>location.replace("/a b,c'\u003c\u003e\u0026+=%20?x=1#f!$()*@~`{}|^[]");</script>"#), "{page}");
    assert_eq!(
        signed_in_page("/pr/7"),
        "<!doctype html>\n<html><head><meta http-equiv=\"refresh\" content=\"1;url=/pr/7\"></head>\n<body style=\"font-family: system-ui; padding: 2em; text-align: center\">\n<p>Signed in \u{2014} continuing\u{2026}</p>\n<script>location.replace(\"/pr/7\");</script>\n</body></html>"
    );
}
