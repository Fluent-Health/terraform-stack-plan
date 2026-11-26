//! The ui service's wiring and routing (Go: `internal/ui/app.go` and the generated
//! `internal/uiapi` router). Routing reproduces Go's `ServeMux` for this route set: path
//! cleaning with 301 redirects, the trailing-slash redirect, per-segment unescaping, the
//! 405 with `Allow`, and the SPA catch-all for every other GET. On the contract routes the
//! parameters bind before the session check, as in the generated wrappers.

use std::sync::Arc;
use std::time::Duration;

use axum::body::Body;
use axum::http::{Method, Request};
use tfstackplan_server_clients::TokenFunc;
use tfstackplan_server_clients::gauth::ClaimsVerifier;
use tracing::warn;

use crate::gohttp::{self, Mode, Resp};
use crate::oauth::OAuth;
use crate::session::{Codec, SESSION_COOKIE, Session};

/// One tier serve the ui aggregates. `token` mints the Google OIDC ID tokens the tier's
/// `api_auth` accepts; `None` calls it unauthenticated (local runs against an
/// auth-disabled serve).
#[derive(Clone)]
pub struct Tier {
    pub name: String,
    pub url: String,
    pub token: Option<TokenFunc>,
}

impl std::fmt::Debug for Tier {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Tier")
            .field("name", &self.name)
            .field("url", &self.url)
            .field("token", &self.token.is_some())
            .finish()
    }
}

/// The wiring the `ui {}` block and its env-var secrets resolve to (Go's `ui.Config`).
#[derive(Debug, Default)]
pub struct Config {
    /// The external base; the OAuth redirect URI is `<base>/auth/callback`.
    pub public_base_url: String,
    pub session_secret: String,
    /// Verifies GitHub App deliveries at the relay before fan-out when set; empty forwards
    /// blindly (every serve verifies the HMAC itself either way).
    pub github_webhook_secret: String,
    /// The required id_token `hd` claim, lowercase.
    pub allowed_domain: String,
    /// The Google authorization-code client; `None` disables login.
    pub oauth: Option<OAuth>,
    /// Sent as `x-goog-user-project` on the user-token PAM call; empty sends none.
    pub quota_project: String,
    /// The PAM endpoint; empty is Google's.
    pub pam_base_url: String,
    /// Verifies the code exchange's id_token (signature, issuer, audience, expiry).
    pub verify_id_token: Option<ClaimsVerifier>,
    pub tiers: Vec<Tier>,
}

/// The HTTP clients, each with connect and read timeouts.
#[derive(Debug, Clone)]
pub(crate) struct Clients {
    /// Contract reads and the gate-reconcile nudge: 15 s in total, as Go's tier client.
    pub api: reqwest::Client,
    /// SSE, logs and plan fragments: no total timeout (streams are long-lived); a read
    /// that stalls past the tier's 25 s heartbeat several times over gives up.
    pub stream: reqwest::Client,
    /// The OAuth code exchange.
    pub oauth: reqwest::Client,
}

/// How long a streamed read may stall.
const STREAM_READ_TIMEOUT: Duration = Duration::from_secs(120);

/// The ui HTTP service (Go's `App`).
#[derive(Debug)]
pub struct App {
    pub(crate) cfg: Config,
    pub(crate) codec: Codec,
    pub(crate) clients: Clients,
}

impl App {
    /// Builds the app. The session secret is required: everything but health sits behind
    /// the login.
    pub fn new(cfg: Config) -> Result<Arc<App>, String> {
        let codec = Codec::new(&cfg.session_secret)?;
        let mk =
            |t: Duration| tfstackplan_server_clients::http::client(t).map_err(|e| e.to_string());
        tfstackplan_server_clients::http::install_crypto_provider();
        let stream = reqwest::Client::builder()
            .connect_timeout(tfstackplan_server_clients::http::CONNECT_TIMEOUT)
            .read_timeout(STREAM_READ_TIMEOUT)
            .build()
            .map_err(|e| format!("http client: {e}"))?;
        Ok(Arc::new(App {
            cfg,
            codec,
            clients: Clients {
                api: mk(Duration::from_secs(15))?,
                stream,
                oauth: mk(Duration::from_secs(30))?,
            },
        }))
    }

    /// The configured tier named `name`.
    pub(crate) fn tier(&self, name: &[u8]) -> Option<&Tier> {
        self.cfg.tiers.iter().find(|t| t.name.as_bytes() == name)
    }

    /// Seals a session cookie value (tests and the replay fixtures bypass the login).
    pub fn seal_session(&self, s: &Session) -> Result<String, String> {
        self.codec.seal(s)
    }

    /// Serves one request.
    pub async fn handle(self: &Arc<Self>, req: Request<Body>) -> Resp {
        let raw_path = req.uri().path().to_string();
        let raw_query = req.uri().query().unwrap_or("").to_string();
        // Go rejects a request target whose path has a bad %-escape before routing.
        if gohttp::unescape(raw_path.as_bytes(), Mode::Path).is_err() {
            let mut r = gohttp::status(400, "400 Bad Request");
            gohttp::set_header(r.headers_mut(), "content-type", "text/plain; charset=utf-8");
            gohttp::set_header(r.headers_mut(), "connection", "close");
            return r;
        }
        let method = req.method().clone();
        let path = gohttp::clean_path(&raw_path);
        let found = route(&method, &path);
        if !found.as_ref().is_some_and(|m| m.exact) && !path.ends_with('/') {
            let slashed = format!("{path}/");
            if route(&method, &slashed).is_some_and(|m| m.exact) {
                let unescaped = gohttp::lenient_path_unescape(raw_path.as_bytes());
                let cleaned = gohttp::clean_path(&String::from_utf8_lossy(&unescaped));
                let mut loc = gohttp::escape(format!("{cleaned}/").as_bytes(), Mode::Path);
                if !raw_query.is_empty() {
                    loc = format!("{loc}?{raw_query}");
                }
                return gohttp::redirect(&method, &loc, 301);
            }
        }
        if path != raw_path {
            // Go re-escapes the already escaped path here, so a %XX turns into %25XX.
            let mut loc = gohttp::escape(path.as_bytes(), Mode::Path);
            if !raw_query.is_empty() {
                loc = format!("{loc}?{raw_query}");
            }
            return gohttp::redirect(&method, &loc, 301);
        }
        let Some(m) = found else {
            let mut allow = vec!["GET", "HEAD"];
            if allowed_post(&path) || (!path.ends_with('/') && allowed_post(&format!("{path}/"))) {
                allow.push("POST");
            }
            let mut r = gohttp::error(405, "Method Not Allowed");
            gohttp::set_header(r.headers_mut(), "allow", &allow.join(", "));
            return r;
        };
        self.dispatch(m.route, &m.caps, req, &raw_query).await
    }

    async fn dispatch(
        self: &Arc<Self>,
        route: Route,
        caps: &[Vec<u8>],
        req: Request<Body>,
        q: &str,
    ) -> Resp {
        let cap = |i: usize| caps.get(i).cloned().unwrap_or_default();
        match route {
            Route::Healthz => healthz(),
            Route::Ready => gohttp::status(200, Body::empty()),
            Route::Login => self.handle_login(req.method(), q),
            Route::Callback => self.handle_callback(req.headers(), q).await,
            Route::Logout => crate::auth::handle_logout(),
            Route::ApproveStart => match self.session(req.headers()) {
                Some(s) => self.handle_approve_start(&s, q, req.method(), req.headers()),
                None => unauthorized(),
            },
            Route::ApproveCallback => match self.session(req.headers()) {
                Some(s) => self.handle_approve_callback(&s, q).await,
                None => unauthorized(),
            },
            Route::GitHubWebhook => self.handle_github_relay(req).await,
            Route::Spa => crate::assets::serve(req.uri().path(), q, req.headers()),
            Route::Events | Route::Logs | Route::Plan => {
                if self.session(req.headers()).is_none() {
                    return unauthorized();
                }
                let tier = cap(0);
                let exec = cap(1);
                if !safe_segment(&exec) {
                    return bad_segment();
                }
                let exec = gohttp::escape(&exec, Mode::PathSegment);
                let rest = match route {
                    Route::Events => format!("/api/execution/{exec}/events"),
                    Route::Logs | Route::Plan => {
                        // An empty remainder (`.../logs/e1/`) is passed on as Go does;
                        // the tier answers it. Otherwise no segment may be empty.
                        let stack = cap(2);
                        let segs: Vec<&[u8]> = if stack.is_empty() {
                            Vec::new()
                        } else {
                            stack.split(|b| *b == b'/').collect()
                        };
                        if !segs.iter().all(|s| safe_segment(s)) {
                            return bad_segment();
                        }
                        let stack: Vec<String> = segs
                            .iter()
                            .map(|s| gohttp::escape(s, Mode::PathSegment))
                            .collect();
                        format!(
                            "/{}/{exec}/{}",
                            if route == Route::Logs { "logs" } else { "plan" },
                            stack.join("/")
                        )
                    }
                    _ => String::new(),
                };
                self.proxy_stream(req.headers(), q, &tier, &rest).await
            }
            api => self.api(api, caps, req.headers(), q).await,
        }
    }

    /// The generated wrappers: bind the path and query parameters (400 on a bad one), then
    /// the session check, then the handler.
    async fn api(
        self: &Arc<Self>,
        route: Route,
        caps: &[Vec<u8>],
        headers: &axum::http::HeaderMap,
        q: &str,
    ) -> Resp {
        // Errors are the router's 400 texts.
        let bind_path = |i: usize, name: &str| -> Result<Vec<u8>, String> {
            let raw = caps.get(i).cloned().unwrap_or_default();
            if raw.is_empty() {
                return Err(param_error(
                    name,
                    &format!("parameter '{name}' is empty, can't bind its value"),
                ));
            }
            gohttp::unescape(&raw, Mode::PathSegment).map_err(|e| {
                param_error(
                    name,
                    &format!("error unescaping path parameter '{name}': {e}"),
                )
            })
        };
        let bind_query = |name: &str, required: bool| -> Result<Option<i64>, String> {
            let values = gohttp::query_values(q, name);
            match values.as_slice() {
                [] if required => Err(format!("Query argument {name} is required, but not found")),
                [] => Ok(None),
                [v] => gohttp::parse_int(v)
                    .map(Some)
                    .map_err(|e| param_error(name, &e)),
                _ => Err(param_error(
                    name,
                    &format!("multiple values for single value parameter '{name}'"),
                )),
            }
        };
        let bound = (|| -> Result<(Vec<u8>, String), String> {
            Ok(match route {
                Route::Me | Route::Tiers => (Vec::new(), String::new()),
                Route::TierExecutions => {
                    let tier = bind_path(0, "tier")?;
                    let pr = bind_query("pr", false)?;
                    let limit = bind_query("limit", false)?;
                    let mut qs = Vec::new();
                    if let Some(pr) = pr {
                        qs.push(format!("pr={pr}"));
                    }
                    if let Some(limit) = limit {
                        qs.push(format!("limit={limit}"));
                    }
                    let mut p = "api/executions".to_string();
                    if !qs.is_empty() {
                        p = format!("{p}?{}", qs.join("&"));
                    }
                    (tier, p)
                }
                Route::TierExecution => {
                    let tier = bind_path(0, "tier")?;
                    let id = bind_path(1, "id")?;
                    if !safe_segment(&id) {
                        return Err(param_error("id", "not a single path segment"));
                    }
                    (
                        tier,
                        format!("api/execution/{}", gohttp::escape(&id, Mode::PathSegment)),
                    )
                }
                Route::TierApprovals => (bind_path(0, "tier")?, "api/approvals".into()),
                Route::TierPool => (bind_path(0, "tier")?, "api/inspect/pool".into()),
                Route::TierMergeQueue => (bind_path(0, "tier")?, "api/merge-queue".into()),
                Route::TierLifecycle => {
                    let tier = bind_path(0, "tier")?;
                    let pr = bind_query("pr", true)?.unwrap_or_default();
                    (tier, format!("api/lifecycle?pr={pr}"))
                }
                Route::TierPr => {
                    let tier = bind_path(0, "tier")?;
                    let n = bind_path(1, "n")?;
                    let n = gohttp::parse_int(&n).map_err(|e| param_error("n", &e))?;
                    (tier, format!("api/pr/{n}"))
                }
                _ => (Vec::new(), String::new()),
            })
        })();
        let (tier, upstream) = match bound {
            Ok(b) => b,
            Err(msg) => return gohttp::error(400, &msg),
        };
        let Some(sess) = self.session(headers) else {
            return unauthorized();
        };
        match route {
            Route::Me => gohttp::json(&Me { email: &sess.email }),
            Route::Tiers => {
                let out: Vec<TierInfo<'_>> = self
                    .cfg
                    .tiers
                    .iter()
                    .map(|t| TierInfo {
                        name: &t.name,
                        url: &t.url,
                    })
                    .collect();
                gohttp::json(&out)
            }
            _ => self.relay(&tier, &upstream).await,
        }
    }

    /// Go's `sessionAuth`: the session of a valid cookie; `None` means 401.
    pub(crate) fn session(&self, h: &axum::http::HeaderMap) -> Option<Session> {
        let c = gohttp::cookie(h, SESSION_COOKIE)?;
        self.codec
            .open(&c)
            .inspect_err(|e| warn!("ui: session rejected: {e}"))
            .ok()
    }
}

/// Whether a decoded path parameter is safe to put in one segment of a tier URL. `.` and
/// `..` (which `%2e%2e` decodes to, and which URL parsing resolves even when escaped)
/// would climb out of the proxied path to any tier route under the ui's own identity.
pub(crate) fn safe_segment(s: &[u8]) -> bool {
    !s.is_empty() && s != b"." && s != b".." && !s.iter().any(|b| matches!(b, b'?' | b'#' | b'\\'))
}

/// The answer to a stream path with an unsafe segment.
fn bad_segment() -> Resp {
    gohttp::error(400, "invalid path segment")
}

/// The generated router's 400 text for a parameter that does not bind.
fn param_error(name: &str, err: &str) -> String {
    format!("Invalid format for parameter {name}: {err}")
}

/// `sessionAuth`'s rejection.
fn unauthorized() -> Resp {
    gohttp::error(401, "unauthorized")
}

#[derive(serde::Serialize)]
struct Me<'a> {
    email: &'a str,
}

#[derive(serde::Serialize)]
struct TierInfo<'a> {
    name: &'a str,
    url: &'a str,
}

/// `/healthz`: the version and git SHA (Go's ui answers an empty 200 here; `/ready` keeps
/// that shape).
fn healthz() -> Resp {
    gohttp::json(&serde_json::json!({
        "status": "ok",
        "version": env!("CARGO_PKG_VERSION"),
        "git_sha": option_env!("TFSTACKPLAN_GIT_SHA").unwrap_or("unknown"),
    }))
}

/// Every route of Go's mux.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Route {
    Healthz,
    Ready,
    Login,
    Callback,
    Logout,
    ApproveStart,
    ApproveCallback,
    GitHubWebhook,
    Me,
    Tiers,
    TierApprovals,
    TierExecutions,
    TierExecution,
    TierPool,
    TierLifecycle,
    TierMergeQueue,
    TierPr,
    Events,
    Logs,
    Plan,
    Spa,
}

/// The patterns, Go's spelling. `GET` also serves `HEAD`.
const PATTERNS: &[(&str, &str, Route)] = &[
    ("GET", "/healthz", Route::Healthz),
    ("GET", "/ready", Route::Ready),
    ("GET", "/auth/login", Route::Login),
    ("GET", "/auth/callback", Route::Callback),
    ("POST", "/auth/logout", Route::Logout),
    ("GET", "/auth/approve", Route::ApproveStart),
    ("GET", "/auth/approve/callback", Route::ApproveCallback),
    ("POST", "/github/webhook", Route::GitHubWebhook),
    ("GET", "/api/me", Route::Me),
    ("GET", "/api/tiers", Route::Tiers),
    ("GET", "/api/tiers/{tier}/approvals", Route::TierApprovals),
    ("GET", "/api/tiers/{tier}/executions", Route::TierExecutions),
    (
        "GET",
        "/api/tiers/{tier}/executions/{id}",
        Route::TierExecution,
    ),
    ("GET", "/api/tiers/{tier}/inspect/pool", Route::TierPool),
    ("GET", "/api/tiers/{tier}/lifecycle", Route::TierLifecycle),
    (
        "GET",
        "/api/tiers/{tier}/merge-queue",
        Route::TierMergeQueue,
    ),
    ("GET", "/api/tiers/{tier}/pr/{n}", Route::TierPr),
    (
        "GET",
        "/api/tiers/{tier}/executions/{id}/events",
        Route::Events,
    ),
    (
        "GET",
        "/api/tiers/{tier}/logs/{exec}/{stack...}",
        Route::Logs,
    ),
    (
        "GET",
        "/api/tiers/{tier}/plan/{exec}/{stack...}",
        Route::Plan,
    ),
    ("GET", "/", Route::Spa),
];

pub(crate) struct Matched {
    pub route: Route,
    pub caps: Vec<Vec<u8>>,
    /// Go's `exactMatch`: no multi wildcard matched a non-empty remainder.
    pub exact: bool,
}

/// net/http's `firstSegment`.
fn first_segment(path: &str) -> (Vec<u8>, &str) {
    if path == "/" {
        return (b"/".to_vec(), "");
    }
    let p = &path[1..];
    let i = p.find('/').unwrap_or(p.len());
    (gohttp::lenient_path_unescape(&p.as_bytes()[..i]), &p[i..])
}

/// Matches one pattern the way Go's routing tree walks it.
fn match_pattern(pattern: &str, path: &str) -> Option<(Vec<Vec<u8>>, bool)> {
    let segs: Vec<&str> = if pattern == "/" {
        vec!["{...}"]
    } else {
        pattern[1..].split('/').collect()
    };
    let mut rest = path;
    let mut caps = Vec::new();
    for seg in &segs {
        if seg.ends_with("...}") {
            if rest.is_empty() {
                return None;
            }
            if *seg != "{...}" {
                caps.push(gohttp::lenient_path_unescape(&rest.as_bytes()[1..]));
            }
            let exact = path.ends_with('/') && segs.len() == path.matches('/').count();
            return Some((caps, exact));
        }
        if rest.is_empty() {
            return None;
        }
        let (s, r) = first_segment(rest);
        if seg.starts_with('{') {
            if s == b"/" {
                return None;
            }
            caps.push(s);
        } else if s != seg.as_bytes() {
            return None;
        }
        rest = r;
    }
    rest.is_empty().then_some((caps, true))
}

/// The route for `method` and `path`: the one non-root pattern that matches, else the
/// SPA catch-all for GET and HEAD.
pub(crate) fn route(method: &Method, path: &str) -> Option<Matched> {
    let want = if method == Method::HEAD {
        "GET"
    } else {
        method.as_str()
    };
    PATTERNS
        .iter()
        .filter(|(m, _, _)| *m == want)
        .find_map(|(_, p, r)| {
            match_pattern(p, path).map(|(caps, exact)| Matched {
                route: *r,
                caps,
                exact,
            })
        })
}

/// Whether a POST pattern matches `path`.
fn allowed_post(path: &str) -> bool {
    route(&Method::POST, path).is_some()
}

#[cfg(test)]
mod tests;
