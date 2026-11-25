//! HTTP replay of `serve` against the Go binary.
//!
//! Starts Go's `serve` and the Rust `serve` from fresh databases with the same
//! config, both pointed at one fake GitHub (`TFSTACKPLAN_GITHUB_API_URL`), sends
//! every case to both, and compares the status, the headers that matter, the body,
//! the GitHub calls the case caused, and a dump of every table afterwards.
//! Timestamps are masked; nothing else is. The report goes to
//! `rust/cases/serve-replay-report.md`.
//!
//! The Rust server runs as a child of this test binary (it re-executes itself with
//! `TFSP_REPLAY_CHILD=1`), so the environment overrides reach it without mutating
//! this process's environment. Skipped (with a note) when the Go binary is absent.
//!
//! No real credentials or cloud resources: the GitHub App key is generated here,
//! auth is off (Go's OIDC verifier cannot be pointed at a fake issuer), and there is
//! no approval backend, executor or object store.

#![expect(
    clippy::unwrap_used,
    clippy::expect_used,
    reason = "a test harness without libtest: a panic is how a case fails"
)]

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::process::{Child, Command, ExitCode, Stdio};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use axum::Router;
use axum::body::Bytes;
use axum::extract::State;
use axum::http::{Method, StatusCode, Uri};
use axum::response::IntoResponse;
use serde_json::{Value, json};

type Calls = Arc<Mutex<Vec<String>>>;

fn main() -> ExitCode {
    if std::env::var_os("TFSP_REPLAY_CHILD").is_some() {
        let args: Vec<String> = std::env::args().skip(1).collect();
        let rt = tokio::runtime::Runtime::new().expect("runtime");
        return rt.block_on(tfstackplan_server::serve::run(args));
    }
    let rt = tokio::runtime::Runtime::new().expect("runtime");
    rt.block_on(replay())
}

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../..")
}

/// The fake GitHub: mints installation tokens, numbers check runs, answers PR and
/// merge-queue reads, and records every other call as `METHOD path body`.
async fn fake_github(calls: Calls) -> (String, tokio::task::JoinHandle<()>) {
    async fn handle(
        State((calls, seq)): State<(Calls, Arc<Mutex<i64>>)>,
        method: Method,
        uri: Uri,
        body: Bytes,
    ) -> axum::response::Response {
        let path = uri.path().to_string();
        if path.ends_with("/access_tokens") {
            return (
                StatusCode::CREATED,
                axum::Json(json!({"token": "ghs_fake", "expires_at": "2099-01-01T00:00:00Z"})),
            )
                .into_response();
        }
        let body_v: Value = serde_json::from_slice(&body).unwrap_or(Value::Null);
        calls
            .lock()
            .unwrap()
            .push(format!("{method} {path} {body_v}"));
        if method == Method::POST && path.ends_with("/check-runs") {
            let mut s = seq.lock().unwrap();
            *s += 1;
            return (StatusCode::CREATED, axum::Json(json!({"id": 1000 + *s}))).into_response();
        }
        if method == Method::GET && path.contains("/pulls/") {
            return axum::Json(
                json!({"head": {"sha": "headsha"}, "state": "open", "merged": false}),
            )
            .into_response();
        }
        if path == "/graphql" {
            return axum::Json(json!({"data": {"repository": {"mergeQueue": null, "defaultBranchRef": {"name": "main"}}}})).into_response();
        }
        if path.ends_with("/pulls") {
            return axum::Json(json!([])).into_response();
        }
        axum::Json(json!({})).into_response()
    }
    let app = Router::new()
        .fallback(handle)
        .with_state((calls, Arc::new(Mutex::new(0i64))));
    let l = tokio::net::TcpListener::bind("127.0.0.1:0")
        .await
        .expect("bind");
    let url = format!("http://{}", l.local_addr().expect("addr"));
    (
        url,
        tokio::spawn(async move {
            let _ = axum::serve(l, app).await;
        }),
    )
}

fn free_port() -> u16 {
    std::net::TcpListener::bind("127.0.0.1:0")
        .and_then(|l| l.local_addr())
        .map(|a| a.port())
        .expect("free port")
}

struct Side {
    name: &'static str,
    child: Child,
    base: String,
    db: PathBuf,
    calls: Calls,
    _gh: tokio::task::JoinHandle<()>,
    _dir: tempfile::TempDir,
}

impl Drop for Side {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

async fn start(name: &'static str, go_bin: Option<&Path>, key_pem: &str) -> Side {
    let dir = tempfile::Builder::new()
        .prefix("tfsp-replay-")
        .tempdir()
        .expect("dir");
    let calls: Calls = Arc::default();
    let (gh_url, gh) = fake_github(Arc::clone(&calls)).await;
    let key = dir.path().join("app.pem");
    std::fs::write(&key, key_pem).expect("key");
    let db = dir.path().join("serve.db");
    let cfg = dir.path().join("cfg.hcl");
    std::fs::write(
        &cfg,
        format!(
            "server {{\n  environment = \"staging\"\n}}\n\nserve {{\n  db_path = \"{}\"\n  public_base_url = \"https://serve.test\"\n  ui_base_url = \"https://ui.test\"\n  github_webhook_secret_env = \"TFSP_REPLAY_WEBHOOK_SECRET\"\n  github_app {{\n    app_id = \"1\"\n    installation_id = \"2\"\n    private_key_path = \"{}\"\n  }}\n}}\n",
            db.display(),
            key.display()
        ),
    )
    .expect("cfg");
    let port = free_port();
    let args = [
        "serve",
        "-config",
        &cfg.to_string_lossy(),
        "-addr",
        &format!("127.0.0.1:{port}"),
    ];
    let mut cmd = match go_bin {
        Some(go) => {
            let mut c = Command::new(go);
            c.args(args);
            c
        }
        None => {
            let mut c = Command::new(std::env::current_exe().expect("exe"));
            c.args(&args[1..]).env("TFSP_REPLAY_CHILD", "1");
            c
        }
    };
    let child = cmd
        .current_dir(dir.path())
        .env("TFSTACKPLAN_GITHUB_API_URL", &gh_url)
        .env("TFSP_REPLAY_WEBHOOK_SECRET", "replay-secret")
        .env("PATH", "/usr/bin:/bin")
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .expect("spawn serve");
    let base = format!("http://127.0.0.1:{port}");
    let hc = client();
    for _ in 0..200 {
        if hc
            .get(format!("{base}/ready"))
            .send()
            .await
            .is_ok_and(|r| r.status() == 200)
        {
            break;
        }
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    Side {
        name,
        child,
        base,
        db,
        calls,
        _gh: gh,
        _dir: dir,
    }
}

fn client() -> reqwest::Client {
    tfstackplan_server_clients::http::install_crypto_provider();
    tfstackplan_server_clients::http::client(Duration::from_secs(10)).expect("client")
}

/// Masks timestamps (RFC 3339, SQLite and Go's String() forms) and the elapsed
/// seconds the pool reports.
fn mask(s: &str) -> String {
    let re =
        regex::Regex::new(r"\d{4}-\d\d-\d\d[T ]\d\d:\d\d:\d\d(\.\d+)?( ?[+-]\d\d:?\d\d( UTC)?|Z)?")
            .expect("regex");
    let s = re.replace_all(s, "<T>").into_owned();
    regex::Regex::new(r#""elapsed_seconds":\d+"#)
        .expect("regex")
        .replace_all(&s, r#""elapsed_seconds":<N>"#)
        .into_owned()
}

/// Every table's rows, timestamps masked: the event log and phase history in rowid
/// order, other tables sorted.
fn dump(db: &Path) -> String {
    let c = rusqlite::Connection::open_with_flags(db, rusqlite::OpenFlags::SQLITE_OPEN_READ_ONLY)
        .expect("open db");
    let mut out = String::new();
    let tables: Vec<String> = c
        .prepare("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
        .expect("tables")
        .query_map([], |r| r.get(0))
        .expect("query")
        .filter_map(Result::ok)
        .collect();
    for t in tables {
        out.push_str(&format!("== {t}\n"));
        let mut stmt = c
            .prepare(&format!("SELECT * FROM \"{t}\" ORDER BY rowid"))
            .expect("select");
        let n = stmt.column_count();
        let rows = stmt
            .query_map([], |r| {
                let mut cells = Vec::new();
                for i in 0..n {
                    cells.push(match r.get_ref(i)? {
                        rusqlite::types::ValueRef::Null => "NULL".to_string(),
                        rusqlite::types::ValueRef::Integer(v) => v.to_string(),
                        rusqlite::types::ValueRef::Real(v) => v.to_string(),
                        rusqlite::types::ValueRef::Text(v) | rusqlite::types::ValueRef::Blob(v) => {
                            String::from_utf8_lossy(v).into_owned()
                        }
                    });
                }
                Ok(cells.join(" | "))
            })
            .expect("rows");
        let mut rows: Vec<String> = rows.filter_map(Result::ok).map(|r| mask(&r)).collect();
        // Go writes some tables by ranging over a map (claims, stack outputs), so their
        // insertion order is random; only the event log and phase history are ordered.
        if !matches!(t.as_str(), "events" | "execution_phases") {
            rows.sort();
        }
        for row in rows {
            out.push_str(&row);
            out.push('\n');
        }
    }
    out
}

struct Case {
    name: &'static str,
    method: &'static str,
    path: &'static str,
    body: String,
    headers: Vec<(&'static str, String)>,
    /// Bodies whose JSON arrays come out of Go map iteration compare as sorted lines.
    unordered: bool,
}

fn case(name: &'static str, method: &'static str, path: &'static str, body: &str) -> Case {
    Case {
        name,
        method,
        path,
        body: body.to_string(),
        headers: Vec::new(),
        unordered: false,
    }
}

fn signed(name: &'static str, event: &'static str, body: Value) -> Case {
    let body = body.to_string();
    let key = aws_lc_rs::hmac::Key::new(aws_lc_rs::hmac::HMAC_SHA256, b"replay-secret");
    let sig = hex::encode(aws_lc_rs::hmac::sign(&key, body.as_bytes()).as_ref());
    Case {
        name,
        method: "POST",
        path: "/github/webhook",
        body,
        headers: vec![
            ("X-GitHub-Event", event.into()),
            ("X-Hub-Signature-256", format!("sha256={sig}")),
        ],
        unordered: false,
    }
}

fn cases() -> Vec<Case> {
    let init = |id: &str, pr: i64, sha: &str, ctx: &str| {
        json!({"id": id, "repo": "o/r", "sha": sha, "pr": pr, "environment": "staging", "log_url": "https://ci/logs/1", "context": ctx,
               "stacks": [{"path": "stacks/a", "project": "proj-a", "status": "pending"}, {"path": "stacks/b", "project": "proj-b"}],
               "edges": [{"from": "stacks/a", "to": "stacks/b"}]})
        .to_string()
    };
    let mut v = vec![
        case("ready", "GET", "/ready", ""),
        case("not-found", "GET", "/nope", ""),
        case("method-not-allowed", "GET", "/api/init", ""),
        case("init", "POST", "/api/init", &init("e1", 7, "cafe", "")),
        case("init-empty-body", "POST", "/api/init", ""),
        case("init-syntax-error", "POST", "/api/init", "{nope"),
        case("init-truncated", "POST", "/api/init", r#"{"id":"x""#),
        case("init-type-mismatch", "POST", "/api/init", r#"{"id":"x","pr":"7"}"#),
        case("init-trailing-garbage", "POST", "/api/init", &format!("{} trailing", init("e9", 9, "f00d", ""))),
        case("phase", "POST", "/api/phase", r#"{"id":"e1","phase":"planning","label":"planning 0/2","progress_pct":10}"#),
        case("update", "POST", "/api/update", r#"{"id":"e1","stack":"stacks/a","status":"planned"}"#),
        case("update-unknown-status", "POST", "/api/update", r#"{"id":"e1","stack":"stacks/a","status":"bogus"}"#),
        case("logs", "POST", "/api/logs", "{\"id\":\"e1\",\"stack\":\"stacks/a\",\"data\":\"plan says <hi> & \\u001b[1mbold\\u001b[0m\\n\"}"),
        case("log-read", "GET", "/logs/e1/stacks/a", ""),
        case("log-read-missing", "GET", "/logs/e1/stacks/zzz", ""),
        case(
            "finalize",
            "POST",
            "/api/finalize",
            &json!({"id": "e1", "report_markdown": "## report\n", "projects": {"stacks/a": "proj-a"},
                    "stack_reports": {"stacks/a": "### stacks/a\n\n```diff\n+ add\n- del\n```\n"},
                    "categories": {"stacks/a": [{"name": "iam", "icon": "🔐"}]},
                    "counts": {"stacks/a": {"add": 1, "change": 2}},
                    "change_reasons": [{"stack": "stacks/a", "kind": "direct", "via": null}]})
            .to_string(),
        ),
        case("plan-fragment", "GET", "/plan/e1/stacks/a", ""),
        case("plan-fragment-missing", "GET", "/plan/e1/stacks/zzz", ""),
        case("execution", "GET", "/api/execution/e1", ""),
        case("execution-404", "GET", "/api/execution/nope", ""),
        case("gate-check-clean", "POST", "/api/gate/check", r#"{"pr":7,"environment":"staging"}"#),
        case("gate-check-not-classified", "POST", "/api/gate/check", r#"{"pr":99,"environment":"staging"}"#),
        case("init-supersede", "POST", "/api/init", &init("e2", 7, "cafe", "plan/staging")),
        case("execution-superseded", "GET", "/api/execution/e1", ""),
        case("init-apply", "POST", "/api/init", &init("a1", 7, "m1", "apply/staging")),
        case("phase-apply", "POST", "/api/phase", r#"{"id":"a1","phase":"applying"}"#),
        case("update-apply-failed", "POST", "/api/update", r#"{"id":"a1","stack":"stacks/a","status":"failed","detail":"Error: googleapi: Error 403: permission denied"}"#),
        case("finalize-apply-failed", "POST", "/api/finalize", r#"{"id":"a1","report_markdown":"","failed":true}"#),
        case("execution-apply", "GET", "/api/execution/a1", ""),
        case("gate-revoke", "POST", "/api/gate/revoke", r#"{"pr":7,"environment":"staging"}"#),
        case("claims-list", "POST", "/api/claims/list", r#"{"environment":"staging"}"#),
        case("claims-release", "POST", "/api/claims/release", r#"{"environment":"staging","pr":7}"#),
        case("claims-list-bad", "POST", "/api/claims/list", "[]"),
        case("executions", "GET", "/api/executions", ""),
        case("executions-pr", "GET", "/api/executions?pr=7&limit=2", ""),
        case("executions-bad-limit", "GET", "/api/executions?limit=0", ""),
        case("executions-bad-int", "GET", "/api/executions?limit=x", ""),
        case("approvals", "GET", "/api/approvals", ""),
        case("pr", "GET", "/api/pr/7", ""),
        case("pr-404", "GET", "/api/pr/4242", ""),
        case("pr-bad", "GET", "/api/pr/abc", ""),
        case("lifecycle", "GET", "/api/lifecycle?pr=7", ""),
        case("lifecycle-missing-pr", "GET", "/api/lifecycle", ""),
        case("merge-queue", "GET", "/api/merge-queue", ""),
        case("catalog", "GET", "/api/catalog", ""),
        case("inspect-grants", "GET", "/api/inspect/grants?state=open", ""),
        case("inspect-gate", "GET", "/api/inspect/gate/7/staging", ""),
        case("inspect-gate-bad", "GET", "/api/inspect/gate/x/staging", ""),
        case("inspect-claims", "GET", "/api/inspect/claims/staging", ""),
        case("inspect-events", "GET", "/api/inspect/events/run:e1", ""),
        case("inspect-events-after", "GET", "/api/inspect/events/run:e1?after=2", ""),
        case("inspect-overview", "GET", "/api/inspect/overview", ""),
        case("inspect-pool", "GET", "/api/inspect/pool", ""),
        case("admin-satisfy", "POST", "/api/admin/gates/satisfy", r#"{"pr":7,"environment":"staging","class":"iam","target":"proj-a","reason":"test"}"#),
        case("admin-satisfy-bad", "POST", "/api/admin/gates/satisfy", "{"),
        case("admin-override", "POST", "/api/admin/checks/override", r#"{"pr":7,"environment":"staging","check":"terraform/staging","conclusion":"success","reason":"r"}"#),
        case("admin-cancel", "POST", "/api/admin/executions/cancel", r#"{"id":"e2","reason":"r"}"#),
        case("admin-cancel-unknown", "POST", "/api/admin/executions/cancel", r#"{"id":"nope"}"#),
        case("admin-release", "POST", "/api/admin/grants/release", r#"{"pr":7,"environment":"staging","class":"iam","target":"proj-a"}"#),
        case("pubsub-disabled", "POST", "/pubsub/push", "{}"),
        case("cloud-builds-disabled", "POST", "/pubsub/cloud-builds", "{}"),
        case("webhook-unsigned", "POST", "/github/webhook", "{}"),
        signed(
            "webhook-pr-opened",
            "pull_request",
            json!({"action": "opened", "pull_request": {"number": 7, "title": "Add <widget>", "body": null, "html_url": "https://github.com/o/r/pull/7",
                   "head": {"sha": "cafe", "ref": "feature/w"}, "user": {"login": "octocat"}, "auto_merge": {}}, "repository": {"full_name": "o/r"}}),
        ),
        signed("webhook-ping", "ping", json!({"zen": "hi"})),
        signed("webhook-merge-group", "merge_group", json!({"action": "checks_requested", "merge_group": {"head_sha": "mg1", "head_ref": "gh-readonly-queue/main/pr-7-abc"}, "repository": {"full_name": "o/r"}})),
        signed("webhook-pr-closed", "pull_request", json!({"action": "closed", "pull_request": {"number": 7, "merged": false}, "repository": {"full_name": "o/r"}})),
        signed("webhook-bad-payload", "pull_request", json!([1, 2])),
        case("pr-after-webhook", "GET", "/api/pr/7", ""),
    ];
    for c in &mut v {
        if c.name == "inspect-claims" || c.name == "inspect-overview" {
            c.unordered = true;
        }
    }
    v
}

struct Got {
    status: u16,
    headers: String,
    body: String,
}

async fn send(hc: &reqwest::Client, side: &Side, c: &Case) -> Got {
    let mut rb = hc.request(
        c.method.parse().expect("method"),
        format!("{}{}", side.base, c.path),
    );
    if !c.body.is_empty() {
        rb = rb
            .header("Content-Type", "application/json")
            .body(c.body.clone());
    }
    for (k, v) in &c.headers {
        rb = rb.header(*k, v);
    }
    let resp = rb.send().await.expect("send");
    let status = resp.status().as_u16();
    let mut headers = String::new();
    for h in [
        "content-type",
        "x-content-type-options",
        "allow",
        "cache-control",
    ] {
        if let Some(v) = resp.headers().get(h).and_then(|v| v.to_str().ok()) {
            headers.push_str(&format!("{h}: {v}\n"));
        }
    }
    let body = resp.text().await.expect("body");
    Got {
        status,
        headers,
        body,
    }
}

fn normalise(body: &str, unordered: bool) -> String {
    let m = mask(body);
    if !unordered {
        return m;
    }
    // Go map iteration: compare the JSON with every array sorted.
    fn sort(v: &mut Value) {
        match v {
            Value::Array(a) => {
                a.iter_mut().for_each(sort);
                a.sort_by_key(ToString::to_string);
            }
            Value::Object(o) => o.values_mut().for_each(sort),
            _ => {}
        }
    }
    match serde_json::from_str::<Value>(&m) {
        Ok(mut v) => {
            sort(&mut v);
            v.to_string()
        }
        Err(_) => m,
    }
}

async fn replay() -> ExitCode {
    let go = repo_root().join("rust/.go/tfstackplan-go");
    if !go.exists() {
        eprintln!(
            "replay: {} not found; build it first (see rust/README.md). Skipping.",
            go.display()
        );
        return ExitCode::SUCCESS;
    }
    let issuer = tfstackplan_server_clients::gauth::gauthtest::Issuer::new().expect("issuer");
    let dir = tempfile::Builder::new().tempdir().expect("dir");
    let sa = issuer
        .write_service_account_key(dir.path(), "replay@example.test", "http://127.0.0.1:1")
        .expect("key");
    let doc: Value = serde_json::from_slice(&std::fs::read(sa).expect("read key")).expect("json");
    let pem = doc["private_key"].as_str().expect("pem").to_string();

    let go_side = start("go", Some(&go), &pem).await;
    let rs_side = start("rust", None, &pem).await;
    let hc = client();
    let mut report = String::from(
        "# serve replay report\n\nGenerated by `cargo test -p tfstackplan-server --test replay`. Each case is sent to Go's\n`serve` and the Rust `serve` (fresh databases, same config, one fake GitHub each);\nstatus, headers, body, GitHub calls and a dump of every table are compared with\ntimestamps masked.\n\n| case | result |\n|---|---|\n",
    );
    let mut mismatches = BTreeMap::new();
    for c in cases() {
        let (g, r) = (send(&hc, &go_side, &c).await, send(&hc, &rs_side, &c).await);
        // Let background work (supersede check-run closes, reconcile nudges) land.
        tokio::time::sleep(Duration::from_millis(150)).await;
        let mut diffs = Vec::new();
        if g.status != r.status {
            diffs.push(format!("status go={} rust={}", g.status, r.status));
        }
        if g.headers != r.headers {
            diffs.push(format!(
                "headers\n--- go\n{}--- rust\n{}",
                g.headers, r.headers
            ));
        }
        let (gb, rb) = (
            normalise(&g.body, c.unordered),
            normalise(&r.body, c.unordered),
        );
        if gb != rb {
            diffs.push(format!("body\n--- go\n{gb}\n--- rust\n{rb}"));
        }
        let (gc, rc) = (
            std::mem::take(&mut *go_side.calls.lock().unwrap()),
            std::mem::take(&mut *rs_side.calls.lock().unwrap()),
        );
        // Supersede closes the old check run in a background task on both sides, so
        // calls are compared as a multiset; their content is still compared exactly.
        let sorted = |mut v: Vec<String>| {
            v.sort();
            mask(&v.join("\n"))
        };
        let (gc, rc) = (sorted(gc), sorted(rc));
        if gc != rc {
            diffs.push(format!("github calls\n--- go\n{gc}\n--- rust\n{rc}"));
        }
        let (gd, rd) = (dump(&go_side.db), dump(&rs_side.db));
        if gd != rd {
            let first = gd
                .lines()
                .zip(rd.lines())
                .find(|(a, b)| a != b)
                .map(|(a, b)| format!("first differing row\n--- go\n{a}\n--- rust\n{b}"))
                .unwrap_or_else(|| "row counts differ".into());
            diffs.push(format!("database: {first}"));
        }
        let ok = diffs.is_empty();
        report.push_str(&format!(
            "| {} | {} |\n",
            c.name,
            if ok { "match" } else { "MISMATCH" }
        ));
        if !ok {
            mismatches.insert(c.name, diffs.join("\n"));
        }
    }
    report.push_str(&format!(
        "\n{} cases, {} mismatches.\n",
        cases().len(),
        mismatches.len()
    ));
    for (name, d) in &mismatches {
        report.push_str(&format!("\n## {name}\n\n```\n{d}\n```\n"));
    }
    let out = repo_root().join("rust/cases/serve-replay-report.md");
    std::fs::write(&out, &report).expect("write report");
    let _ = (go_side.name, rs_side.name);
    if mismatches.is_empty() {
        ExitCode::SUCCESS
    } else {
        eprintln!("{report}");
        ExitCode::FAILURE
    }
}
