//! Shared test helpers: requests straight into [`App::handle`], session cookies sealed
//! without the login flow, and an address nothing listens on.

use std::sync::Arc;

use axum::body::Body;
use axum::http::{HeaderMap, Request};
use jiff::{SignedDuration, Timestamp};

use crate::app::{App, Config, Tier};
use crate::session::{SESSION_COOKIE, Session};

pub const TEST_CLIENT_ID: &str = "test-client.apps.googleusercontent.com";

/// A response, read whole.
#[derive(Debug)]
pub struct Got {
    pub status: u16,
    pub headers: HeaderMap,
    pub body: String,
}

impl Got {
    pub fn header(&self, name: &str) -> &str {
        self.headers
            .get(name)
            .and_then(|v| v.to_str().ok())
            .unwrap_or("")
    }

    /// The `Set-Cookie` lines.
    pub fn set_cookies(&self) -> Vec<String> {
        self.headers
            .get_all("set-cookie")
            .iter()
            .filter_map(|v| v.to_str().ok().map(str::to_string))
            .collect()
    }

    /// The value a `Set-Cookie` gives `name`, if any.
    pub fn cookie(&self, name: &str) -> Option<String> {
        self.set_cookies().into_iter().find_map(|c| {
            let first = c.split(';').next()?.to_string();
            let (n, v) = first.split_once('=')?;
            (n == name).then(|| v.to_string())
        })
    }
}

/// Sends `req` to the app and reads the whole response.
pub async fn send(app: &Arc<App>, req: Request<Body>) -> Got {
    let r = app.handle(req).await;
    let status = r.status().as_u16();
    let headers = r.headers().clone();
    let body = axum::body::to_bytes(r.into_body(), usize::MAX)
        .await
        .map(|b| String::from_utf8_lossy(&b).into_owned())
        .unwrap_or_default();
    Got {
        status,
        headers,
        body,
    }
}

/// A request with an optional `Cookie` header.
pub fn req(method: &str, uri: &str, cookie: Option<&str>) -> Request<Body> {
    let mut b = Request::builder().method(method).uri(uri);
    if let Some(c) = cookie {
        b = b.header("cookie", c);
    }
    b.body(Body::empty()).unwrap_or_default()
}

/// `GET uri` with an optional cookie.
pub async fn get(app: &Arc<App>, uri: &str, cookie: Option<&str>) -> Got {
    send(app, req("GET", uri, cookie)).await
}

/// A `Cookie` header value carrying a valid session for `email`.
pub fn session_for(app: &App, email: &str) -> String {
    let sealed = app
        .seal_session(&Session {
            email: email.into(),
            expires: Timestamp::now() + SignedDuration::from_hours(1),
        })
        .unwrap_or_default();
    format!("{SESSION_COOKIE}={sealed}")
}

/// Go's `newSessionApp`: an app over `tiers` plus a valid session cookie.
pub fn session_app(tiers: Vec<Tier>) -> (Arc<App>, String) {
    let app = App::new(Config {
        session_secret: "s3cret".into(),
        tiers,
        ..Config::default()
    })
    .unwrap_or_else(|e| panic!("app: {e}"));
    let c = session_for(&app, "ivan@example.com");
    (app, c)
}

/// A tier with no token source.
pub fn tier(name: &str, url: &str) -> Tier {
    Tier {
        name: name.into(),
        url: url.into(),
        token: None,
    }
}

/// A tier whose token source always yields `token`.
pub fn tier_with_token(name: &str, url: &str, token: &'static str) -> Tier {
    let f: tfstackplan_server_clients::TokenFunc =
        Arc::new(
            move || async move { Ok::<_, tfstackplan_server_clients::Error>(token.to_string()) },
        );
    Tier {
        name: name.into(),
        url: url.into(),
        token: Some(f),
    }
}

/// `http://127.0.0.1:<port>` with nothing listening (Go's closed httptest server).
pub async fn dead_url() -> String {
    let l = tokio::net::TcpListener::bind("127.0.0.1:0")
        .await
        .unwrap_or_else(|e| panic!("bind: {e}"));
    let addr = l.local_addr().unwrap_or_else(|e| panic!("addr: {e}"));
    drop(l);
    format!("http://{addr}")
}

/// The value of `key` in a URL's query, decoded.
pub fn url_query(url: &str, key: &str) -> String {
    let q = url.split_once('?').map_or("", |(_, q)| q);
    crate::gohttp::query_get_str(q, key)
}
