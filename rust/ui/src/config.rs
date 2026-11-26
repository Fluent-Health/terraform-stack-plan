//! The `ui {}` block of the HCL config (Go: `internal/config/ui.go`) and the slice of Go's
//! `config.Load` the ui face needs: the file is parsed whole, an unknown top-level block is
//! an error as in Go, and the last `ui` block wins. The other known blocks (server, serve,
//! classification and so on) belong to other subcommands and are not decoded here, so an
//! error inside one of them does not stop `ui` the way it stops Go's loader.

use std::path::Path;

use hcl::eval::{Context, Evaluate};
use hcl::{Block, Body, Value};

/// The config file looked for in the working directory.
pub const DEFAULT_FILENAME: &str = ".tfstackplan.hcl";

/// Top-level blocks Go's loader knows.
const KNOWN_BLOCKS: [&str; 10] = [
    "classification",
    "diff",
    "links",
    "server",
    "ui",
    "serve",
    "class",
    "progress",
    "cache",
    "env_uniqueness",
];

/// One `tier "<name>" {}` block. The ui authenticates to it with Google ID tokens minted
/// for `audience` (default: the url).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct UiTier {
    pub name: String,
    pub url: String,
    pub audience: String,
}

/// The `oauth {}` sub-block: the Google OAuth client users log in with.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct UiOAuth {
    pub client_id: String,
    pub client_secret_env: String,
    /// The required Workspace domain (the id_token `hd` claim), lowercase.
    pub allowed_domain: String,
    /// Sent as `x-goog-user-project` on user-token PAM calls; empty sends none.
    pub quota_project: String,
}

/// The top-level `ui {}` block.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct UiConfig {
    /// External base URL, trailing slashes trimmed.
    pub public_base_url: String,
    pub session_secret_env: String,
    pub github_webhook_secret_env: String,
    pub tiers: Vec<UiTier>,
    pub oauth: Option<UiOAuth>,
}

/// Go's `config.Load`, keeping the `ui` block: `Ok(None)` when the file has none.
pub fn load(path: &Path) -> Result<Option<UiConfig>, String> {
    let shown = path.display();
    let src = std::fs::read_to_string(path).map_err(|e| match e.kind() {
        std::io::ErrorKind::NotFound => format!("open {shown}: no such file or directory"),
        std::io::ErrorKind::PermissionDenied => format!("open {shown}: permission denied"),
        std::io::ErrorKind::IsADirectory => format!("read {shown}: is a directory"),
        _ => format!("open {shown}: {e}"),
    })?;
    let body: Body = hcl::parse(&src).map_err(|e| format!("parse {shown}: {e}"))?;
    let mut ui = None;
    for blk in body.blocks() {
        let kind = blk.identifier();
        if kind == "ui" {
            ui = Some(decode_ui(blk)?);
        } else if !KNOWN_BLOCKS.contains(&kind) {
            return Err(format!(
                "{shown}: unknown top-level block {}",
                crate::gohttp::go_quote(kind.as_bytes())
            ));
        }
    }
    Ok(ui)
}

/// gohcl's decoding of optional string attributes: unknown attributes and blocks are
/// errors, and a value must evaluate (with no variables or functions) to something that
/// converts to a string.
fn strings(
    body: &Body,
    allowed: &[&str],
    blocks: &[&str],
) -> Result<Vec<(String, String)>, String> {
    let mut out = Vec::new();
    for attr in body.attributes() {
        let key = attr.key();
        if !allowed.contains(&key) {
            return Err(format!(
                "Unsupported argument; An argument named {} is not expected here.",
                crate::gohttp::go_quote(key.as_bytes())
            ));
        }
        let v = attr
            .expr()
            .evaluate(&Context::new())
            .map_err(|e| format!("{key}: {e}"))?;
        let s = match v {
            Value::String(s) => s,
            Value::Number(n) => n.to_string(),
            Value::Bool(b) => b.to_string(),
            _ => {
                return Err(format!(
                    "Incorrect attribute value type; Inappropriate value for attribute {}: string required.",
                    crate::gohttp::go_quote(key.as_bytes())
                ));
            }
        };
        out.push((key.to_string(), s));
    }
    for blk in body.blocks() {
        if !blocks.contains(&blk.identifier()) {
            return Err(format!(
                "Unsupported block type; Blocks of type {} are not expected here.",
                crate::gohttp::go_quote(blk.identifier().as_bytes())
            ));
        }
    }
    Ok(out)
}

fn get(attrs: &[(String, String)], key: &str) -> String {
    attrs
        .iter()
        .find(|(k, _)| k == key)
        .map(|(_, v)| v.clone())
        .unwrap_or_default()
}

/// Go's `decodeUI` with its validation.
fn decode_ui(blk: &Block) -> Result<UiConfig, String> {
    let wrap = |e: String| format!("ui block: {e}");
    let attrs = strings(
        blk.body(),
        &[
            "public_base_url",
            "session_secret_env",
            "github_webhook_secret_env",
        ],
        &["tier", "oauth"],
    )
    .map_err(wrap)?;
    let mut raw_tiers = Vec::new();
    let mut raw_oauth = None;
    for b in blk.body().blocks() {
        let labels: Vec<&str> = b.labels().iter().map(|l| l.as_str()).collect();
        if b.identifier() == "tier" {
            match labels.as_slice() {
                [name] => {
                    let a = strings(b.body(), &["url", "audience"], &[]).map_err(wrap)?;
                    raw_tiers.push(((*name).to_string(), get(&a, "url"), get(&a, "audience")));
                }
                [] => {
                    return Err(wrap(
                        "Missing name for tier; All tier blocks must have 1 labels (name).".into(),
                    ));
                }
                _ => {
                    return Err(wrap(
                        "Extraneous label for tier; Only 1 labels (name) are expected for tier blocks."
                            .into(),
                    ));
                }
            }
        } else {
            if !labels.is_empty() {
                return Err(wrap(
                    "Extraneous label for oauth; No labels are expected for oauth blocks.".into(),
                ));
            }
            if raw_oauth.is_some() {
                return Err(wrap(
                    "Duplicate oauth block; Only one oauth block is allowed.".into(),
                ));
            }
            raw_oauth = Some(
                strings(
                    b.body(),
                    &[
                        "client_id",
                        "client_secret_env",
                        "allowed_domain",
                        "quota_project",
                    ],
                    &[],
                )
                .map_err(wrap)?,
            );
        }
    }
    let mut u = UiConfig {
        public_base_url: get(&attrs, "public_base_url")
            .trim_end_matches('/')
            .to_string(),
        session_secret_env: get(&attrs, "session_secret_env"),
        github_webhook_secret_env: get(&attrs, "github_webhook_secret_env"),
        tiers: Vec::new(),
        oauth: None,
    };
    if raw_tiers.is_empty() {
        return Err("ui: at least one tier block is required".into());
    }
    for (name, url, audience) in raw_tiers {
        let q = crate::gohttp::go_quote(name.as_bytes());
        if u.tiers.iter().any(|t| t.name == name) {
            return Err(format!("ui: duplicate tier {q}"));
        }
        let url = url.trim_end_matches('/').to_string();
        if url.is_empty() {
            return Err(format!("ui tier {q}: url is required"));
        }
        let audience = if audience.is_empty() {
            url.clone()
        } else {
            audience
        };
        u.tiers.push(UiTier {
            name,
            url,
            audience,
        });
    }
    if let Some(o) = raw_oauth {
        let (client_id, secret_env) = (get(&o, "client_id"), get(&o, "client_secret_env"));
        if client_id.is_empty() || secret_env.is_empty() {
            return Err("ui oauth: client_id and client_secret_env are required".into());
        }
        let domain = get(&o, "allowed_domain");
        if domain.is_empty() {
            return Err("ui oauth: allowed_domain is required (the Workspace hd claim)".into());
        }
        u.oauth = Some(UiOAuth {
            client_id,
            client_secret_env: secret_env,
            allowed_domain: domain.to_lowercase(),
            quota_project: get(&o, "quota_project"),
        });
    }
    Ok(u)
}

#[cfg(test)]
mod tests;
