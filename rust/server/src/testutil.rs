//! Shared test helpers (Go: `newServerTestDB`, `fakeOIDC`, `seedInit`,
//! `seedProjectionTarget`, and the httptest round trip).

use std::collections::HashMap;
use std::sync::Arc;

use axum::body::Body;
use axum::http::{Request, header};
use tempfile::TempDir;
use tfstackplan_gate::events::{Init, PhaseEvent};
use tfstackplan_gate::execution::{ReportInit, ReportPhase};
use tfstackplan_server_clients::github::{GitHub, MockGitHub};
use tfstackplan_store::Store;
use tower::ServiceExt;

use crate::shell::exec_init_from_events;
use crate::{App, Config, Verifier};

/// A fresh migrated store in a temp dir.
pub(crate) fn store() -> (TempDir, Arc<Store>) {
    let dir = tempfile::Builder::new()
        .prefix("tfsp-server-")
        .tempdir()
        .expect("temp dir");
    let s = Store::open(dir.path().join("server.db")).expect("open store");
    (dir, Arc::new(s))
}

/// An app over a fresh store.
pub(crate) fn app_with(gh: Arc<dyn GitHub>, cfg: Config) -> (TempDir, Arc<App>) {
    let (dir, s) = store();
    (dir, Arc::new(App::new(s, gh, cfg)))
}

/// An app with a no-op GitHub and default config.
pub(crate) fn app() -> (TempDir, Arc<App>) {
    app_with(Arc::new(MockGitHub::default()), Config::default())
}

/// A verifier that knows a fixed token → email table.
pub(crate) fn fake_oidc(tokens: &[(&str, &str)]) -> Verifier {
    let tokens: HashMap<String, String> = tokens
        .iter()
        .map(|(t, e)| ((*t).into(), (*e).into()))
        .collect();
    Arc::new(move |bearer: String| {
        let got = tokens.get(&bearer).cloned();
        Box::pin(async move { got.ok_or_else(|| "bad token".to_string()) })
    })
}

pub(crate) async fn seed_init(app: &App, init: Init) {
    app.handle_exec(
        &init.id,
        ReportInit {
            exec: exec_init_from_events(&init),
        }
        .into(),
    )
    .await
    .expect("seed init");
}

pub(crate) async fn seed_phase(app: &App, p: PhaseEvent) {
    let sig = ReportPhase {
        id: p.id.clone(),
        phase: p.phase,
        label: p.label,
        pct: p.progress_pct,
        repo: p.repo,
        sha: p.sha,
        pr: p.pr,
        environment: p.environment,
        context: p.context,
        log_url: p.log_url,
    };
    app.handle_exec(&p.id, sig.into())
        .await
        .expect("seed phase");
}

#[expect(
    clippy::too_many_arguments,
    reason = "Go's seedProjectionTarget signature"
)]
pub(crate) fn seed_projection_target(
    s: &Store,
    pr: i64,
    env: &str,
    class: &str,
    target: &str,
    grant: &str,
    state: &str,
    requester: &str,
) {
    s.exec(|c| {
        c.execute(
            "INSERT INTO gate_targets (pr, environment, class, target, grant_name, state, requester)
             VALUES (?1,?2,?3,?4,?5,?6,?7)
             ON CONFLICT(pr, environment, class, target) DO UPDATE SET
               grant_name=excluded.grant_name, state=excluded.state,
               requester=excluded.requester, updated_at=CURRENT_TIMESTAMP",
            rusqlite::params![pr, env, class, target, grant, state, requester],
        )
    })
    .expect("seed target");
}

/// A response as the wire goldens record it.
pub(crate) struct Wire {
    pub status: u16,
    pub content_type: String,
    pub body: Vec<u8>,
    pub headers: axum::http::HeaderMap,
}

impl Wire {
    pub(crate) fn text(&self) -> String {
        String::from_utf8_lossy(&self.body).into_owned()
    }
}

/// Sends one request through the router.
pub(crate) async fn send(
    app: &Arc<App>,
    method: &str,
    path: &str,
    token: &str,
    body: &str,
) -> Wire {
    send_with(app, method, path, token, body, &[]).await
}

pub(crate) async fn send_with(
    app: &Arc<App>,
    method: &str,
    path: &str,
    token: &str,
    body: &str,
    headers: &[(&str, &str)],
) -> Wire {
    let mut b = Request::builder()
        .method(method)
        .uri(path)
        .header(header::HOST, "serve.test");
    if !body.is_empty() {
        b = b.header(header::CONTENT_TYPE, "application/json");
    }
    if !token.is_empty() {
        b = b.header(header::AUTHORIZATION, format!("Bearer {token}"));
    }
    for (k, v) in headers {
        b = b.header(*k, *v);
    }
    let req = b.body(Body::from(body.to_string())).expect("request");
    let resp = app.router().oneshot(req).await.expect("infallible");
    let status = resp.status().as_u16();
    let headers = resp.headers().clone();
    let content_type = headers
        .get(header::CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("(none)")
        .to_string();
    let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
        .await
        .expect("body")
        .to_vec();
    Wire {
        status,
        content_type,
        body,
        headers,
    }
}

/// An executor that records starts and returns scripted results.
#[derive(Default)]
pub(crate) struct FakeExecutor {
    pub starts: std::sync::Mutex<Vec<tfstackplan_server_clients::executor::RunRequest>>,
    pub cancels: std::sync::Mutex<Vec<String>>,
    pub start_err: Option<String>,
    pub probe: Option<tfstackplan_server_clients::executor::Phase>,
}

#[async_trait::async_trait]
impl tfstackplan_server_clients::executor::Backend for FakeExecutor {
    async fn start(
        &self,
        req: &tfstackplan_server_clients::executor::RunRequest,
    ) -> tfstackplan_server_clients::Result<tfstackplan_server_clients::executor::Ref> {
        self.starts.lock().expect("lock").push(req.clone());
        match &self.start_err {
            Some(e) => Err(tfstackplan_server_clients::Error::msg(e.clone())),
            None => Ok(tfstackplan_server_clients::executor::Ref {
                id: format!("build-{}", req.execution_id),
            }),
        }
    }

    async fn cancel(
        &self,
        r: &tfstackplan_server_clients::executor::Ref,
    ) -> tfstackplan_server_clients::Result<()> {
        self.cancels.lock().expect("lock").push(r.id.clone());
        Ok(())
    }

    async fn probe(
        &self,
        _r: &tfstackplan_server_clients::executor::Ref,
    ) -> tfstackplan_server_clients::Result<tfstackplan_server_clients::executor::Phase> {
        Ok(self
            .probe
            .unwrap_or(tfstackplan_server_clients::executor::Phase::Working))
    }
}
