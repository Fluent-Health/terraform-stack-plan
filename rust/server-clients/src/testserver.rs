//! A local HTTP server for tests, Go's `httptest.NewServer`: one handler sees every request
//! and answers it. It listens on 127.0.0.1 on a free port and stops when dropped.

use std::sync::Arc;

use axum::body::Body;
use axum::http::{HeaderMap, Request, Response, StatusCode};

/// One request as the handler sees it.
#[derive(Debug, Clone)]
pub struct Req {
    pub method: String,
    pub path: String,
    /// The raw query string, without `?`.
    pub query: String,
    pub headers: HeaderMap,
    pub body: Vec<u8>,
}

impl Req {
    /// A header's value, or "" when absent.
    pub fn header(&self, name: &str) -> &str {
        self.headers
            .get(name)
            .and_then(|v| v.to_str().ok())
            .unwrap_or("")
    }

    /// The body as JSON (`null` when it is not JSON).
    pub fn json(&self) -> serde_json::Value {
        serde_json::from_slice(&self.body).unwrap_or(serde_json::Value::Null)
    }

    /// A query or form parameter, percent-decoded (`+` is a space); "" when absent.
    pub fn param(&self, name: &str, form: bool) -> String {
        let src = if form {
            String::from_utf8_lossy(&self.body).into_owned()
        } else {
            self.query.clone()
        };
        src.split('&')
            .filter_map(|kv| kv.split_once('='))
            .find(|(k, _)| *k == name)
            .map(|(_, v)| unescape(v))
            .unwrap_or_default()
    }
}

/// Undoes Go's `url.QueryEscape` (`+` is a space, `%XX` a byte).
pub fn unescape(s: &str) -> String {
    let b = s.as_bytes();
    let mut out = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        let hex = b
            .get(i + 1..i + 3)
            .and_then(|h| std::str::from_utf8(h).ok())
            .and_then(|h| u8::from_str_radix(h, 16).ok());
        match (b[i], hex) {
            (b'+', _) => out.push(b' '),
            (b'%', Some(v)) => {
                out.push(v);
                i += 2;
            }
            (c, _) => out.push(c),
        }
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

/// The handler's answer.
#[derive(Debug, Clone)]
pub struct Resp {
    pub status: u16,
    pub headers: Vec<(String, String)>,
    pub body: Vec<u8>,
}

impl Resp {
    /// 200 with `body`.
    pub fn ok(body: impl Into<Vec<u8>>) -> Resp {
        Resp::status(200, body)
    }

    /// `status` with `body`.
    pub fn status(status: u16, body: impl Into<Vec<u8>>) -> Resp {
        Resp {
            status,
            headers: Vec::new(),
            body: body.into(),
        }
    }

    /// Adds a response header.
    #[must_use]
    pub fn header(mut self, name: &str, value: &str) -> Resp {
        self.headers.push((name.into(), value.into()));
        self
    }
}

/// A running server; dropping it stops it.
#[derive(Debug)]
pub struct TestServer {
    /// `http://127.0.0.1:<port>`.
    pub url: String,
    handle: tokio::task::JoinHandle<()>,
}

impl Drop for TestServer {
    fn drop(&mut self) {
        self.handle.abort();
    }
}

/// Starts a server answering every request with `handler`. Needs a Tokio runtime.
pub async fn serve<F>(handler: F) -> std::io::Result<TestServer>
where
    F: Fn(Req) -> Resp + Send + Sync + 'static,
{
    let handler = Arc::new(handler);
    let app = axum::Router::new().fallback(move |req: Request<Body>| {
        let handler = Arc::clone(&handler);
        async move {
            let (parts, body) = req.into_parts();
            let body = axum::body::to_bytes(body, 64 << 20)
                .await
                .map(|b| b.to_vec())
                .unwrap_or_default();
            let r = handler(Req {
                method: parts.method.to_string(),
                path: parts.uri.path().to_string(),
                query: parts.uri.query().unwrap_or("").to_string(),
                headers: parts.headers,
                body,
            });
            let mut resp = Response::new(Body::from(r.body));
            *resp.status_mut() =
                StatusCode::from_u16(r.status).unwrap_or(StatusCode::INTERNAL_SERVER_ERROR);
            for (k, v) in r.headers {
                if let (Ok(k), Ok(v)) = (
                    axum::http::HeaderName::try_from(k),
                    axum::http::HeaderValue::try_from(v),
                ) {
                    resp.headers_mut().append(k, v);
                }
            }
            resp
        }
    });
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await?;
    let url = format!("http://{}", listener.local_addr()?);
    let handle = tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });
    Ok(TestServer { url, handle })
}
