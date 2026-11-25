//! The parts of `.tfstackplan.hcl` the CLI faces read: the `server` blocks
//! (claims, admin, whoami) and the `env_uniqueness` block (uniqueness). The other
//! top-level blocks belong to `render` and `serve`; they are accepted by name and
//! not decoded here.
//!
//! Finding the file (`config.Discover`) touches the filesystem, so it lives in the
//! binary; this module only parses source text.

use crate::hclx::{self, Decoder};

/// The file name `config.Discover` looks for.
pub const DEFAULT_FILENAME: &str = ".tfstackplan.hcl";

/// Top-level blocks Go's `config.Load` knows.
const KNOWN_BLOCKS: &[&str] = &[
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

/// A `server` block: where the control plane lives.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ServerConfig {
    /// The block's label; empty for the default (unlabeled) server.
    pub name: String,
    /// The server's base URL.
    pub url: String,
    /// The environment this server reports for.
    pub environment: String,
}

/// The parsed file, as far as the CLI faces need it.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Config {
    /// The last unlabeled `server` block.
    pub server: Option<ServerConfig>,
    /// Every `server` block, in order.
    pub servers: Vec<ServerConfig>,
    /// The `env_uniqueness` block.
    pub env_uniqueness: Option<EnvUniquenessConfig>,
}

impl Config {
    /// The server URL for an environment, as `runner.ClientForEnvironment` and
    /// `whoami` pick it: the first block whose environment or name is `env`, else
    /// the default server's URL.
    pub fn server_url_for(&self, env: &str) -> String {
        let mut base = self
            .servers
            .iter()
            .find(|s| s.environment == env || s.name == env)
            .map(|s| s.url.clone())
            .unwrap_or_default();
        if base.is_empty()
            && let Some(s) = &self.server
        {
            base = s.url.clone();
        }
        base
    }
}

/// `env_uniqueness {}`: the per-environment value duplication lint.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct EnvUniquenessConfig {
    /// The tier whose values must not leak into other tiers (default `prod`).
    pub protected_tier: String,
    /// The `{env}` template naming a project (empty when unset).
    pub project_token_template: String,
    /// The declared environments.
    pub environments: Vec<EnvBlock>,
    /// Where instance manifests live; always set after parsing, with defaults filled in.
    pub source: SourceBlock,
    /// Extra identifying tokens per environment, sorted by environment.
    pub extra_env_tokens: Vec<(String, Vec<String>)>,
    /// Extra env-scoped key segments.
    pub extra_scoped_segments: Vec<String>,
    /// Extra identifier key patterns (Go regexps).
    pub extra_key_patterns: Vec<String>,
    /// Reviewed exceptions.
    pub allows: Vec<AllowBlock>,
}

/// `environment "<name>" { tier = "<tier>" }`.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct EnvBlock {
    /// The environment name (the label).
    pub name: String,
    /// Its tier.
    pub tier: String,
}

/// `source {}`: where instance manifests live and which paths inside them to read.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SourceBlock {
    /// `<dir>/**/<pattern>` glob of instance manifests.
    pub glob: String,
    /// Dot path to the per-environment map.
    pub environments_path: String,
    /// Dot path, inside one environment, to its inputs.
    pub inputs_path: String,
    /// Input leaf naming an environment's tier (empty: tiers come from `environment` blocks).
    pub tier_input: String,
}

/// `allow {}`: a reviewed, justified duplicate.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct AllowBlock {
    /// The unit the exception is for.
    pub unit: String,
    /// The key (or `filepath.Match` glob of keys).
    pub key: String,
    /// The environments it covers.
    pub envs: Vec<String>,
    /// Why it is fine.
    pub reason: String,
    /// `YYYY-MM-DD` after which it no longer applies (empty: never).
    pub expires: String,
}

/// Catalyst defaults for an unspecified `source {}`.
pub const DEFAULT_SOURCE_GLOB: &str = "components/**/instances/*.tm.yml";
/// Default dot path to the per-environment map.
pub const DEFAULT_SOURCE_ENVIRONMENTS_PATH: &str = "environments";
/// Default dot path to an environment's inputs.
pub const DEFAULT_SOURCE_INPUTS_PATH: &str = "inputs";
const DEFAULT_PROTECTED_TIER: &str = "prod";

/// `config.Load` on already-read source; `path` names the file in errors.
pub fn parse(src: &str, path: &str) -> Result<Config, String> {
    let body = hclx::parse(src).map_err(|e| format!("parse {path}: {e}"))?;
    let mut cfg = Config::default();
    if let Some(a) = body.attributes().next() {
        return Err(format!(
            "parse {path}: Unsupported argument; An argument named {:?} is not expected here.",
            a.key.as_str()
        ));
    }
    for blk in body.blocks() {
        let kind = blk.identifier.as_str();
        if !KNOWN_BLOCKS.contains(&kind) {
            return Err(format!("{path}: unknown top-level block {kind:?}"));
        }
        match kind {
            "server" => {
                let s = decode_server(blk).map_err(|e| format!("server block: {e}"))?;
                if s.name.is_empty() {
                    cfg.server = Some(s.clone());
                }
                cfg.servers.push(s);
            }
            "env_uniqueness" => {
                cfg.env_uniqueness = Some(decode_env_uniqueness(blk)?);
            }
            _ => {}
        }
    }
    Ok(cfg)
}

fn decode_server(blk: &hcl::Block) -> Result<ServerConfig, String> {
    let mut d = Decoder::new(&blk.body);
    let url = d.opt_str("url")?.unwrap_or_default();
    let environment = d.opt_str("environment")?.unwrap_or_default();
    d.finish()?;
    Ok(ServerConfig {
        name: hclx::labels(blk).into_iter().next().unwrap_or_default(),
        url,
        environment,
    })
}

fn decode_env_uniqueness(blk: &hcl::Block) -> Result<EnvUniquenessConfig, String> {
    let wrap = |e: String| format!("env_uniqueness block: {e}");
    let mut d = Decoder::new(&blk.body);
    let mut c = EnvUniquenessConfig {
        protected_tier: d
            .opt_str("protected_tier")
            .map_err(wrap)?
            .unwrap_or_default(),
        project_token_template: d
            .opt_str("project_token_template")
            .map_err(wrap)?
            .unwrap_or_default(),
        extra_env_tokens: d
            .opt_strings_map("extra_env_tokens")
            .map_err(wrap)?
            .unwrap_or_default(),
        extra_scoped_segments: d
            .opt_strings("extra_scoped_segments")
            .map_err(wrap)?
            .unwrap_or_default(),
        extra_key_patterns: d
            .opt_strings("extra_key_patterns")
            .map_err(wrap)?
            .unwrap_or_default(),
        ..Default::default()
    };
    for env in d.blocks("environment") {
        let labels = hclx::labels(env);
        if labels.len() != 1 {
            return Err(wrap(
                "Missing name for environment; All environment blocks must have 1 labels (name)."
                    .to_string(),
            ));
        }
        let mut e = Decoder::new(&env.body);
        let tier = e.req_str("tier").map_err(wrap)?;
        e.finish().map_err(wrap)?;
        c.environments.push(EnvBlock {
            name: labels[0].clone(),
            tier,
        });
    }
    let sources = d.blocks("source");
    if sources.len() > 1 {
        return Err(wrap(
            "Duplicate source block; Only one source block is allowed.".to_string(),
        ));
    }
    if let Some(src) = sources.first() {
        let mut s = Decoder::new(&src.body);
        c.source = SourceBlock {
            glob: s.opt_str("glob").map_err(wrap)?.unwrap_or_default(),
            environments_path: s
                .opt_str("environments_path")
                .map_err(wrap)?
                .unwrap_or_default(),
            inputs_path: s.opt_str("inputs_path").map_err(wrap)?.unwrap_or_default(),
            tier_input: s.opt_str("tier_input").map_err(wrap)?.unwrap_or_default(),
        };
        s.finish().map_err(wrap)?;
    }
    for a in d.blocks("allow") {
        let mut ad = Decoder::new(&a.body);
        let allow = AllowBlock {
            unit: ad.req_str("unit").map_err(wrap)?,
            key: ad.req_str("key").map_err(wrap)?,
            envs: ad.req_strings("envs").map_err(wrap)?,
            reason: ad.req_str("reason").map_err(wrap)?,
            expires: ad.opt_str("expires").map_err(wrap)?.unwrap_or_default(),
        };
        ad.finish().map_err(wrap)?;
        c.allows.push(allow);
    }
    d.finish().map_err(wrap)?;

    if c.protected_tier.is_empty() {
        c.protected_tier = DEFAULT_PROTECTED_TIER.to_string();
    }
    if c.source.glob.is_empty() {
        c.source.glob = DEFAULT_SOURCE_GLOB.to_string();
    }
    if c.source.environments_path.is_empty() {
        c.source.environments_path = DEFAULT_SOURCE_ENVIRONMENTS_PATH.to_string();
    }
    if c.source.inputs_path.is_empty() {
        c.source.inputs_path = DEFAULT_SOURCE_INPUTS_PATH.to_string();
    }
    for a in &c.allows {
        if a.reason.trim().is_empty() {
            return Err(format!(
                "env_uniqueness: allow {}.{}: reason is required",
                a.unit, a.key
            ));
        }
    }
    Ok(c)
}

#[cfg(test)]
mod tests;
