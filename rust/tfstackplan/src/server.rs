//! Building the control-plane client the way Go's `runner.ClientForEnvironment`
//! does: `$TFSTACKPLAN_SERVER`, else the discovered `.tfstackplan.hcl`'s server
//! for the environment, with Google ID tokens for the audience when ADC has them.

use std::path::Path;

use tfstackplan_clients::server::Client;
use tfstackplan_clients::{TokenFn, google};

use crate::faces::err;
use crate::fsx;

/// A non-empty environment variable.
pub fn env(key: &str) -> String {
    std::env::var(key).unwrap_or_default()
}

/// The configured server URL for an environment: `$TFSTACKPLAN_SERVER`, else the
/// discovered config's matching (or default) server.
fn base_for(environment: &str) -> String {
    let base = env("TFSTACKPLAN_SERVER");
    if !base.is_empty() {
        return base;
    }
    fsx::discover_config(Path::new("."))
        .and_then(|p| fsx::load_config(&p).ok())
        .map(|cfg| cfg.server_url_for(environment))
        .unwrap_or_default()
}

/// `runner.APITokenFunc`: ID tokens for a non-empty audience.
pub fn api_token(audience: &str) -> Result<Option<TokenFn>, String> {
    if audience.is_empty() {
        return Ok(None);
    }
    google::id_token_source(audience).map(Some)
}

/// `runner.ClientForEnvironment(environment)`.
pub fn client_for_environment(environment: &str) -> Client {
    let base = base_for(environment);
    if base.is_empty() {
        return disabled();
    }
    let aud_env = env("TFSTACKPLAN_AUDIENCE");
    let aud = if aud_env.is_empty() {
        base.clone()
    } else {
        aud_env.clone()
    };
    let tok = match api_token(&aud) {
        Ok(t) => t,
        Err(e) => {
            if !aud_env.is_empty() {
                err(&format!(
                    "tfstackplan: OIDC audience is set but Google ADC is unavailable ({e}) — reporting unauthenticated\n"
                ));
            }
            None
        }
    };
    match Client::new(&base, tok) {
        Ok(mut c) => {
            c.set_audience(&aud);
            c
        }
        Err(e) => {
            err(&format!("tfstackplan: {e}\n"));
            disabled()
        }
    }
}

/// `runner.ClientFromEnv()`: the client for `$TFSTACKPLAN_ENVIRONMENT`.
pub fn client_from_env() -> Client {
    client_for_environment(&env("TFSTACKPLAN_ENVIRONMENT"))
}

fn disabled() -> Client {
    #[expect(
        clippy::expect_used,
        reason = "a disabled client builds no HTTP client and cannot fail"
    )]
    Client::new("", None).expect("disabled client")
}
