//! Completed-log offload (Go: `internal/server/objects.go`).

use std::sync::LazyLock;
use std::time::Duration;

use async_trait::async_trait;
use reqwest::Method;

use crate::http::{self, is_2xx, path_escape, query_escape};
use crate::{Error, Result, TokenFunc};

/// Persists completed log objects. A missing object is [`crate::Error::NotFound`].
#[async_trait]
pub trait ObjectStore: Send + Sync {
    /// Writes `body` at `key`.
    async fn put(&self, key: &str, body: Vec<u8>) -> Result<()>;
    /// Reads the object at `key`.
    async fn get(&self, key: &str) -> Result<Vec<u8>>;
}

/// Overrides the GCS JSON API base, e.g. to point at a local fake.
pub const ENV_GCS_URL: &str = "TFSTACKPLAN_GCS_URL";

/// The public GCS JSON API.
pub const DEFAULT_GCS_URL: &str = "https://storage.googleapis.com";

/// How long one GCS call may take.
const TIMEOUT: Duration = Duration::from_secs(60);

static GCS_BASE_URL: LazyLock<String> =
    LazyLock::new(|| gcs_base_url_from(std::env::var(ENV_GCS_URL).ok().as_deref()));

/// The GCS base every GCS client uses, read once from `$TFSTACKPLAN_GCS_URL`.
pub fn gcs_base_url() -> &'static str {
    &GCS_BASE_URL
}

/// Go's `gcsBaseURL`: the override without trailing slashes, or the public API when it is
/// unset or empty.
pub fn gcs_base_url_from(env: Option<&str>) -> String {
    match env.map(|v| v.trim_end_matches('/')) {
        Some(v) if !v.is_empty() => v.to_string(),
        _ => DEFAULT_GCS_URL.to_string(),
    }
}

/// Go's `gcsObjectStore`: objects at `<prefix>/<key>` in a bucket, over the GCS JSON API.
pub struct Gcs {
    token: TokenFunc,
    bucket: String,
    prefix: String,
    base_url: String,
    hc: reqwest::Client,
}

impl std::fmt::Debug for Gcs {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Gcs")
            .field("bucket", &self.bucket)
            .field("prefix", &self.prefix)
            .field("base_url", &self.base_url)
            .finish_non_exhaustive()
    }
}

impl Gcs {
    /// A store in `bucket` under `prefix`. An empty `base_url` means [`gcs_base_url`].
    pub fn new(token: TokenFunc, bucket: &str, prefix: &str, base_url: &str) -> Result<Gcs> {
        let base = if base_url.is_empty() {
            gcs_base_url()
        } else {
            base_url
        };
        Ok(Gcs {
            token,
            bucket: bucket.into(),
            prefix: prefix.into(),
            base_url: base.trim_end_matches('/').to_string(),
            hc: http::client(TIMEOUT)?,
        })
    }

    /// The API base this store calls.
    pub fn base_url(&self) -> &str {
        &self.base_url
    }

    fn name(&self, key: &str) -> String {
        if self.prefix.is_empty() {
            key.to_string()
        } else {
            format!("{}/{key}", self.prefix.trim_end_matches('/'))
        }
    }
}

#[async_trait]
impl ObjectStore for Gcs {
    async fn put(&self, key: &str, body: Vec<u8>) -> Result<()> {
        let tok = self.token.token().await?;
        let name = self.name(key);
        let url = format!(
            "{}/upload/storage/v1/b/{}/o?uploadType=media&name={}",
            self.base_url,
            path_escape(&self.bucket),
            query_escape(&name)
        );
        let resp = self
            .hc
            .post(&url)
            .bearer_auth(tok)
            .header("Content-Type", "application/octet-stream")
            .body(body)
            .send()
            .await
            .map_err(|e| Error::msg(e.to_string()))?;
        let status = resp.status();
        if !is_2xx(status) {
            http::log_non_2xx("gcs", &Method::POST, &url, status.as_u16());
            let b = resp.bytes().await.unwrap_or_default();
            return Err(Error::msg(format!(
                "gcs put {name}: {}: {}",
                status.as_u16(),
                String::from_utf8_lossy(&b)
            )));
        }
        Ok(())
    }

    async fn get(&self, key: &str) -> Result<Vec<u8>> {
        let tok = self.token.token().await?;
        let name = self.name(key);
        let url = format!(
            "{}/storage/v1/b/{}/o/{}?alt=media",
            self.base_url,
            path_escape(&self.bucket),
            query_escape(&name)
        );
        let resp = self
            .hc
            .get(&url)
            .bearer_auth(tok)
            .send()
            .await
            .map_err(|e| Error::msg(e.to_string()))?;
        let status = resp.status();
        if !is_2xx(status) {
            http::log_non_2xx("gcs", &Method::GET, &url, status.as_u16());
            let text = format!("gcs get {name}: {}", status.as_u16());
            return Err(if status.as_u16() == 404 {
                Error::NotFound(text)
            } else {
                Error::msg(text)
            });
        }
        Ok(resp
            .bytes()
            .await
            .map_err(|e| Error::msg(e.to_string()))?
            .to_vec())
    }
}

#[cfg(test)]
mod tests;
