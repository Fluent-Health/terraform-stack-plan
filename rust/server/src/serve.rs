//! The `serve` subcommand (Go: `cmd/tfstackplan/serve.go`): flags, the `serve {}`
//! configuration, the backends, the background loops and graceful shutdown.

use std::collections::{BTreeMap, HashMap};
use std::future::Future;
use std::path::Path;
use std::process::ExitCode;
use std::sync::Arc;
use std::time::Duration;

use axum::Router;
use axum::http::StatusCode;
use axum::response::Response;
use tfstackplan_server_clients::approval::gcppam;
use tfstackplan_server_clients::executor::cloudbuild::CloudBuild;
use tfstackplan_server_clients::gauth;
use tfstackplan_server_clients::gcpcreds;
use tfstackplan_server_clients::github::app::RealClient;
use tfstackplan_server_clients::objects::{Gcs, gcs_base_url};
use tfstackplan_server_clients::{Impersonator, TokenFunc};
use tfstackplan_store::Store;
use tokio::net::TcpListener;
use tracing::warn;

use crate::config::{
    Config, PhaseWeight, ProgressConfig, SCOPE_ADMIN, SCOPE_READ, SCOPE_REPORT,
    default_phase_weight,
};
use crate::{App, Verifier, http};

/// After SIGTERM, how long serve keeps accepting before it stops (load balancers
/// may still route to it for a moment).
pub const LAME_DUCK: Duration = Duration::from_secs(2);
/// How long in-flight requests get to finish after accepting stops; Cloud Run's
/// grace period is 10 s.
pub const DRAIN: Duration = Duration::from_secs(7);

/// GET /api/catalog: the component catalog of the working directory.
pub(crate) async fn catalog(_app: Arc<App>) -> Response {
    match crate::catalog::build(".").await {
        Ok(c) => http::json_ok(&c),
        Err(e) => http::error(
            StatusCode::INTERNAL_SERVER_ERROR,
            &format!("build catalog: {e}"),
        ),
    }
}

/// The `serve {}` block and the top-level blocks serve reads.
#[derive(Debug, Clone, Default)]
pub struct ServeFile {
    pub db_path: String,
    pub public_base_url: String,
    pub ui_base_url: String,
    pub github_webhook_secret_env: String,
    pub logs_dir: String,
    pub github_app: Option<(String, String, String)>,
    pub approval: Option<Approval>,
    pub objects: Option<(String, String, String)>,
    pub pubsub: Option<(String, String)>,
    pub api_auth: Option<ApiAuth>,
    pub executor: Option<Executor>,
    pub environment: String,
    pub progress: Option<ProgressConfig>,
    /// class → (entitlement, entitlement scope).
    pub classes: Vec<(String, String, String)>,
    pub has_serve: bool,
}

#[derive(Debug, Clone, Default)]
pub struct Approval {
    pub backend: String,
    pub location: String,
    pub duration: String,
    pub requester_pool: Vec<String>,
}

#[derive(Debug, Clone, Default)]
pub struct ApiAuth {
    pub audience: String,
    pub extra_audiences: Vec<String>,
    pub principals: Vec<(String, Vec<String>)>,
}

#[derive(Debug, Clone, Default)]
pub struct Executor {
    pub backend: String,
    pub project: String,
    pub region: String,
    pub triggers: BTreeMap<String, String>,
}

fn attr_str(body: &hcl::Body, key: &str) -> Result<String, String> {
    match body
        .attributes()
        .find(|a| a.key() == key)
        .map(hcl::Attribute::expr)
    {
        None => Ok(String::new()),
        Some(hcl::Expression::String(s)) => Ok(s.clone()),
        Some(other) => Err(format!("{key}: expected a string, got {other}")),
    }
}

fn attr_list(body: &hcl::Body, key: &str) -> Result<Vec<String>, String> {
    match body
        .attributes()
        .find(|a| a.key() == key)
        .map(hcl::Attribute::expr)
    {
        None => Ok(Vec::new()),
        Some(hcl::Expression::Array(items)) => items
            .iter()
            .map(|e| match e {
                hcl::Expression::String(s) => Ok(s.clone()),
                other => Err(format!("{key}: expected a list of strings, got {other}")),
            })
            .collect(),
        Some(other) => Err(format!("{key}: expected a list of strings, got {other}")),
    }
}

fn label(b: &hcl::Block, i: usize) -> String {
    b.labels()
        .get(i)
        .map(|l| l.as_str().to_string())
        .unwrap_or_default()
}

fn block<'a>(body: &'a hcl::Body, name: &str) -> Option<&'a hcl::Block> {
    body.blocks().find(|b| b.identifier() == name)
}

/// Loads the serve-relevant parts of the HCL config. The render blocks
/// (classification, diff, links, cache, env_uniqueness, ui) are accepted and left to
/// their own subcommands.
pub fn load(path: &str) -> Result<ServeFile, String> {
    let src = std::fs::read_to_string(path).map_err(|e| open_error(path, &e))?;
    let body = hcl::parse(&src).map_err(|e| format!("parse {path}: {e}"))?;
    let mut f = ServeFile::default();
    for blk in body.blocks() {
        match blk.identifier() {
            "classification" | "diff" | "links" | "ui" | "cache" | "env_uniqueness" => {}
            "server" => {
                if blk.labels().is_empty() {
                    f.environment = attr_str(blk.body(), "environment")
                        .map_err(|e| format!("server block: {e}"))?;
                }
            }
            "class" => {
                if blk.labels().len() != 1 {
                    return Err("class block needs exactly one name label".into());
                }
                let name = label(blk, 0);
                let ent = attr_str(blk.body(), "entitlement")
                    .map_err(|e| format!("class {name:?}: {e}"))?;
                let scope = attr_str(blk.body(), "entitlement_scope")
                    .map_err(|e| format!("class {name:?}: {e}"))?;
                f.classes.push((name, ent, scope));
            }
            "progress" => f.progress = Some(decode_progress(blk)?),
            "serve" => decode_serve(blk.body(), &mut f)?,
            other => return Err(format!("{path}: unknown top-level block {other:?}")),
        }
    }
    Ok(f)
}

fn decode_progress(blk: &hcl::Block) -> Result<ProgressConfig, String> {
    let mut pc = ProgressConfig::default();
    for op in blk.body().blocks() {
        let mut phases = Vec::new();
        for b in op.body().blocks() {
            if b.identifier() != "phase" {
                return Err(format!(
                    "progress.{}: unknown block {:?} (want phase)",
                    op.identifier(),
                    b.identifier()
                ));
            }
            if b.labels().len() != 1 {
                return Err(format!(
                    "progress.{}.phase: expected one label (the phase name)",
                    op.identifier()
                ));
            }
            let ph = label(b, 0);
            let mut w = default_phase_weight(&ph);
            if let Some(a) = b.body().attributes().find(|a| a.key() == "weight") {
                w = match a.expr() {
                    hcl::Expression::Number(n) => n.as_f64().unwrap_or(w),
                    other => {
                        return Err(format!(
                            "progress.{}.phase {ph:?} weight: {other}",
                            op.identifier()
                        ));
                    }
                };
            }
            phases.push(PhaseWeight {
                phase: tfstackplan_gate::events::Phase::new(&ph),
                weight: w,
            });
        }
        match op.identifier() {
            "plan" => pc.plan = phases,
            "apply" => pc.apply = phases,
            other => {
                return Err(format!(
                    "progress: unknown block {other:?} (want plan|apply)"
                ));
            }
        }
    }
    Ok(pc)
}

fn decode_serve(b: &hcl::Body, f: &mut ServeFile) -> Result<(), String> {
    let s = |k: &str| attr_str(b, k).map_err(|e| format!("serve block: {e}"));
    f.has_serve = true;
    f.db_path = s("db_path")?;
    f.public_base_url = s("public_base_url")?;
    f.ui_base_url = s("ui_base_url")?.trim_end_matches('/').to_string();
    f.github_webhook_secret_env = s("github_webhook_secret_env")?;
    f.logs_dir = s("logs_dir")?;
    if let Some(g) = block(b, "github_app") {
        f.github_app = Some((
            attr_str(g.body(), "app_id")?,
            attr_str(g.body(), "installation_id")?,
            attr_str(g.body(), "private_key_path")?,
        ));
    }
    if let Some(a) = block(b, "approval") {
        f.approval = Some(Approval {
            backend: label(a, 0),
            location: attr_str(a.body(), "location")?,
            duration: attr_str(a.body(), "duration")?,
            requester_pool: attr_list(a.body(), "requester_pool")?,
        });
    }
    if let Some(o) = block(b, "objects") {
        f.objects = Some((
            attr_str(o.body(), "backend")?,
            attr_str(o.body(), "bucket")?,
            attr_str(o.body(), "prefix")?,
        ));
    }
    if let Some(p) = block(b, "pubsub") {
        f.pubsub = Some((
            attr_str(p.body(), "audience")?,
            attr_str(p.body(), "service_account")?,
        ));
    }
    if let Some(a) = block(b, "api_auth") {
        let mut aa = ApiAuth {
            audience: attr_str(a.body(), "audience")?,
            extra_audiences: attr_list(a.body(), "extra_audiences")?,
            principals: Vec::new(),
        };
        let mut seen = std::collections::HashSet::new();
        for p in a.body().blocks().filter(|p| p.identifier() == "principal") {
            let email = label(p, 0);
            if !seen.insert(email.to_lowercase()) {
                return Err(format!("api_auth: duplicate principal {email:?}"));
            }
            let scopes = attr_list(p.body(), "scopes")?;
            for sc in &scopes {
                if ![SCOPE_REPORT, SCOPE_READ, SCOPE_ADMIN].contains(&sc.as_str()) {
                    return Err(format!(
                        "api_auth principal {email:?}: unknown scope {sc:?} (valid: {SCOPE_REPORT}, {SCOPE_READ}, {SCOPE_ADMIN})"
                    ));
                }
            }
            aa.principals.push((email, scopes));
        }
        f.api_auth = Some(aa);
    }
    if let Some(e) = block(b, "executor") {
        let backend = label(e, 0);
        if backend != "cloudbuild" {
            return Err(format!(
                "executor: unknown backend {backend:?} (only \"cloudbuild\" is implemented)"
            ));
        }
        let (project, region) = (
            attr_str(e.body(), "project")?,
            attr_str(e.body(), "region")?,
        );
        if project.is_empty() || region.is_empty() {
            return Err(format!(
                "executor {backend:?}: project and region are required"
            ));
        }
        let mut triggers = BTreeMap::new();
        for t in e.body().blocks().filter(|t| t.identifier() == "trigger") {
            let kind = label(t, 0);
            if kind != "plan" && kind != "apply" {
                return Err(format!(
                    "executor {backend:?}: unknown trigger kind {kind:?} (valid: plan, apply)"
                ));
            }
            let name = attr_str(t.body(), "name")?;
            if name.is_empty() {
                return Err(format!(
                    "executor {backend:?}: trigger {kind:?} needs a name"
                ));
            }
            if triggers.insert(kind.clone(), name).is_some() {
                return Err(format!("executor {backend:?}: duplicate trigger {kind:?}"));
            }
        }
        if !triggers.contains_key("plan") || !triggers.contains_key("apply") {
            return Err(format!(
                "executor {backend:?}: both a plan and an apply trigger are required"
            ));
        }
        f.executor = Some(Executor {
            backend,
            project,
            region,
            triggers,
        });
    }
    Ok(())
}

/// The per-stack log buffer directory: the configured one, else `<dir(db)>/logs`.
pub fn default_logs_dir(configured: &str, db_path: &str) -> String {
    if !configured.is_empty() {
        return configured.to_string();
    }
    let dir = Path::new(db_path)
        .parent()
        .filter(|p| !p.as_os_str().is_empty())
        .unwrap_or(Path::new("."));
    dir.join("logs").to_string_lossy().into_owned()
}

fn verifier(v: gauth::Verifier) -> Verifier {
    let v = Arc::new(v);
    Arc::new(move |token: String| {
        let v = Arc::clone(&v);
        Box::pin(async move { v.verify(&token).await.map_err(|e| e.to_string()) })
    })
}

/// The GCP credentials factory: an access-token source and an impersonator.
/// Injected so the wiring is testable without live GCP (Go: `credsFactory`).
pub type CredsFactory = Arc<
    dyn Fn() -> futures_util::future::BoxFuture<
            'static,
            Result<(TokenFunc, Arc<dyn Impersonator>), String>,
        > + Send
        + Sync,
>;

/// The real factory: Application Default Credentials and IAM Credentials.
pub fn real_creds() -> CredsFactory {
    Arc::new(|| {
        Box::pin(async {
            gcpcreds::gcp_creds(&gcpcreds::Env::from_env())
                .await
                .map_err(|e| e.to_string())
        })
    })
}

/// Go's `os` error text for a failed open.
fn open_error(path: &str, e: &std::io::Error) -> String {
    let why = match e.kind() {
        std::io::ErrorKind::NotFound => "no such file or directory".to_string(),
        std::io::ErrorKind::PermissionDenied => "permission denied".to_string(),
        _ => e.to_string(),
    };
    format!("open {path}: {why}")
}

/// Builds the app from the config (Go: `buildServeApp`). Errors are Go's text.
pub async fn build_app(
    f: &ServeFile,
    webhook_secret: String,
    creds: CredsFactory,
) -> Result<Arc<App>, String> {
    if !f.has_serve {
        return Err("serve: no `serve {}` block in config".into());
    }
    if f.db_path.is_empty() {
        return Err("serve: db_path is required".into());
    }
    let path = f.db_path.clone();
    let store = tokio::task::spawn_blocking(move || Store::open(path))
        .await
        .map_err(|e| format!("serve: open store: {e}"))?
        .map_err(|e| format!("serve: open store: {e}"))?;
    let Some((app_id, inst, key_path)) = &f.github_app else {
        return Err("serve: github_app block is required".into());
    };
    let key = std::fs::read(key_path)
        .map_err(|e| format!("serve: read github app key: {}", open_error(key_path, &e)))?;
    let gh =
        RealClient::new(app_id, inst, &key).map_err(|e| format!("serve: github client: {e}"))?;
    let logs_dir = default_logs_dir(&f.logs_dir, &f.db_path);
    eprintln!("tfstackplan serve: log buffers in {logs_dir}");
    let mut build_trigger_names = HashMap::new();
    if let Some(ex) = &f.executor {
        for (kind, name) in &ex.triggers {
            if !name.is_empty() {
                build_trigger_names.insert(name.clone(), kind.clone());
            }
        }
    }
    let cfg = Config {
        public_base_url: f.public_base_url.clone(),
        ui_base_url: f.ui_base_url.clone(),
        environment: f.environment.clone(),
        logs_dir,
        push_service_account: f.pubsub.as_ref().map(|p| p.1.clone()).unwrap_or_default(),
        api_principals: f
            .api_auth
            .as_ref()
            .map(|a| {
                a.principals
                    .iter()
                    .map(|(e, s)| (e.to_lowercase(), s.clone()))
                    .collect()
            })
            .unwrap_or_default(),
        github_webhook_secret: webhook_secret,
        progress: f.progress.clone(),
        build_trigger_names,
        requester_pool: f
            .approval
            .as_ref()
            .map(|a| a.requester_pool.clone())
            .unwrap_or_default(),
    };
    let mut app = App::new(Arc::new(store), Arc::new(gh), cfg);
    if let Some(aa) = &f.api_auth {
        let aud = if aa.audience.is_empty() {
            f.public_base_url.trim_end_matches('/').to_string()
        } else {
            aa.audience.clone()
        };
        if aud.is_empty() {
            return Err(
                "serve: api_auth needs an audience (set api_auth.audience or public_base_url)"
                    .into(),
            );
        }
        let mut auds = vec![aud];
        auds.extend(aa.extra_audiences.iter().cloned());
        app.api_verifier = Some(verifier(
            gauth::Verifier::new(auds).map_err(|e| format!("serve: api_auth verifier: {e}"))?,
        ));
    }
    if let Some((aud, _)) = &f.pubsub {
        let aud = if aud.is_empty() {
            format!("{}/pubsub/push", f.public_base_url.trim_end_matches('/'))
        } else {
            aud.clone()
        };
        app.push_verifier = Some(verifier(
            gauth::Verifier::new(vec![aud]).map_err(|e| format!("serve: pubsub verifier: {e}"))?,
        ));
    }
    if let Some(ex) = &f.executor {
        let (token, _) = creds()
            .await
            .map_err(|e| format!("serve: gcp credentials for executor: {e}"))?;
        let cb = CloudBuild::new(&ex.project, &ex.region, ex.triggers.clone(), token)
            .map_err(|e| format!("serve: gcp credentials for executor: {e}"))?;
        app.executor = Some(Arc::new(cb));
    }
    if let Some(a) = &f.approval
        && a.backend == "gcp-pam"
    {
        let (token, impersonate) = creds()
            .await
            .map_err(|e| format!("serve: gcp credentials: {e}"))?;
        let mut pc = gcppam::Config {
            location: a.location.clone(),
            duration: a.duration.clone(),
            requester_pool: a.requester_pool.clone(),
            ..gcppam::Config::default()
        };
        for (class, ent, scope) in &f.classes {
            if !ent.is_empty() {
                pc.entitlements.insert(class.clone(), ent.clone());
                if !scope.is_empty() {
                    pc.entitlement_scopes.insert(class.clone(), scope.clone());
                }
            }
        }
        let backend = gcppam::Backend::new(pc, token, impersonate)
            .map_err(|e| format!("serve: gcp credentials: {e}"))?;
        app.approval = Some(Arc::new(backend));
    }
    if let Some((backend, bucket, prefix)) = &f.objects
        && (backend.is_empty() || backend == "gcs")
        && !bucket.is_empty()
    {
        let (token, _) = creds()
            .await
            .map_err(|e| format!("serve: gcp creds for objects: {e}"))?;
        let gcs = Gcs::new(token, bucket, prefix, gcs_base_url())
            .map_err(|e| format!("serve: gcp creds for objects: {e}"))?;
        app.objects = Some(Arc::new(gcs));
    }
    Ok(Arc::new(app))
}

const USAGE: &str = "Usage of serve:\n  -addr string\n    \tlisten address (default \":8080\")\n  -config string\n    \tHCL config file (default \".tfstackplan.hcl\")\n  -demo\n    \tboot in credential-free demo mode with seeded data\n";

/// The parsed flags.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Flags {
    pub config: String,
    pub addr: String,
    pub demo: bool,
}

/// Go's `flag` parsing for serve's three flags; `Err` is the text Go prints to
/// stderr (the caller exits 2).
pub fn parse_flags(args: &[String]) -> Result<Flags, String> {
    let mut f = Flags {
        config: ".tfstackplan.hcl".into(),
        addr: ":8080".into(),
        demo: false,
    };
    let mut i = 0;
    while i < args.len() {
        let a = &args[i];
        if a == "--" {
            break;
        }
        let Some(name) = a.strip_prefix("--").or_else(|| a.strip_prefix('-')) else {
            break;
        };
        if name.is_empty() || name.starts_with('-') || name.starts_with('=') {
            return Err(format!("bad flag syntax: {a}\n{USAGE}"));
        }
        let (name, value) = match name.split_once('=') {
            Some((n, v)) => (n, Some(v.to_string())),
            None => (name, None),
        };
        match name {
            "h" | "help" => return Err(USAGE.to_string()),
            "demo" => {
                f.demo = match value.as_deref() {
                    None | Some("1" | "t" | "T" | "true" | "TRUE" | "True") => true,
                    Some("0" | "f" | "F" | "false" | "FALSE" | "False") => false,
                    Some(v) => {
                        return Err(format!(
                            "invalid boolean value {v:?} for -demo: parse error\n{USAGE}"
                        ));
                    }
                };
            }
            "config" | "addr" => {
                let v = match value {
                    Some(v) => v,
                    None => {
                        i += 1;
                        match args.get(i) {
                            Some(v) => v.clone(),
                            None => {
                                return Err(format!("flag needs an argument: -{name}\n{USAGE}"));
                            }
                        }
                    }
                };
                if name == "config" {
                    f.config = v;
                } else {
                    f.addr = v;
                }
            }
            other => return Err(format!("flag provided but not defined: -{other}\n{USAGE}")),
        }
        i += 1;
    }
    Ok(f)
}

/// Serves `router` on `listener` until `signal` resolves, then keeps accepting for
/// `lame_duck`, stops accepting, and gives in-flight requests `drain` to finish.
pub async fn serve_until(
    listener: TcpListener,
    router: Router,
    signal: impl Future<Output = ()> + Send + 'static,
    lame_duck: Duration,
    drain: Duration,
) -> std::io::Result<()> {
    let (stop_tx, stop_rx) = tokio::sync::oneshot::channel::<()>();
    let (stopped_tx, stopped_rx) = tokio::sync::oneshot::channel::<()>();
    tokio::spawn(async move {
        signal.await;
        tokio::time::sleep(lame_duck).await;
        let _ = stop_tx.send(());
        let _ = stopped_tx.send(());
    });
    let server = axum::serve(listener, router).with_graceful_shutdown(async move {
        let _ = stop_rx.await;
    });
    let server = std::pin::pin!(server.into_future());
    tokio::select! {
        r = server => r,
        () = async {
            let _ = stopped_rx.await;
            tokio::time::sleep(drain).await;
        } => {
            warn!("serve: drain deadline passed with requests still in flight");
            Ok(())
        }
    }
}

async fn terminate() {
    let ctrl_c = async {
        let _ = tokio::signal::ctrl_c().await;
    };
    #[cfg(unix)]
    {
        let term = async {
            match tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()) {
                Ok(mut s) => {
                    s.recv().await;
                }
                Err(_) => std::future::pending::<()>().await,
            }
        };
        tokio::select! { () = ctrl_c => {}, () = term => {} }
    }
    #[cfg(not(unix))]
    ctrl_c.await;
}

/// Go's `log` timestamp, `2006/01/02 15:04:05`, in UTC (serve runs in UTC containers).
struct GoTime;

impl tracing_subscriber::fmt::time::FormatTime for GoTime {
    fn format_time(&self, w: &mut tracing_subscriber::fmt::format::Writer<'_>) -> std::fmt::Result {
        write!(
            w,
            "{}",
            jiff::Timestamp::now().strftime("%Y/%m/%d %H:%M:%S")
        )
    }
}

/// Routes `tracing` events to stderr in Go's log format. Idempotent.
pub fn init_logging() {
    let _ = tracing_subscriber::fmt()
        .with_writer(std::io::stderr)
        .with_timer(GoTime)
        .with_level(false)
        .with_target(false)
        .try_init();
}

/// `tfstackplan serve`: returns the process exit code.
pub async fn run(args: Vec<String>) -> ExitCode {
    tfstackplan_server_clients::http::install_crypto_provider();
    init_logging();
    let flags = match parse_flags(&args) {
        Ok(f) => f,
        Err(msg) => {
            eprint!("{msg}");
            return ExitCode::from(2);
        }
    };
    let (app, _demo_dir) = if flags.demo {
        eprintln!("tfstackplan serve: starting in credential-free demo mode...");
        let dir = match tempfile::Builder::new()
            .prefix("tfstackplan-demo-")
            .tempdir()
        {
            Ok(d) => d,
            Err(e) => {
                eprintln!("tfstackplan serve: create temp dir: {e}");
                return ExitCode::from(1);
            }
        };
        match crate::demo::build_demo_app(&dir.path().join("demo.db")) {
            Ok(app) => {
                spawn_demo_seed(flags.addr.clone());
                (app, Some(dir))
            }
            Err(e) => {
                eprintln!("tfstackplan serve demo: {e}");
                return ExitCode::from(1);
            }
        }
    } else {
        match configured_app(&flags.config).await {
            Ok(app) => (app, None),
            Err(code) => return code,
        }
    };
    serve_app(app, &flags.addr).await
}

/// Seeds the demo scenario once the listener is up, then prints where to look.
fn spawn_demo_seed(addr: String) {
    tokio::spawn(async move {
        tokio::time::sleep(Duration::from_millis(150)).await;
        let host = if addr.starts_with(':') {
            format!("127.0.0.1{addr}")
        } else {
            addr
        };
        match crate::demo::seed_scenario(&format!("http://{host}")).await {
            Err(e) => eprintln!("demo: seed scenario failed: {e}"),
            Ok((plan, apply)) => {
                let bar = "=================================================================";
                eprint!(
                    "\n{bar}\nDEMO MODE READY!\nSeeded Plan ID:      {plan}\nSeeded Apply ID:     {apply}\nExecution API:       http://{host}/api/execution/{plan}\nReady URL:           http://{host}/ready\nPoint a `tfstackplan ui` tier at http://{host} to browse it.\n{bar}\n\n"
                );
            }
        }
    });
}

/// Loads the config and builds the app; `Err` is the exit code after the message.
async fn configured_app(config: &str) -> Result<Arc<App>, ExitCode> {
    let file = match load(config) {
        Ok(f) => f,
        Err(e) => {
            eprintln!("tfstackplan serve: {e}");
            return Err(ExitCode::from(1));
        }
    };
    let secret = if file.github_webhook_secret_env.is_empty() {
        String::new()
    } else {
        std::env::var(&file.github_webhook_secret_env).unwrap_or_default()
    };
    match build_app(&file, secret, real_creds()).await {
        Ok(a) => Ok(a),
        Err(e) => {
            eprintln!("tfstackplan serve: {e}");
            Err(ExitCode::from(1))
        }
    }
}

/// Starts the loops and serves until SIGTERM.
async fn serve_app(app: Arc<App>, addr: &str) -> ExitCode {
    let loops = [
        tokio::spawn(Arc::clone(&app).reconcile_loop(Duration::from_secs(30))),
        tokio::spawn(
            Arc::clone(&app).watchdog_loop(Duration::from_secs(60), Duration::from_secs(600)),
        ),
        tokio::spawn(Arc::clone(&app).orphan_sweep_loop(Duration::from_secs(300))),
        tokio::spawn(Arc::clone(&app).claims_sweep_loop(Duration::from_secs(60))),
    ];
    let cleaner = Arc::clone(&app);
    tokio::spawn(async move {
        cleaner
            .clean_log_buffers(Duration::from_secs(24 * 3600))
            .await
    });
    eprintln!("tfstackplan serve: listening on {addr}");
    let bind = if addr.starts_with(':') {
        format!("0.0.0.0{addr}")
    } else {
        addr.to_string()
    };
    let listener = match TcpListener::bind(&bind).await {
        Ok(l) => l,
        Err(e) => {
            eprintln!("tfstackplan serve: listen tcp {addr}: {e}");
            return ExitCode::from(1);
        }
    };
    let res = serve_until(listener, app.router(), terminate(), LAME_DUCK, DRAIN).await;
    for l in loops {
        l.abort();
    }
    match res {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("tfstackplan serve: {e}");
            ExitCode::from(1)
        }
    }
}

#[cfg(test)]
mod tests;
