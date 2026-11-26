//! The one HTTP client setup every client here shares: rustls on the aws-lc-rs provider, with
//! connect and total timeouts so a stalled peer cannot hang a caller.

use std::time::Duration;

use crate::{Error, Result};

/// How long a TCP and TLS connect may take.
pub const CONNECT_TIMEOUT: Duration = Duration::from_secs(10);

/// Installs aws-lc-rs as the process's rustls crypto provider. Safe to call any number of
/// times: a provider already installed (by an earlier call or by anything else) stays.
pub fn install_crypto_provider() {
    // install_default fails only when a provider is already installed, which is the state we want.
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
}

/// A client whose requests give up after `timeout` in total (connect, send and read the body)
/// and whose connects give up after [`CONNECT_TIMEOUT`].
pub fn client(timeout: Duration) -> Result<reqwest::Client> {
    install_crypto_provider();
    reqwest::Client::builder()
        .connect_timeout(CONNECT_TIMEOUT)
        .read_timeout(timeout)
        .timeout(timeout)
        .build()
        .map_err(|e| Error::msg(format!("http client: {e}")))
}

/// Logs a non-2xx answer from GitHub or Google as status, method and path. Never the body,
/// the query string or any header: bodies and queries can echo tokens back.
pub fn log_non_2xx(service: &str, method: &reqwest::Method, url: &str, status: u16) {
    tracing::warn!(
        service,
        status,
        method = %method,
        path = endpoint_path(url),
        "non-2xx response"
    );
}

/// The path of `url`, without scheme, host, query or fragment.
pub fn endpoint_path(url: &str) -> &str {
    let rest = url.split_once("://").map_or(url, |(_, r)| r);
    let path = rest.find('/').map_or("/", |i| &rest[i..]);
    let end = path.find(['?', '#']).unwrap_or(path.len());
    &path[..end]
}

/// Go's `url.QueryEscape`: everything but ASCII letters, digits and `-_.~` is %-escaped,
/// and a space becomes `+`.
pub fn query_escape(s: &str) -> String {
    escape(
        s,
        |b| b.is_ascii_alphanumeric() || b"-_.~".contains(&b),
        true,
    )
}

/// Go's `url.PathEscape`: escapes a string to sit in one path segment.
pub fn path_escape(s: &str) -> String {
    escape(
        s,
        |b| b.is_ascii_alphanumeric() || b"-_.~$&+:=@".contains(&b),
        false,
    )
}

fn escape(s: &str, keep: impl Fn(u8) -> bool, space_plus: bool) -> String {
    let mut out = String::with_capacity(s.len());
    for b in s.bytes() {
        if keep(b) {
            out.push(char::from(b));
        } else if b == b' ' && space_plus {
            out.push('+');
        } else {
            out.push_str(&format!("%{b:02X}"));
        }
    }
    out
}

/// Go's `resp.StatusCode/100 != 2`.
pub(crate) fn is_2xx(status: reqwest::StatusCode) -> bool {
    status.as_u16() / 100 == 2
}

#[cfg(test)]
mod tests;
