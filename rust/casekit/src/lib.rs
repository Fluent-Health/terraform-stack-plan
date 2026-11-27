//! What the recorder and the replay test share about running a case: the
//! hermetic environment every case gets, and the optional `[harness]` table a
//! case toml can carry (trycmd ignores it):
//!
//! ```toml
//! [harness]
//! requires = ["terraform"]                    # skip the case when not on PATH
//! env = { TFSTACKPLAN_SERVER = "{fake}" }     # `{fake}`: the fake server's base URL;
//!                                             # `{fakehost}`: its host:port
//! adc = "authorized_user"                     # ADC file in HOME, token_uri on the fake server
//!
//! [[harness.route]]                           # canned responses of the fake server
//! method = "POST"
//! path = "/api/claims/list"
//! status = 200
//! body = '[]'
//! ```
//!
//! A case with routes or `adc` gets a fake HTTP server on 127.0.0.1 for each
//! run. Its URL changes from run to run, so outputs show it as `[FAKE]`.

#![forbid(unsafe_code)]

use std::io::{BufRead, BufReader, Read, Write};
use std::net::{TcpListener, TcpStream};
use std::path::Path;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};

/// How the fake server's URL appears in recorded output.
pub const FAKE_VAR: &str = "[FAKE]";
/// How the fake server's host:port appears in recorded output.
pub const FAKE_HOST_VAR: &str = "[FAKEHOST]";

/// Variables every case runs without, so neither binary sees the developer's
/// credentials, servers or overrides. They are set empty (both binaries treat
/// an empty value as unset); HOME is a fresh empty directory.
pub const HERMETIC_UNSET: &[&str] = &[
    "TFSTACKPLAN_SERVER",
    "TFSTACKPLAN_AUDIENCE",
    "TFSTACKPLAN_EXECUTION",
    "TFSTACKPLAN_STACK",
    "TFSTACKPLAN_ENVIRONMENT",
    "TFSTACKPLAN_PR",
    "TFSTACKPLAN_REPO",
    "TFSTACKPLAN_SHA",
    "TFSTACKPLAN_GCS_URL",
    "TFSTACKPLAN_GITHUB_API_URL",
    "GOOGLE_APPLICATION_CREDENTIALS",
    "GOOGLE_OAUTH_ACCESS_TOKEN",
    "CLOUDSDK_CONFIG",
    "GCE_METADATA_HOST",
];

/// Variables the recorder and the replay set themselves, which a case's
/// `env.add` and `[harness] env` must not override.
pub const RESERVED_ENV: &[&str] = &["TFSTACKPLAN_GO_BIN", "TFSTACKPLAN_FALLBACK_LOG", "HOME"];

/// Credential variables in [`HERMETIC_UNSET`]. A case may set one only to point
/// it at the fake server (a value starting with `{fake}` or `{fakehost}`).
pub const CREDENTIAL_ENV: &[&str] = &[
    "GOOGLE_APPLICATION_CREDENTIALS",
    "GOOGLE_OAUTH_ACCESS_TOKEN",
    "CLOUDSDK_CONFIG",
    "GCE_METADATA_HOST",
];

/// The first variable in `env` that a case must not set, if any.
pub fn reserved_key<'a>(env: impl IntoIterator<Item = (&'a str, &'a str)>) -> Option<&'a str> {
    env.into_iter()
        .find(|(k, v)| {
            RESERVED_ENV.contains(k) || (CREDENTIAL_ENV.contains(k) && !v.starts_with("{fake"))
        })
        .map(|(k, _)| k)
}

/// One canned response.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Route {
    /// HTTP method.
    pub method: String,
    /// Request path, without the query string.
    pub path: String,
    /// Response status.
    pub status: u16,
    /// Response body.
    pub body: String,
}

/// A case's `[harness]` table.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Harness {
    /// Programs that must be on PATH.
    pub requires: Vec<String>,
    /// Extra environment, `{fake}` substituted.
    pub env: Vec<(String, String)>,
    /// Write an ADC file of this type into HOME.
    pub adc: Option<String>,
    /// The fake server's routes.
    pub routes: Vec<Route>,
}

fn strings(item: Option<&toml_edit::Item>) -> Vec<String> {
    item.and_then(|i| i.as_array())
        .map(|a| {
            a.iter()
                .filter_map(|v| v.as_str().map(str::to_owned))
                .collect()
        })
        .unwrap_or_default()
}

impl Harness {
    /// Reads `[harness]` from a case toml (absent: the default, empty harness).
    /// Fails if `[harness] env` or the case's `env.add` sets a variable the
    /// recorder and the replay control (see [`reserved_key`]).
    pub fn from_case(doc: &toml_edit::DocumentMut) -> Result<Self, String> {
        let out = match doc.get("harness") {
            Some(h) => Self::parse(h)?,
            None => Self::default(),
        };
        let env_add: Vec<(String, String)> = doc
            .get("env")
            .and_then(|e| e.get("add"))
            .and_then(|a| a.as_table_like())
            .map(|t| {
                t.iter()
                    .filter_map(|(k, v)| Some((k.to_string(), v.as_str()?.to_string())))
                    .collect()
            })
            .unwrap_or_default();
        for (table, env) in [("harness.env", &out.env), ("env.add", &env_add)] {
            if let Some(k) = reserved_key(env.iter().map(|(k, v)| (k.as_str(), v.as_str()))) {
                return Err(if RESERVED_ENV.contains(&k) {
                    format!("{table} must not set {k}: the recorder and the replay set it")
                } else {
                    format!(
                        "{table} may set {k} only to the fake server (a value starting with {{fake}} or {{fakehost}})"
                    )
                });
            }
        }
        Ok(out)
    }

    fn parse(h: &toml_edit::Item) -> Result<Self, String> {
        let mut out = Self {
            requires: strings(h.get("requires")),
            adc: h.get("adc").and_then(|v| v.as_str()).map(str::to_owned),
            ..Self::default()
        };
        if let Some(env) = h.get("env").and_then(|e| e.as_table_like()) {
            for (k, v) in env.iter() {
                let v = v
                    .as_str()
                    .ok_or_else(|| format!("harness.env.{k} must be a string"))?;
                out.env.push((k.to_string(), v.to_string()));
            }
        }
        if let Some(routes) = h.get("route").and_then(|r| r.as_array_of_tables()) {
            for r in routes.iter() {
                let s = |k: &str| {
                    r.get(k)
                        .and_then(|v| v.as_str())
                        .map(str::to_owned)
                        .ok_or_else(|| format!("harness.route.{k} must be a string"))
                };
                out.routes.push(Route {
                    method: s("method")?,
                    path: s("path")?,
                    status: r
                        .get("status")
                        .and_then(|v| v.as_integer())
                        .and_then(|i| u16::try_from(i).ok())
                        .unwrap_or(200),
                    body: r
                        .get("body")
                        .and_then(|v| v.as_str())
                        .unwrap_or_default()
                        .to_string(),
                });
            }
        }
        Ok(out)
    }

    /// The required programs that are not on PATH.
    pub fn missing_tools(&self) -> Vec<String> {
        let path = std::env::var_os("PATH").unwrap_or_default();
        self.requires
            .iter()
            .filter(|t| !std::env::split_paths(&path).any(|d| d.join(t).is_file()))
            .cloned()
            .collect()
    }

    /// Prepares one run: a fresh HOME, the fake server if the case needs one, and
    /// the ADC file.
    pub fn start(&self) -> std::io::Result<Session> {
        let home = tempfile::Builder::new()
            .prefix("tfsp-case-home-")
            .tempdir()?;
        let fake = if self.routes.is_empty() && self.adc.is_none() {
            None
        } else {
            let mut routes = self.routes.clone();
            if self.adc.is_some() {
                routes.push(token_route());
            }
            Some(FakeServer::start(routes)?)
        };
        let url = fake.as_ref().map(FakeServer::url).unwrap_or_default();
        if let Some(kind) = &self.adc {
            let dir = home.path().join(".config/gcloud");
            std::fs::create_dir_all(&dir)?;
            std::fs::write(
                dir.join("application_default_credentials.json"),
                adc_json(kind, &url),
            )?;
        }
        let mut env: Vec<(String, String)> = HERMETIC_UNSET
            .iter()
            .map(|k| (k.to_string(), String::new()))
            .collect();
        env.push(("HOME".into(), home.path().to_string_lossy().into_owned()));
        let host = url.trim_start_matches("http://").to_string();
        for (k, v) in &self.env {
            env.push((
                k.clone(),
                v.replace("{fakehost}", &host).replace("{fake}", &url),
            ));
        }
        Ok(Session { home, fake, env })
    }
}

/// One run's environment. Dropping it stops the fake server and removes HOME.
pub struct Session {
    home: tempfile::TempDir,
    fake: Option<FakeServer>,
    /// Variables to set, in order (later wins).
    pub env: Vec<(String, String)>,
}

impl Session {
    /// The fake server's base URL, if the case has one.
    pub fn fake_url(&self) -> Option<String> {
        self.fake.as_ref().map(FakeServer::url)
    }

    /// The fake server's host:port, if the case has one.
    pub fn fake_host(&self) -> Option<String> {
        self.fake.as_ref().map(|f| f.addr.to_string())
    }

    /// The run's HOME.
    pub fn home(&self) -> &Path {
        self.home.path()
    }

    /// Replaces the fake server's URL in output with [`FAKE_VAR`] (and a bare
    /// host:port with [`FAKE_HOST_VAR`]).
    pub fn redact(&self, s: &str) -> String {
        match self.fake_url() {
            Some(u) => s
                .replace(&u, FAKE_VAR)
                .replace(u.trim_start_matches("http://"), FAKE_HOST_VAR),
            None => s.to_string(),
        }
    }
}

/// An unsigned ID token for a made-up service account: `whoami` only decodes it.
pub const FAKE_ID_TOKEN: &str =
    "eyJhbGciOiJub25lIn0.eyJlbWFpbCI6ImNpLWJvdEBleGFtcGxlLmNvbSIsInN1YiI6IjQyIn0.";

fn token_route() -> Route {
    Route {
        method: "POST".into(),
        path: "/token".into(),
        status: 200,
        body: format!(
            r#"{{"access_token":"fake-access-token","id_token":"{FAKE_ID_TOKEN}","token_type":"Bearer","expires_in":3600}}"#
        ),
    }
}

fn adc_json(kind: &str, url: &str) -> String {
    format!(
        r#"{{"type":"{kind}","client_id":"fake-client.apps.example.com","client_secret":"fake-secret","refresh_token":"fake-refresh-token","token_uri":"{url}/token"}}"#
    )
}

/// A tiny HTTP/1.1 server answering canned routes, one thread per connection.
pub struct FakeServer {
    addr: std::net::SocketAddr,
    stop: Arc<AtomicBool>,
}

impl FakeServer {
    /// Binds 127.0.0.1 on a free port and serves `routes`.
    pub fn start(routes: Vec<Route>) -> std::io::Result<Self> {
        let listener = TcpListener::bind("127.0.0.1:0")?;
        let addr = listener.local_addr()?;
        let stop = Arc::new(AtomicBool::new(false));
        let routes = Arc::new(routes);
        let flag = stop.clone();
        std::thread::spawn(move || {
            for conn in listener.incoming() {
                if flag.load(Ordering::SeqCst) {
                    break;
                }
                let Ok(conn) = conn else { continue };
                let routes = routes.clone();
                std::thread::spawn(move || {
                    let _ = serve(conn, &routes);
                });
            }
        });
        Ok(Self { addr, stop })
    }

    /// `http://127.0.0.1:<port>`.
    pub fn url(&self) -> String {
        format!("http://{}", self.addr)
    }
}

impl Drop for FakeServer {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::SeqCst);
        // Wake the accept loop so it sees the flag.
        let _ = TcpStream::connect(self.addr);
    }
}

fn serve(conn: TcpStream, routes: &[Route]) -> std::io::Result<()> {
    let mut reader = BufReader::new(conn.try_clone()?);
    let mut line = String::new();
    reader.read_line(&mut line)?;
    let mut parts = line.split_whitespace();
    let method = parts.next().unwrap_or_default().to_string();
    let target = parts.next().unwrap_or_default().to_string();
    let path = target.split('?').next().unwrap_or_default().to_string();
    let mut len = 0usize;
    loop {
        let mut h = String::new();
        if reader.read_line(&mut h)? == 0 || h == "\r\n" || h == "\n" {
            break;
        }
        if let Some((k, v)) = h.split_once(':')
            && k.trim().eq_ignore_ascii_case("content-length")
        {
            len = v.trim().parse().unwrap_or(0);
        }
    }
    let mut body = vec![0u8; len];
    reader.read_exact(&mut body)?;
    let (status, text) = routes
        .iter()
        .find(|r| r.method == method && r.path == path)
        .map(|r| (r.status, r.body.clone()))
        .unwrap_or((404, "not found".to_string()));
    let mut w = conn;
    write!(
        w,
        "HTTP/1.1 {status} {}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{text}",
        reason(status),
        text.len()
    )?;
    w.flush()
}

fn reason(status: u16) -> &'static str {
    match status {
        200 => "OK",
        204 => "No Content",
        400 => "Bad Request",
        401 => "Unauthorized",
        403 => "Forbidden",
        404 => "Not Found",
        409 => "Conflict",
        412 => "Precondition Failed",
        500 => "Internal Server Error",
        _ => "Status",
    }
}

#[cfg(test)]
mod tests;
