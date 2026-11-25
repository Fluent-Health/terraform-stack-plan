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

/// Go's `whSecret`.
pub(crate) const WH_SECRET: &str = "webhook-secret";

/// Go's `webhookReq`: a signed `/github/webhook` delivery.
pub(crate) async fn webhook_req(
    app: &Arc<App>,
    secret: &str,
    event: &str,
    payload: &serde_json::Value,
) -> Wire {
    let body = payload.to_string();
    let sig =
        tfstackplan_server_clients::github::sign_github_payload(secret.as_bytes(), body.as_bytes());
    send_with(
        app,
        "POST",
        "/github/webhook",
        "",
        &body,
        &[("X-GitHub-Event", event), ("X-Hub-Signature-256", &sig)],
    )
    .await
}

/// Go's `prSyncPayload`.
pub(crate) fn pr_sync_payload(pr: i64, sha: &str) -> serde_json::Value {
    serde_json::json!({
        "action": "synchronize",
        "pull_request": {"number": pr, "head": {"sha": sha, "ref": "feat/x"}},
        "repository": {"full_name": "o/r"},
    })
}

/// Go's `seedTerminalStatus`: "success" or "failure" through the execution aggregate.
pub(crate) async fn seed_terminal_status(app: &App, id: &str, status: &str) {
    let sig: tfstackplan_gate::execution::Signal = match status {
        "success" => tfstackplan_gate::execution::ReportSucceed {}.into(),
        "failure" => tfstackplan_gate::execution::ReportFail {}.into(),
        _ => panic!("seed_terminal_status {id}: unsupported status {status:?}"),
    };
    app.handle_exec(id, sig)
        .await
        .expect("seed terminal status");
}

/// Every check-run update the mock saw, in order.
pub(crate) fn check_updates(
    gh: &MockGitHub,
) -> Vec<tfstackplan_server_clients::github::CheckRunUpdate> {
    gh.calls()
        .into_iter()
        .filter_map(|c| match c {
            tfstackplan_server_clients::github::Call::UpdateCheckRun { update, .. } => Some(update),
            _ => None,
        })
        .collect()
}

/// The names of every check run the mock created, in order.
pub(crate) fn created_check_runs(gh: &MockGitHub) -> Vec<String> {
    gh.calls()
        .into_iter()
        .filter_map(|c| match c {
            tfstackplan_server_clients::github::Call::CreateCheckRun { name, .. } => Some(name),
            _ => None,
        })
        .collect()
}

// --- Helpers for the shell, gate, apply-lock, webhook and loop ports (Go: `post`,
// `newRunTriggerApp`, `recordingGitHub`, `activeClaimedStacks`). ---

/// Go's `post`: marshals `v` and POSTs it unauthenticated, returning the status.
pub(crate) async fn post_json<T: serde::Serialize>(app: &Arc<App>, path: &str, v: &T) -> u16 {
    let body = serde_json::to_string(v).expect("marshal");
    send(app, "POST", path, "", &body).await.status
}

/// The GitHub mock of Go's `newRunTriggerApp`: every check run is id 4242.
pub(crate) fn run_trigger_gh() -> MockGitHub {
    let mut gh = MockGitHub::default();
    gh.create_check_run_fn = Some(Box::new(|_| Ok(4242)));
    gh
}

/// Go's `newRunTriggerApp`: an armed nonprod tier with a webhook secret.
pub(crate) fn run_trigger_app(
    gh: MockGitHub,
    executor: Arc<dyn tfstackplan_server_clients::executor::Backend>,
) -> (TempDir, Arc<App>) {
    let (dir, s) = store();
    let mut a = App::new(
        s,
        Arc::new(gh),
        Config {
            github_webhook_secret: WH_SECRET.into(),
            environment: "nonprod".into(),
            public_base_url: "https://serve.test".into(),
            ..Config::default()
        },
    );
    a.executor = Some(executor);
    (dir, Arc::new(a))
}

/// Go's `recordingGitHub`: every created check run is id 99. Tests add the
/// merge-group and PR-head hooks they need, and read updates with
/// [`check_updates`].
pub(crate) fn recording_github() -> MockGitHub {
    let mut gh = MockGitHub::default();
    gh.create_check_run_fn = Some(Box::new(|_| Ok(99)));
    gh
}

/// Go's `activeClaimedStacks`: unexpired claims as stack → PR.
pub(crate) fn active_claimed_stacks(
    s: &Store,
    env: &str,
    now: jiff::Timestamp,
) -> std::collections::BTreeMap<String, i64> {
    s.list_claims(env)
        .unwrap_or_default()
        .into_iter()
        .filter(|c| c.expires_at > now)
        .map(|c| (c.stack_path, c.owner_pr))
        .collect()
}

/// A streamed response: status, Content-Type and the body's data frames.
pub(crate) struct Streamed {
    pub status: u16,
    pub content_type: String,
    body: axum::body::BodyDataStream,
    /// Everything read so far.
    pub seen: String,
}

impl Streamed {
    /// Reads frames until `seen` contains `sub`; panics after `timeout`.
    pub(crate) async fn wait_for(&mut self, sub: &str, timeout: std::time::Duration) {
        use futures_util::StreamExt as _;
        let deadline = tokio::time::Instant::now() + timeout;
        while !self.seen.contains(sub) {
            match tokio::time::timeout_at(deadline, self.body.next()).await {
                Ok(Some(Ok(b))) => self.seen.push_str(&String::from_utf8_lossy(&b)),
                Ok(_) => panic!("stream ended waiting for {sub:?}; got:\n{}", self.seen),
                Err(_) => panic!("timed out waiting for {sub:?}; got:\n{}", self.seen),
            }
        }
    }

    /// Reads to the end of the stream; panics after `timeout`.
    pub(crate) async fn read_to_end(&mut self, timeout: std::time::Duration) {
        use futures_util::StreamExt as _;
        let deadline = tokio::time::Instant::now() + timeout;
        loop {
            match tokio::time::timeout_at(deadline, self.body.next()).await {
                Ok(Some(Ok(b))) => self.seen.push_str(&String::from_utf8_lossy(&b)),
                Ok(_) => return,
                Err(_) => panic!("stream did not end; got:\n{}", self.seen),
            }
        }
    }
}

/// Sends a GET and returns the body as a stream (for SSE).
pub(crate) async fn open_stream(
    app: &Arc<App>,
    path: &str,
    token: &str,
    headers: &[(&str, &str)],
) -> Streamed {
    let mut b = Request::builder()
        .method("GET")
        .uri(path)
        .header(header::HOST, "serve.test");
    if !token.is_empty() {
        b = b.header(header::AUTHORIZATION, format!("Bearer {token}"));
    }
    for (k, v) in headers {
        b = b.header(*k, *v);
    }
    let resp = app
        .router()
        .oneshot(b.body(Body::empty()).expect("request"))
        .await
        .expect("infallible");
    Streamed {
        status: resp.status().as_u16(),
        content_type: resp
            .headers()
            .get(header::CONTENT_TYPE)
            .and_then(|v| v.to_str().ok())
            .unwrap_or("(none)")
            .to_string(),
        body: resp.into_body().into_data_stream(),
        seen: String::new(),
    }
}

/// Go's `memObjects`: an in-memory object store.
#[derive(Default)]
pub(crate) struct MemObjects {
    pub data: std::sync::Mutex<HashMap<String, Vec<u8>>>,
}

#[async_trait::async_trait]
impl tfstackplan_server_clients::objects::ObjectStore for MemObjects {
    async fn put(&self, key: &str, body: Vec<u8>) -> tfstackplan_server_clients::Result<()> {
        self.data.lock().expect("lock").insert(key.into(), body);
        Ok(())
    }

    async fn get(&self, key: &str) -> tfstackplan_server_clients::Result<Vec<u8>> {
        self.data
            .lock()
            .expect("lock")
            .get(key)
            .cloned()
            .ok_or_else(|| tfstackplan_server_clients::Error::msg("not found"))
    }
}

/// A verifier that maps every token to `email`, except `bad`.
pub(crate) fn verifier_for(email: &'static str) -> Verifier {
    Arc::new(move |bearer: String| {
        Box::pin(async move {
            if bearer == "bad" {
                Err("bad token".to_string())
            } else {
                Ok(email.to_string())
            }
        })
    })
}

/// Go's `newRunTriggerApp` with `PushVerifier` answering `email` (Go's cloud-build
/// push tests), a push service account and the trigger-name map.
pub(crate) fn push_verifier_app(
    email: &'static str,
    push_sa: &str,
    triggers: &[(&str, &str)],
) -> (TempDir, Arc<App>) {
    let (d, s) = store();
    let mut a = App::new(
        s,
        Arc::new(run_trigger_gh()),
        Config {
            github_webhook_secret: WH_SECRET.into(),
            environment: "nonprod".into(),
            public_base_url: "https://serve.test".into(),
            push_service_account: push_sa.into(),
            build_trigger_names: triggers
                .iter()
                .map(|(k, v)| ((*k).to_string(), (*v).to_string()))
                .collect(),
            ..Config::default()
        },
    );
    a.executor = Some(Arc::new(FakeExecutor::default()));
    a.push_verifier = Some(verifier_for(email));
    (d, Arc::new(a))
}

/// Go's `cloudBuildPush`.
pub(crate) async fn cloud_build_push(app: &Arc<App>, build: &serde_json::Value) -> u16 {
    use base64::Engine as _;
    let data = base64::engine::general_purpose::STANDARD.encode(build.to_string());
    let env = serde_json::json!({"message": {"data": data}});
    send(
        app,
        "POST",
        "/pubsub/cloud-builds",
        "faketoken",
        &env.to_string(),
    )
    .await
    .status
}
