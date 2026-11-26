//! HTTP for the terraform provider cache (Go's `internal/cache` storage and
//! registry clients): provider tarballs in a GCS bucket, and provider zips from a
//! terraform registry.

use std::io::Read;
use std::time::Duration;

use tfstackplan_cli::cache::{Provider, decode_download_url, download_api_url, goarch, goos};
use tfstackplan_cli::gourl::{path_escape, query_escape};

use crate::TokenFn;

/// A readable response body.
pub type Body = Box<dyn Read + Send>;

/// Where provider tarballs are kept (Go's `cache.StorageBackend`).
pub trait Storage: Send + Sync {
    /// Whether `key` exists.
    fn exists(&self, key: &str) -> Result<bool, String>;
    /// Reads `key`.
    fn get(&self, key: &str) -> Result<Body, String>;
    /// Writes `data` to `key`.
    fn put(&self, key: &str, data: Vec<u8>) -> Result<(), String>;
}

/// Provider tarballs under `<prefix>/<key>` in a GCS bucket (Go's `GCSStorage`).
pub struct GcsStorage {
    bucket: String,
    prefix: String,
    base_url: String,
    token: TokenFn,
    http: reqwest::blocking::Client,
}

impl GcsStorage {
    /// Storage against [`crate::gcs_base_url`] (`$TFSTACKPLAN_GCS_URL`, else GCS).
    pub fn new(token: TokenFn, bucket: &str, prefix: &str) -> Result<Self, String> {
        Self::with_base_url(token, bucket, prefix, &crate::gcs_base_url())
    }

    /// Storage against an explicit base URL (tests).
    pub fn with_base_url(
        token: TokenFn,
        bucket: &str,
        prefix: &str,
        base_url: &str,
    ) -> Result<Self, String> {
        Ok(Self {
            bucket: bucket.to_string(),
            prefix: prefix.to_string(),
            base_url: base_url.trim_end_matches('/').to_string(),
            token,
            http: crate::http_client(Duration::from_secs(60))?,
        })
    }

    /// The base URL requests go to.
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

    fn object_url(&self, key: &str) -> String {
        format!(
            "{}/storage/v1/b/{}/o/{}",
            self.base_url,
            path_escape(&self.bucket),
            query_escape(&self.name(key))
        )
    }
}

fn transport(e: &reqwest::Error) -> String {
    crate::server::transport(e)
}

impl Storage for GcsStorage {
    fn exists(&self, key: &str) -> Result<bool, String> {
        let tok = (self.token)()?;
        let resp = self
            .http
            .get(self.object_url(key))
            .bearer_auth(tok)
            .send()
            .map_err(|e| transport(&e))?;
        let status = resp.status().as_u16();
        if status == 404 {
            return Ok(false);
        }
        if status / 100 != 2 {
            return Err(format!("gcs status: {status}"));
        }
        Ok(true)
    }

    fn get(&self, key: &str) -> Result<Body, String> {
        let tok = (self.token)()?;
        let resp = self
            .http
            .get(format!("{}?alt=media", self.object_url(key)))
            .bearer_auth(tok)
            .send()
            .map_err(|e| transport(&e))?;
        let status = resp.status().as_u16();
        if status / 100 != 2 {
            return Err(format!("gcs get status: {status}"));
        }
        Ok(Box::new(resp))
    }

    fn put(&self, key: &str, data: Vec<u8>) -> Result<(), String> {
        let tok = (self.token)()?;
        let url = format!(
            "{}/upload/storage/v1/b/{}/o?uploadType=media&name={}",
            self.base_url,
            path_escape(&self.bucket),
            query_escape(&self.name(key))
        );
        let resp = self
            .http
            .post(url)
            .bearer_auth(tok)
            .header("Content-Type", "application/octet-stream")
            .body(data)
            .send()
            .map_err(|e| transport(&e))?;
        let status = resp.status().as_u16();
        if status / 100 != 2 {
            return Err(format!("gcs put status: {status}"));
        }
        Ok(())
    }
}

/// Downloads provider zips from a terraform registry (Go's `RegistryClient`).
pub struct RegistryClient {
    host: String,
    base_url: String,
    http: reqwest::blocking::Client,
}

impl RegistryClient {
    /// The public registry, `https://registry.terraform.io`.
    pub fn new() -> Result<Self, String> {
        Self::with_base_url("registry.terraform.io", "https://registry.terraform.io")
    }

    /// A registry for `host` served at `base_url` (tests point it at a fake).
    pub fn with_base_url(host: &str, base_url: &str) -> Result<Self, String> {
        Ok(Self {
            host: host.to_string(),
            base_url: base_url.trim_end_matches('/').to_string(),
            http: crate::http_client(Duration::from_secs(30))?,
        })
    }

    /// The provider zip's download URL for this platform (Go's `GetDownloadURL`).
    pub fn download_url(&self, p: &Provider) -> Result<String, String> {
        let url = download_api_url(&self.host, &self.base_url, p, goos(), goarch())?;
        let resp = self.http.get(url).send().map_err(|e| transport(&e))?;
        let status = resp.status().as_u16();
        if status / 100 != 2 {
            return Err(format!("registry status: {status}"));
        }
        let body = resp.bytes().map_err(|e| transport(&e))?;
        decode_download_url(&body)
    }

    /// The provider zip and its length when known (Go's `Fetch`).
    pub fn fetch(&self, p: &Provider) -> Result<(Body, Option<u64>), String> {
        let url = self.download_url(p)?;
        let resp = self.http.get(url).send().map_err(|e| transport(&e))?;
        let status = resp.status().as_u16();
        if status / 100 != 2 {
            return Err(format!("registry fetch download status: {status}"));
        }
        let len = resp.content_length();
        Ok((Box::new(resp), len))
    }
}

#[cfg(test)]
mod tests;
