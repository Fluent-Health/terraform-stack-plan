//! `tfstackplan ui` (Go: `cmd/tfstackplan/ui.go`): flags, config, startup and serving with
//! a graceful drain on SIGTERM.

use std::future::Future;
use std::process::ExitCode;
use std::sync::Arc;
use std::time::Duration;

use hyper::server::conn::http1;
use hyper::service::service_fn;
use hyper_util::rt::{TokioIo, TokioTimer};
use hyper_util::server::graceful::GracefulShutdown;
use tfstackplan_server_clients::gauth::{self, ClaimsVerifier};
use tfstackplan_server_clients::gcpcreds::Env;
use tokio::net::TcpListener;
use tracing::{info, warn};

use crate::app::{App, Config, Tier};
use crate::config::{self, UiConfig};
use crate::oauth::{GOOGLE_AUTH_URL, GOOGLE_TOKEN_URL, OAuth};

const USAGE: &str = "Usage of ui:
  -addr string
    \tlisten address (default \":8081\")
  -config string
    \tHCL config file (default \".tfstackplan.hcl\")
";

/// How the process stops on SIGTERM. Cloud Run gives a container 10 s between SIGTERM and
/// SIGKILL: keep accepting for `linger` (requests already routed here still land), then
/// stop accepting and give in-flight requests up to `drain` to finish. Long-lived streams
/// still open at the deadline are cut.
#[derive(Debug, Clone, Copy)]
pub struct Shutdown {
    pub linger: Duration,
    pub drain: Duration,
}

impl Default for Shutdown {
    fn default() -> Shutdown {
        Shutdown {
            linger: Duration::from_secs(2),
            drain: Duration::from_secs(7),
        }
    }
}

/// Go's `flag` package for the two ui flags. `Err` carries the exit code.
fn parse_flags(args: &[String]) -> Result<(String, String), u8> {
    let mut cfg = config::DEFAULT_FILENAME.to_string();
    let mut addr = ":8081".to_string();
    let mut i = 0;
    let fail = |msg: &str| {
        eprint!("{msg}\n{USAGE}");
        2
    };
    while i < args.len() {
        let a = &args[i];
        if a == "--" || !a.starts_with('-') || a == "-" {
            break;
        }
        let name = a.trim_start_matches('-');
        if a.starts_with("---") || name.is_empty() || name.starts_with('=') {
            return Err(fail(&format!("bad flag syntax: {a}")));
        }
        let (name, inline) = match name.split_once('=') {
            Some((n, v)) => (n, Some(v.to_string())),
            None => (name, None),
        };
        if name == "h" || name == "help" {
            eprint!("{USAGE}");
            return Err(2);
        }
        if name != "config" && name != "addr" {
            return Err(fail(&format!("flag provided but not defined: -{name}")));
        }
        let value = match inline {
            Some(v) => v,
            None => {
                i += 1;
                match args.get(i) {
                    Some(v) => v.clone(),
                    None => return Err(fail(&format!("flag needs an argument: -{name}"))),
                }
            }
        };
        if name == "config" {
            cfg = value;
        } else {
            addr = value;
        }
        i += 1;
    }
    Ok((cfg, addr))
}

/// Runs `tfstackplan ui` with the arguments after the subcommand name.
pub async fn run(args: Vec<String>) -> ExitCode {
    let _ = tracing_subscriber::fmt()
        .with_writer(std::io::stderr)
        .with_target(false)
        .try_init();
    tfstackplan_server_clients::http::install_crypto_provider();
    let (cfg_path, addr) = match parse_flags(&args) {
        Ok(v) => v,
        Err(code) => return ExitCode::from(code),
    };
    let fail = |e: String| {
        eprintln!("tfstackplan ui: {e}");
        ExitCode::from(1)
    };
    let ui = match config::load(std::path::Path::new(&cfg_path)) {
        Ok(Some(u)) => u,
        Ok(None) => return fail("config has no ui {} block".into()),
        Err(e) => return fail(e),
    };
    let cfg = match build_config(&ui, |k| std::env::var(k).unwrap_or_default()).await {
        Ok(c) => c,
        Err(e) => return fail(e),
    };
    let tiers = cfg.tiers.len();
    let app = match App::new(cfg) {
        Ok(a) => a,
        Err(e) => return fail(e),
    };
    let bind = if addr.starts_with(':') {
        format!("0.0.0.0{addr}")
    } else {
        addr.clone()
    };
    let listener = match TcpListener::bind(&bind).await {
        Ok(l) => l,
        Err(e) => return fail(format!("listen tcp {addr}: {e}")),
    };
    eprintln!("tfstackplan ui: listening on {addr} ({tiers} tiers)");
    serve(listener, app, sigterm(), Shutdown::default()).await;
    ExitCode::SUCCESS
}

/// Resolves when SIGTERM or Ctrl-C arrives.
async fn sigterm() {
    #[cfg(unix)]
    {
        use tokio::signal::unix::{SignalKind, signal};
        match signal(SignalKind::terminate()) {
            Ok(mut s) => {
                tokio::select! {
                    _ = s.recv() => {}
                    _ = tokio::signal::ctrl_c() => {}
                }
            }
            Err(_) => {
                let _ = tokio::signal::ctrl_c().await;
            }
        }
    }
    #[cfg(not(unix))]
    {
        let _ = tokio::signal::ctrl_c().await;
    }
}

/// Serves `app` on `listener` until `stop` resolves, then lingers, stops accepting and
/// drains in-flight requests (see [`Shutdown`]). Request headers must arrive within 10 s
/// (Go's `ReadHeaderTimeout`).
pub async fn serve(
    listener: TcpListener,
    app: Arc<App>,
    stop: impl Future<Output = ()>,
    shutdown: Shutdown,
) {
    let graceful = GracefulShutdown::new();
    let mut builder = http1::Builder::new();
    builder
        .timer(TokioTimer::new())
        .header_read_timeout(Duration::from_secs(10));
    let accept = |stream: tokio::net::TcpStream| {
        let app = Arc::clone(&app);
        let svc = service_fn(move |req: hyper::Request<hyper::body::Incoming>| {
            let app = Arc::clone(&app);
            async move {
                Ok::<_, std::convert::Infallible>(app.handle(req.map(axum::body::Body::new)).await)
            }
        });
        let conn = builder.serve_connection(TokioIo::new(stream), svc);
        let conn = graceful.watch(conn);
        tokio::spawn(async move {
            let _ = conn.await;
        });
    };
    let mut backoff = Duration::ZERO;
    tokio::pin!(stop);
    loop {
        tokio::select! {
            _ = &mut stop => break,
            conn = listener.accept() => {
                match conn {
                    Ok((stream, _)) => {
                        backoff = Duration::ZERO;
                        accept(stream);
                    }
                    Err(e) => accept_backoff(&mut backoff, &e).await,
                }
            }
        }
    }
    info!("ui: SIGTERM: finishing in-flight requests");
    let linger = tokio::time::sleep(shutdown.linger);
    tokio::pin!(linger);
    loop {
        tokio::select! {
            _ = &mut linger => break,
            conn = listener.accept() => {
                match conn {
                    Ok((stream, _)) => {
                        backoff = Duration::ZERO;
                        accept(stream);
                    }
                    Err(e) => accept_backoff(&mut backoff, &e).await,
                }
            }
        }
    }
    drop(listener);
    if tokio::time::timeout(shutdown.drain, graceful.shutdown())
        .await
        .is_err()
    {
        warn!("ui: drain deadline passed with requests still open");
    }
}

/// Go's `buildUIConfig`: env-var secrets, the OAuth client and id_token verifier, and one
/// OIDC token source per tier. A tier whose credentials cannot mint ID tokens is called
/// unauthenticated (the tier then rejects visibly) instead of blocking startup.
pub async fn build_config(u: &UiConfig, env: impl Fn(&str) -> String) -> Result<Config, String> {
    if u.session_secret_env.is_empty() {
        return Err("ui: session_secret_env is required".into());
    }
    let session_secret = env(&u.session_secret_env);
    if session_secret.is_empty() {
        return Err(format!(
            "ui: {} is empty \u{2014} set the session secret",
            u.session_secret_env
        ));
    }
    let mut out = Config {
        public_base_url: u.public_base_url.clone(),
        session_secret,
        ..Config::default()
    };
    if !u.github_webhook_secret_env.is_empty() {
        out.github_webhook_secret = env(&u.github_webhook_secret_env);
        if out.github_webhook_secret.is_empty() {
            eprintln!(
                "tfstackplan ui: {} is empty \u{2014} GitHub webhook relay disabled",
                u.github_webhook_secret_env
            );
        }
    }
    if let Some(o) = &u.oauth {
        let client_secret = env(&o.client_secret_env);
        if client_secret.is_empty() {
            return Err(format!(
                "ui oauth: {} is empty \u{2014} set the OAuth client secret",
                o.client_secret_env
            ));
        }
        if u.public_base_url.is_empty() {
            return Err("ui oauth: public_base_url is required (the OAuth redirect base)".into());
        }
        out.oauth = Some(OAuth {
            client_id: o.client_id.clone(),
            client_secret,
            auth_url: GOOGLE_AUTH_URL.into(),
            token_url: GOOGLE_TOKEN_URL.into(),
            redirect_url: format!("{}/auth/callback", u.public_base_url),
            scopes: vec!["openid".into(), "email".into(), "profile".into()],
        });
        out.allowed_domain = o.allowed_domain.clone();
        out.quota_project = o.quota_project.clone();
        out.verify_id_token = Some(
            ClaimsVerifier::new(vec![o.client_id.clone()])
                .map_err(|e| format!("ui oauth: build id_token verifier: {e}"))?,
        );
    }
    let genv = Env::from_env();
    for t in &u.tiers {
        let token = match gauth::source(&genv, &t.audience).await {
            Ok(f) => Some(f),
            Err(e) => {
                eprintln!(
                    "tfstackplan ui: tier {}: no OIDC identity ({e}) \u{2014} calls will be unauthenticated",
                    t.name
                );
                None
            }
        };
        out.tiers.push(Tier {
            name: t.name.clone(),
            url: t.url.clone(),
            token,
        });
    }
    Ok(out)
}

/// After a failed accept (say, out of file descriptors), wait before the next one as Go's
/// `http.Server` does: 5 ms, doubling to at most 1 s, reset by a successful accept.
async fn accept_backoff(delay: &mut Duration, err: &std::io::Error) {
    *delay = (*delay * 2).clamp(Duration::from_millis(5), Duration::from_secs(1));
    warn!("ui: accept failed, retrying in {delay:?}: {err}");
    tokio::time::sleep(*delay).await;
}

#[cfg(test)]
mod tests;
