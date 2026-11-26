//! `whoami`: the server, the audience and the Google identity the CLI would
//! authenticate as.

use std::path::Path;
use std::process::ExitCode;

use tfstackplan_cli::flag::FlagSet;

use super::{out, parse};
use crate::fsx;
use crate::server::{api_token, env};

/// Runs `whoami`.
pub fn run(args: &[String]) -> ExitCode {
    let mut fs = FlagSet::new("whoami");
    fs.string("env", "", "environment name (optional)");
    let p = match parse(&fs, args) {
        Ok(p) => p,
        Err(c) => return c,
    };
    let environment = p.str("env");

    let mut base = env("TFSTACKPLAN_SERVER");
    if base.is_empty()
        && let Some(cfg) =
            fsx::discover_config(Path::new(".")).and_then(|p| fsx::load_config(&p).ok())
    {
        if !environment.is_empty()
            && let Some(s) = cfg
                .servers
                .iter()
                .find(|s| s.environment == environment || s.name == environment)
        {
            base = s.url.clone();
        }
        if base.is_empty()
            && let Some(s) = &cfg.server
        {
            base = s.url.clone();
        }
    }
    let mut aud = env("TFSTACKPLAN_AUDIENCE");
    if aud.is_empty() {
        aud = base.clone();
    }
    out(&format!("Server URL: {base}\nOIDC Audience: {aud}\n"));
    if aud.is_empty() {
        out("Identity: anonymous (no server/audience configured)\n");
        return ExitCode::SUCCESS;
    }
    let tok = match api_token(&aud) {
        Ok(Some(t)) => t,
        Ok(None) => return ExitCode::SUCCESS,
        Err(e) => {
            out(&format!(
                "Identity: Lookup failed — Google ADC is unavailable: {e}\n"
            ));
            return ExitCode::FAILURE;
        }
    };
    let t = match tok() {
        Ok(t) => t,
        Err(e) => {
            out(&format!("Identity: Fetching token failed: {e}\n"));
            return ExitCode::FAILURE;
        }
    };
    match tfstackplan_cli::jwt::subject(&t) {
        Ok(id) => {
            out(&format!("Identity: {id}\n"));
            ExitCode::SUCCESS
        }
        Err(e) => {
            out(&format!(
                "Identity: Fetched token, but payload decode failed: {e}\n"
            ));
            ExitCode::FAILURE
        }
    }
}
