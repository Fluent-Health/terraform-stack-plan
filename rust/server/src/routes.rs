//! The router (Go: `App.Routes`, the generated `api.HandlerWithOptions`, the auth
//! middleware). The `/api` operations follow `api/openapi.yaml`: path, method and
//! accepted scopes per operation ([`API_ROUTES`], checked against the spec by a
//! test). Unmatched requests answer like Go's `http.ServeMux`.

use std::collections::HashMap;
use std::sync::Arc;

use axum::Router;
use axum::body::{Body, Bytes};
use axum::extract::{DefaultBodyLimit, Request, State};
use axum::http::{HeaderMap, HeaderValue, Method, StatusCode, Uri, header};
use axum::response::Response;
use axum::routing::{MethodRouter, get, post};
use tracing::info;

use crate::config::{SCOPE_ADMIN, SCOPE_READ, SCOPE_REPORT};
use crate::{App, admin, execution, http, ingest, lifecycle, logs, pubsub, read, webhook};

/// Per-request context the handlers need from the request.
#[derive(Debug, Clone, Default)]
pub(crate) struct Ctx {
    /// The public origin for Details links.
    pub base_url: String,
    /// The verified caller, "" with auth disabled.
    pub actor: String,
}

const ANY: &[&str] = &[SCOPE_REPORT, SCOPE_READ, SCOPE_ADMIN];
const REPORT: &[&str] = &[SCOPE_REPORT];
const REPORT_ADMIN: &[&str] = &[SCOPE_REPORT, SCOPE_ADMIN];
const ADMIN: &[&str] = &[SCOPE_ADMIN];

/// Every `/api` operation of the contract: (method, path, accepted scopes).
pub const API_ROUTES: &[(&str, &str, &[&str])] = &[
    ("POST", "/api/init", REPORT),
    ("POST", "/api/phase", REPORT),
    ("POST", "/api/update", REPORT),
    ("POST", "/api/finalize", REPORT),
    ("POST", "/api/logs", REPORT),
    ("POST", "/api/gate/check", REPORT),
    ("POST", "/api/gate/reconcile", ANY),
    ("POST", "/api/gate/revoke", REPORT),
    ("POST", "/api/claims/list", ANY),
    ("POST", "/api/claims/release", REPORT_ADMIN),
    ("GET", "/api/catalog", ANY),
    ("GET", "/api/executions", ANY),
    ("GET", "/api/approvals", ANY),
    ("GET", "/api/lifecycle", ANY),
    ("GET", "/api/pr/{n}", ANY),
    ("GET", "/api/merge-queue", ANY),
    ("GET", "/api/execution/{id}", ANY),
    ("GET", "/api/inspect/grants", ANY),
    ("GET", "/api/inspect/gate/{pr}/{env}", ANY),
    ("GET", "/api/inspect/claims/{env}", ANY),
    ("GET", "/api/inspect/events/{stream}", ANY),
    ("GET", "/api/inspect/overview", ANY),
    ("GET", "/api/inspect/pool", ANY),
    ("POST", "/api/admin/grants/release", ADMIN),
    ("POST", "/api/admin/executions/cancel", ADMIN),
    ("POST", "/api/admin/gates/satisfy", ADMIN),
    ("POST", "/api/admin/checks/override", ADMIN),
];

fn scopes_of(path: &str) -> &'static [&'static str] {
    API_ROUTES
        .iter()
        .find(|(_, p, _)| *p == path)
        .map_or(ANY, |(_, _, s)| *s)
}

impl App {
    /// The public origin: the configured one, else derived from the request.
    fn base_url(&self, headers: &HeaderMap) -> String {
        if !self.cfg.public_base_url.is_empty() {
            return self.cfg.public_base_url.trim_end_matches('/').to_string();
        }
        let scheme = headers
            .get("X-Forwarded-Proto")
            .and_then(|v| v.to_str().ok())
            .filter(|s| !s.is_empty())
            .unwrap_or("http");
        let host = headers
            .get(header::HOST)
            .and_then(|v| v.to_str().ok())
            .unwrap_or_default();
        format!("{scheme}://{host}")
    }

    /// Bearer auth for an `/api` route: a verified OIDC token whose email holds one
    /// of `scopes`. Disabled without a verifier. `Ok` carries the actor.
    fn auth(
        &self,
        req: &Request,
        scopes: &'static [&'static str],
    ) -> impl Future<Output = Result<String, Box<Response>>> + Send + '_ {
        // Only owned, Sync parts of the request may live across the verifier's await.
        let (headers, method, path) = (
            req.headers().clone(),
            req.method().clone(),
            req.uri().path().to_string(),
        );
        async move { self.auth_parts(&headers, &method, &path, scopes).await }
    }

    async fn auth_parts(
        &self,
        headers: &HeaderMap,
        method: &Method,
        path: &str,
        scopes: &[&str],
    ) -> Result<String, Box<Response>> {
        let Some(verify) = &self.api_verifier else {
            return Ok(String::new());
        };
        let Some(token) = headers
            .get(header::AUTHORIZATION)
            .and_then(|v| v.to_str().ok())
            .and_then(|h| h.strip_prefix("Bearer "))
        else {
            return Err(Box::new(http::error(StatusCode::UNAUTHORIZED, "unauthorized")));
        };
        let Ok(email) = verify(token.to_string()).await else {
            return Err(Box::new(http::error(StatusCode::UNAUTHORIZED, "unauthorized")));
        };
        let email = email.to_lowercase();
        let granted = self
            .cfg
            .api_principals
            .get(&email)
            .map(Vec::as_slice)
            .unwrap_or_default();
        if !granted.iter().any(|g| scopes.contains(&g.as_str())) {
            info!("server: api auth: {email} lacks scope {scopes:?} for {method} {path}");
            return Err(Box::new(http::error(StatusCode::FORBIDDEN, "forbidden")));
        }
        Ok(email)
    }

    /// The router: every route of Go's `Routes`.
    pub fn router(self: &Arc<Self>) -> Router {
        let mut r = Router::new()
            .route(
                "/ready",
                get(|| async { http::ok() }).fallback(method_not_allowed(&["GET", "HEAD"])),
            )
            .route(
                "/healthz",
                get(healthz).fallback(method_not_allowed(&["GET", "HEAD"])),
            )
            .route("/logs/{exec}/{*stack}", gated_get(serve_logs))
            .route("/plan/{exec}/{*stack}", gated_get(serve_plan))
            .route("/pubsub/push", post_only(pubsub_push))
            .route("/pubsub/cloud-builds", post_only(pubsub_cloud_builds))
            .route("/github/webhook", post_only(github_webhook))
            .route("/api/execution/{id}/events", gated_get(execution_events));
        for (method, path, _) in API_ROUTES {
            let mr: MethodRouter<Arc<App>> = if *method == "GET" {
                get(api).fallback(method_not_allowed(&["GET", "HEAD"]))
            } else {
                post(api).fallback(method_not_allowed(&["POST"]))
            };
            r = r.route(path, mr);
        }
        r.fallback(|| async { http::not_found_page() })
            .layer(DefaultBodyLimit::disable())
            .with_state(Arc::clone(self))
    }
}

/// Go's ServeMux 405: the allowed methods and a plain-text body.
fn method_not_allowed(allow: &'static [&'static str]) -> MethodRouter<Arc<App>> {
    axum::routing::any(move || async move {
        let mut r = http::error(StatusCode::METHOD_NOT_ALLOWED, "Method Not Allowed");
        if let Ok(v) = HeaderValue::from_str(&allow.join(", ")) {
            r.headers_mut().insert(header::ALLOW, v);
        }
        r
    })
}

fn post_only<H, T>(h: H) -> MethodRouter<Arc<App>>
where
    H: axum::handler::Handler<T, Arc<App>>,
    T: 'static,
{
    post(h).fallback(method_not_allowed(&["POST"]))
}

fn gated_get<H, T>(h: H) -> MethodRouter<Arc<App>>
where
    H: axum::handler::Handler<T, Arc<App>>,
    T: 'static,
{
    get(h).fallback(method_not_allowed(&["GET", "HEAD"]))
}

async fn healthz() -> Response {
    http::json_ok(&serde_json::json!({
        "status": "ok",
        "version": env!("CARGO_PKG_VERSION"),
        "git_sha": option_env!("TFSTACKPLAN_GIT_SHA").unwrap_or("unknown"),
    }))
}

/// The decoded path parameters of a request.
fn path_params(matched: &str, path: &str) -> HashMap<String, String> {
    let mut out = HashMap::new();
    for (pat, seg) in matched.split('/').zip(path.split('/')) {
        if let Some(name) = pat.strip_prefix('{').and_then(|p| p.strip_suffix('}')) {
            out.insert(name.to_string(), percent_decode(seg));
        }
    }
    out
}

/// Go's `url.PathUnescape`, lenient: an invalid escape is kept as is.
fn percent_decode(s: &str) -> String {
    let b = s.as_bytes();
    let mut out = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        if b[i] == b'%'
            && let (Some(h), Some(l)) = (
                b.get(i + 1).and_then(|c| hex(*c)),
                b.get(i + 2).and_then(|c| hex(*c)),
            )
        {
            out.push(h * 16 + l);
            i += 3;
            continue;
        }
        out.push(b[i]);
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

fn hex(c: u8) -> Option<u8> {
    char::from(c)
        .to_digit(16)
        .and_then(|d| u8::try_from(d).ok())
}

/// The query values of a request (Go's `r.URL.Query()`).
fn query(uri: &Uri) -> HashMap<String, Vec<String>> {
    let mut out: HashMap<String, Vec<String>> = HashMap::new();
    let pairs: Vec<(String, String)> =
        serde_urlencoded::from_str(uri.query().unwrap_or_default()).unwrap_or_default();
    for (k, v) in pairs {
        out.entry(k).or_default().push(v);
    }
    out
}

/// oapi-codegen's integer binding error text.
fn parse_int(name: &str, raw: &str) -> Result<i64, Box<Response>> {
    raw.parse::<i64>().map_err(|e| {
        let why = match e.kind() {
            std::num::IntErrorKind::PosOverflow | std::num::IntErrorKind::NegOverflow => "value out of range",
            _ => "invalid syntax",
        };
        Box::new(http::bad_request(&format!(
            "Invalid format for parameter {name}: error binding string parameter: strconv.ParseInt: parsing \"{raw}\": {why}"
        )))
    })
}

/// An optional integer query parameter.
fn query_int(q: &HashMap<String, Vec<String>>, name: &str) -> Result<Option<i64>, Box<Response>> {
    match q.get(name).and_then(|v| v.first()) {
        None => Ok(None),
        Some(raw) => parse_int(name, raw).map(Some),
    }
}

fn query_str(q: &HashMap<String, Vec<String>>, name: &str) -> Option<String> {
    q.get(name).and_then(|v| v.first()).cloned()
}

/// The single entry point for the generated `/api` operations: bind parameters
/// (before auth, as the generated wrapper does), authorize, dispatch.
async fn api(State(app): State<Arc<App>>, req: Request) -> Response {
    let matched = req
        .extensions()
        .get::<axum::extract::MatchedPath>()
        .map(|m| m.as_str().to_string())
        .unwrap_or_default();
    let params = path_params(&matched, req.uri().path());
    let q = query(req.uri());
    let p = |n: &str| params.get(n).cloned().unwrap_or_default();

    // Parameter binding, in the generated wrappers' order.
    enum Bound {
        None,
        Pr(i64),
        PrEnv(i64, String),
        Str(String),
        Exec(Option<i64>, Option<i64>),
        Grants(Option<String>, Option<i64>),
        After(String, Option<i64>),
    }
    let bound = match matched.as_str() {
        "/api/pr/{n}" => match parse_int("n", &p("n")) {
            Ok(n) => Bound::Pr(n),
            Err(r) => return *r,
        },
        "/api/inspect/gate/{pr}/{env}" => match parse_int("pr", &p("pr")) {
            Ok(n) => Bound::PrEnv(n, p("env")),
            Err(r) => return *r,
        },
        "/api/inspect/claims/{env}" => Bound::Str(p("env")),
        "/api/execution/{id}" => Bound::Str(p("id")),
        "/api/inspect/events/{stream}" => match query_int(&q, "after") {
            Ok(a) => Bound::After(p("stream"), a),
            Err(r) => return *r,
        },
        "/api/executions" => match (query_int(&q, "pr"), query_int(&q, "limit")) {
            (Err(r), _) | (_, Err(r)) => return *r,
            (Ok(pr), Ok(limit)) => Bound::Exec(pr, limit),
        },
        "/api/inspect/grants" => match query_int(&q, "live") {
            Ok(live) => Bound::Grants(query_str(&q, "state"), live),
            Err(r) => return *r,
        },
        "/api/lifecycle" => match q.get("pr").and_then(|v| v.first()) {
            None => return http::bad_request("Query argument pr is required, but not found"),
            Some(raw) => match parse_int("pr", raw) {
                Ok(n) => Bound::Pr(n),
                Err(r) => return *r,
            },
        },
        _ => Bound::None,
    };

    let actor = match app.auth(&req, scopes_of(&matched)).await {
        Ok(a) => a,
        Err(r) => return *r,
    };
    let ctx = Ctx {
        base_url: app.base_url(req.headers()),
        actor,
    };
    let body: Bytes = match axum::body::to_bytes(req.into_body(), usize::MAX).await {
        Ok(b) => b,
        Err(_) => Bytes::new(),
    };
    let body = &body[..];
    match (matched.as_str(), bound) {
        ("/api/init", _) => ingest::init(app, ctx, body).await,
        ("/api/phase", _) => ingest::phase(app, ctx, body).await,
        ("/api/update", _) => ingest::update(app, ctx, body).await,
        ("/api/finalize", _) => ingest::finalize(app, ctx, body).await,
        ("/api/logs", _) => logs::append(app, body).await,
        ("/api/gate/check", _) => ingest::gate_check(app, body).await,
        ("/api/gate/reconcile", _) => ingest::gate_reconcile(app),
        ("/api/gate/revoke", _) => ingest::gate_revoke(app, body).await,
        ("/api/claims/list", _) => ingest::claims_list(app, body).await,
        ("/api/claims/release", _) => ingest::claims_release(app, body).await,
        ("/api/catalog", _) => crate::serve::catalog(app).await,
        ("/api/executions", Bound::Exec(pr, limit)) => read::list_executions(app, pr, limit).await,
        ("/api/approvals", _) => read::list_approvals(app).await,
        ("/api/lifecycle", Bound::Pr(pr)) => lifecycle::get(app, pr).await,
        ("/api/pr/{n}", Bound::Pr(n)) => read::get_pr(app, n).await,
        ("/api/merge-queue", _) => read::merge_queue(app).await,
        ("/api/execution/{id}", Bound::Str(id)) => execution::get(app, id).await,
        ("/api/inspect/grants", Bound::Grants(state, live)) => {
            read::inspect_grants(app, state, live).await
        }
        ("/api/inspect/gate/{pr}/{env}", Bound::PrEnv(pr, env)) => {
            read::inspect_gate(app, pr, env).await
        }
        ("/api/inspect/claims/{env}", Bound::Str(env)) => read::inspect_claims(app, env).await,
        ("/api/inspect/events/{stream}", Bound::After(stream, after)) => {
            read::inspect_events(app, stream, after).await
        }
        ("/api/inspect/overview", _) => read::inspect_overview(app).await,
        ("/api/inspect/pool", _) => read::inspect_pool(app).await,
        ("/api/admin/grants/release", _) => admin::grants_release(app, ctx.actor, body).await,
        ("/api/admin/executions/cancel", _) => admin::executions_cancel(app, ctx.actor, body).await,
        ("/api/admin/gates/satisfy", _) => admin::gates_satisfy(app, ctx.actor, body).await,
        ("/api/admin/checks/override", _) => admin::checks_override(app, ctx.actor, body).await,
        _ => http::not_found_page(),
    }
}

/// The `{stack...}` remainder and `{exec}` of a log or plan path, as Go's
/// `PathValue` decodes them.
fn exec_and_stack(uri: &Uri, prefix: &str) -> (String, String) {
    let rest = uri.path().strip_prefix(prefix).unwrap_or_default();
    let (exec, stack) = rest.split_once('/').unwrap_or((rest, ""));
    (percent_decode(exec), percent_decode(stack))
}

async fn serve_logs(State(app): State<Arc<App>>, req: Request) -> Response {
    if let Err(r) = app.auth(&req, ANY).await {
        return *r;
    }
    let (exec, stack) = exec_and_stack(req.uri(), "/logs/");
    let follow = query(req.uri())
        .get("follow")
        .and_then(|v| v.first())
        .is_some_and(|v| !v.is_empty());
    let headers = req.headers().clone();
    let resp = logs::serve(app, exec, stack, follow, &headers).await;
    head_strip(req.method(), resp)
}

async fn serve_plan(State(app): State<Arc<App>>, req: Request) -> Response {
    if let Err(r) = app.auth(&req, ANY).await {
        return *r;
    }
    let (exec, stack) = exec_and_stack(req.uri(), "/plan/");
    head_strip(req.method(), logs::plan(app, exec, stack).await)
}

async fn execution_events(State(app): State<Arc<App>>, req: Request) -> Response {
    if let Err(r) = app.auth(&req, ANY).await {
        return *r;
    }
    let path = req.uri().path();
    let id = path
        .strip_prefix("/api/execution/")
        .and_then(|p| p.strip_suffix("/events"))
        .map(percent_decode)
        .unwrap_or_default();
    execution::events(app, id).await
}

/// A HEAD answer carries the GET headers without the body.
fn head_strip(method: &Method, mut r: Response) -> Response {
    if method == Method::HEAD {
        *r.body_mut() = Body::empty();
    }
    r
}

async fn pubsub_push(State(app): State<Arc<App>>, headers: HeaderMap, body: Bytes) -> Response {
    pubsub::push(app, &headers, &body).await
}

async fn pubsub_cloud_builds(
    State(app): State<Arc<App>>,
    headers: HeaderMap,
    body: Bytes,
) -> Response {
    pubsub::cloud_builds(app, &headers, &body).await
}

async fn github_webhook(State(app): State<Arc<App>>, headers: HeaderMap, body: Bytes) -> Response {
    webhook::handle(app, &headers, &body).await
}

#[cfg(test)]
mod tests;
