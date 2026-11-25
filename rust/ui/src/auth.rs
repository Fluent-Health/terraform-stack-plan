//! The Google login browser flow (Go: `internal/ui/auth.go`), outside the OpenAPI contract:
//! authorization code with `openid email profile`, the Workspace `hd` claim checked on
//! the *verified* id_token (never the unauthenticated hint parameters), and the session in
//! an encrypted cookie. No Google token is stored: the id_token is verified and dropped.

use std::sync::Arc;

use axum::http::{HeaderMap, Method};
use base64::Engine as _;
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use jiff::Timestamp;
use serde_json::Value;
use tracing::warn;

use crate::app::App;
use crate::gohttp::{self, Resp};
use crate::session::{SESSION_COOKIE, SESSION_TTL, Session};

pub(crate) const STATE_COOKIE: &str = "tfsp_ui_state";
pub(crate) const NEXT_COOKIE: &str = "tfsp_ui_next";

/// Ten minutes, the state and next cookies' lifetime.
const FLOW_COOKIE_MAX_AGE: i64 = 600;

impl App {
    /// `GET /auth/login`: CSRF state in a short-lived cookie, an optional `?next=` return
    /// path (local paths only), and the redirect to Google.
    pub(crate) fn handle_login(self: &Arc<Self>, method: &Method, q: &str) -> Resp {
        let Some(oauth) = &self.cfg.oauth else {
            return gohttp::error(501, "login is not configured");
        };
        let mut nonce = [0u8; 16];
        if aws_lc_rs::rand::fill(&mut nonce).is_err() {
            return gohttp::error(500, "internal error");
        }
        let state = URL_SAFE_NO_PAD.encode(nonce);
        let mut r = gohttp::redirect(method, &oauth.auth_code_url(&state, &[]), 302);
        let h = r.headers_mut();
        gohttp::add_cookie(h, STATE_COOKIE, &state, "/auth", FLOW_COOKIE_MAX_AGE);
        let next = gohttp::query_get_str(q, "next");
        if let Some(next) = safe_local_path(&next) {
            gohttp::add_cookie(h, NEXT_COOKIE, next, "/auth", FLOW_COOKIE_MAX_AGE);
        }
        r
    }

    /// `GET /auth/callback`: state check, code exchange, id_token verification
    /// (signature, issuer, audience and expiry by the verifier; email, `email_verified`
    /// and the Workspace `hd` claim here), the session cookie, and the landing page.
    pub(crate) async fn handle_callback(self: &Arc<Self>, h: &HeaderMap, q: &str) -> Resp {
        let (Some(oauth), Some(verify)) = (&self.cfg.oauth, &self.cfg.verify_id_token) else {
            return gohttp::error(501, "login is not configured");
        };
        let state = gohttp::cookie(h, STATE_COOKIE).unwrap_or_default();
        if state.is_empty() || gohttp::query_get(q, "state") != state.as_bytes() {
            return gohttp::error(400, "state mismatch");
        }
        let mut cookies = HeaderMap::new();
        gohttp::add_cookie(&mut cookies, STATE_COOKIE, "", "/auth", -1);
        let with_cookies = |mut r: Resp, c: &HeaderMap| {
            for v in c.get_all(axum::http::header::SET_COOKIE) {
                r.headers_mut()
                    .append(axum::http::header::SET_COOKIE, v.clone());
            }
            r
        };
        let code = gohttp::query_get_str(q, "code");
        if code.is_empty() {
            return with_cookies(gohttp::error(400, "missing code"), &cookies);
        }
        let tok = match oauth.exchange(&self.clients.oauth, &code).await {
            Ok(t) => t,
            Err(e) => {
                warn!("ui: code exchange failed: {e}");
                return with_cookies(gohttp::error(502, "code exchange failed"), &cookies);
            }
        };
        let raw_id = tok
            .raw
            .get("id_token")
            .and_then(Value::as_str)
            .unwrap_or("");
        if raw_id.is_empty() {
            return with_cookies(
                gohttp::error(502, "no id_token in token response"),
                &cookies,
            );
        }
        let claims = match verify.verify(raw_id).await {
            Ok(c) => c,
            Err(e) => {
                warn!("ui: id_token rejected: {e}");
                return with_cookies(gohttp::error(401, "identity verification failed"), &cookies);
            }
        };
        let email = claims.get("email").and_then(Value::as_str).unwrap_or("");
        if email.is_empty() || claims.get("email_verified") == Some(&Value::Bool(false)) {
            return with_cookies(gohttp::error(401, "identity verification failed"), &cookies);
        }
        let hd = claims.get("hd").and_then(Value::as_str).unwrap_or("");
        if !equal_fold(hd, &self.cfg.allowed_domain) {
            return with_cookies(
                gohttp::error(
                    403,
                    &format!(
                        "account is not in the {} workspace",
                        self.cfg.allowed_domain
                    ),
                ),
                &cookies,
            );
        }
        let sealed = match self.codec.seal(&Session {
            email: email.to_lowercase(),
            expires: Timestamp::now() + SESSION_TTL,
        }) {
            Ok(s) => s,
            Err(_) => return with_cookies(gohttp::error(500, "internal error"), &cookies),
        };
        gohttp::add_cookie(
            &mut cookies,
            SESSION_COOKIE,
            &sealed,
            "/",
            SESSION_TTL.as_secs(),
        );
        let next = gohttp::cookie(h, NEXT_COOKIE)
            .and_then(|c| safe_local_path(&c).map(str::to_string))
            .unwrap_or_else(|| "/".into());
        gohttp::add_cookie(&mut cookies, NEXT_COOKIE, "", "/auth", -1);
        // A 200 page that then navigates, not a 302: browsers increasingly drop cookies set
        // on the return-from-provider redirect hop, while a cookie set by the document the
        // user lands on is first-party.
        with_cookies(gohttp::html(signed_in_page(&next)), &cookies)
    }
}

/// `POST /auth/logout`: drops the session cookie. POST only; SameSite=Lax keeps
/// cross-site POSTs cookie-less.
pub(crate) fn handle_logout() -> Resp {
    let mut r = gohttp::status(204, axum::body::Body::empty());
    gohttp::add_cookie(r.headers_mut(), SESSION_COOKIE, "", "/", -1);
    r
}

/// Admits only same-origin absolute paths (`/x`, not `//evil`, `/\evil` or full URLs),
/// so `?next=` cannot redirect off-site.
pub(crate) fn safe_local_path(p: &str) -> Option<&str> {
    let b = p.as_bytes();
    (b.first() == Some(&b'/') && (b.len() == 1 || (b[1] != b'/' && b[1] != b'\\'))).then_some(p)
}

/// Go's `strings.EqualFold`, conservatively: equal after per-character case mapping. It
/// never accepts a pair Go would reject.
fn equal_fold(a: &str, b: &str) -> bool {
    a.chars().count() == b.chars().count()
        && a.chars().zip(b.chars()).all(|(x, y)| {
            x == y || x.to_lowercase().eq(y.to_lowercase()) || x.to_uppercase().eq(y.to_uppercase())
        })
}

/// The post-login landing page, as Go's `signedInTmpl` renders it: `next` HTML-escaped in
/// the meta refresh and a JSON string in the script.
fn signed_in_page(next: &str) -> String {
    format!(
        "<!doctype html>\n<html><head><meta http-equiv=\"refresh\" content=\"1;url={}\"></head>\n<body style=\"font-family: system-ui; padding: 2em; text-align: center\">\n<p>Signed in \u{2014} continuing\u{2026}</p>\n<script>location.replace({});</script>\n</body></html>",
        gohttp::html_escape(next),
        gohttp::go_json(next)
    )
}

#[cfg(test)]
mod tests;
