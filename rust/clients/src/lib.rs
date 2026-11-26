//! HTTP for the `tfstackplan` CLI faces: the control-plane server client, GCS
//! (the state lock and the object store) and Google credentials (ADC, the
//! metadata server, service-account keys and impersonation).
//!
//! The CLI is synchronous, so every client here is a blocking `reqwest` client
//! with connect and total timeouts. Call [`install_crypto_provider`] once at
//! startup before building one.

#![forbid(unsafe_code)]

use std::time::Duration;

pub mod cache;
pub mod gcs;
pub mod google;
pub mod server;
pub mod status;

/// A function that returns a currently valid bearer token.
pub type TokenFn = Box<dyn Fn() -> Result<String, String> + Send + Sync>;

/// Installs aws-lc-rs as the process's rustls provider. Only one provider is
/// compiled in, but picking it explicitly means rustls never has to guess. A
/// second call is a no-op.
pub fn install_crypto_provider() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
}

/// A blocking client with a connect timeout and a total per-request timeout.
pub fn http_client(timeout: Duration) -> Result<reqwest::blocking::Client, String> {
    install_crypto_provider();
    reqwest::blocking::Client::builder()
        .connect_timeout(Duration::from_secs(10).min(timeout))
        .timeout(timeout)
        .build()
        .map_err(|e| e.to_string())
}

/// The client for the GCE metadata server: Go's metadata client (2 s dial, 5 s per request)
/// and, like it, no proxy, whatever `HTTP_PROXY` says. Built once per process.
pub fn metadata_client() -> Result<reqwest::blocking::Client, String> {
    static CLIENT: std::sync::OnceLock<Result<reqwest::blocking::Client, String>> =
        std::sync::OnceLock::new();
    CLIENT
        .get_or_init(|| {
            install_crypto_provider();
            reqwest::blocking::Client::builder()
                .no_proxy()
                .connect_timeout(Duration::from_secs(2))
                .timeout(Duration::from_secs(5))
                .build()
                .map_err(|e| e.to_string())
        })
        .clone()
}

/// `$TFSTACKPLAN_GCS_URL` without a trailing slash, else the real GCS JSON API.
pub fn gcs_base_url() -> String {
    let v = std::env::var("TFSTACKPLAN_GCS_URL").unwrap_or_default();
    let v = v.trim_end_matches('/');
    if v.is_empty() {
        "https://storage.googleapis.com".to_string()
    } else {
        v.to_string()
    }
}
