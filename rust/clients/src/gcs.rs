//! The GCS JSON API calls the CLI makes: terraform's `.tflock` object as a
//! fail-fast pessimistic lock (`state apply --lock`), and a small object store.

use std::time::Duration;

use tfstackplan_cli::gourl::{path_escape, query_escape};

use crate::TokenFn;

/// Terraform's GCS backend lock object payload.
#[derive(Clone, Debug, serde::Serialize)]
pub struct LockInfo {
    /// Random hex id.
    #[serde(rename = "ID")]
    pub id: String,
    /// What holds the lock.
    #[serde(rename = "Operation")]
    pub operation: String,
    /// `user@host`.
    #[serde(rename = "Who")]
    pub who: String,
    /// Tool version.
    #[serde(rename = "Version")]
    pub version: String,
    /// RFC 3339 UTC time with nanoseconds.
    #[serde(rename = "Created")]
    pub created: String,
    /// `gs://bucket/object`.
    #[serde(rename = "Path")]
    pub path: String,
}

/// Acquires and releases `<prefix>/default.tflock` in a state bucket.
pub struct Locker {
    token: TokenFn,
    base_url: String,
    http: reqwest::blocking::Client,
}

/// A held lock. Release it with [`Lock::release`].
pub struct Lock<'a> {
    locker: &'a Locker,
    bucket: String,
    object: String,
}

impl Locker {
    /// A locker against `base_url` (empty: [`crate::gcs_base_url`]).
    pub fn new(token: TokenFn, base_url: &str) -> Result<Self, String> {
        let base_url = if base_url.is_empty() {
            crate::gcs_base_url()
        } else {
            base_url.to_string()
        };
        Ok(Self {
            token,
            base_url,
            http: crate::http_client(Duration::from_secs(60))?,
        })
    }

    /// Creates the lock object with `ifGenerationMatch=0`. A 412 means another
    /// holder: the error names `stack_dir` and the object.
    pub fn acquire(
        &self,
        stack_dir: &str,
        bucket: &str,
        prefix: &str,
        info: &LockInfo,
    ) -> Result<Lock<'_>, String> {
        let object = format!("{prefix}/default.tflock");
        let body = serde_json::to_vec(info).map_err(|e| format!("marshal lock info: {e}"))?;
        let token = (self.token)().map_err(|e| format!("auth token: {e}"))?;
        let url = format!(
            "{}/upload/storage/v1/b/{}/o?uploadType=media&name={}&ifGenerationMatch=0",
            self.base_url,
            bucket,
            query_escape(&object)
        );
        let resp = self
            .http
            .post(url)
            .bearer_auth(token)
            .header("Content-Type", "application/json")
            .body(body)
            .send()
            .map_err(|e| format!("acquire lock: {e}"))?;
        let status = resp.status().as_u16();
        if status == 412 {
            return Err(format!(
                "state {stack_dir} is already locked (gs://{bucket}/{object})"
            ));
        }
        if !(200..300).contains(&status) {
            let msg = read_limited(resp, 4096);
            return Err(format!(
                "acquire lock: unexpected status {status}: {}",
                msg.trim()
            ));
        }
        Ok(Lock {
            locker: self,
            bucket: bucket.to_string(),
            object,
        })
    }
}

impl Lock<'_> {
    /// The object this lock holds.
    pub fn object(&self) -> &str {
        &self.object
    }

    /// Deletes the lock object. Any HTTP status counts as released, as in Go.
    pub fn release(self) -> Result<(), String> {
        let l = self.locker;
        let token = (l.token)().map_err(|e| format!("auth token: {e}"))?;
        let url = format!(
            "{}/storage/v1/b/{}/o/{}",
            l.base_url,
            self.bucket,
            query_escape(&self.object)
        );
        l.http
            .delete(url)
            .bearer_auth(token)
            .send()
            .map_err(|e| format!("release lock: {e}"))?;
        Ok(())
    }
}

fn read_limited(resp: reqwest::blocking::Response, limit: usize) -> String {
    let text = resp.text().unwrap_or_default();
    let mut end = text.len().min(limit);
    while !text.is_char_boundary(end) {
        end -= 1;
    }
    text[..end].to_string()
}

/// Objects under `<prefix>/<key>` in one bucket (Go's `gcsObjectStore`).
pub struct ObjectStore {
    token: TokenFn,
    bucket: String,
    prefix: String,
    base_url: String,
    http: reqwest::blocking::Client,
}

impl ObjectStore {
    /// A store against `base_url` (empty: [`crate::gcs_base_url`]); a trailing
    /// slash is trimmed.
    pub fn new(token: TokenFn, bucket: &str, prefix: &str, base_url: &str) -> Result<Self, String> {
        let base_url = if base_url.is_empty() {
            crate::gcs_base_url()
        } else {
            base_url.trim_end_matches('/').to_string()
        };
        Ok(Self {
            token,
            bucket: bucket.to_string(),
            prefix: prefix.to_string(),
            base_url,
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

    /// Uploads `data` to `key`.
    pub fn put(&self, key: &str, data: Vec<u8>) -> Result<(), String> {
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
            .map_err(|e| e.to_string())?;
        let status = resp.status().as_u16();
        if status / 100 != 2 {
            let b = resp.text().unwrap_or_default();
            return Err(format!("gcs put {}: {status}: {b}", self.name(key)));
        }
        Ok(())
    }

    /// Downloads `key`.
    pub fn get(&self, key: &str) -> Result<Vec<u8>, String> {
        let tok = (self.token)()?;
        let url = format!(
            "{}/storage/v1/b/{}/o/{}?alt=media",
            self.base_url,
            path_escape(&self.bucket),
            query_escape(&self.name(key))
        );
        let resp = self
            .http
            .get(url)
            .bearer_auth(tok)
            .send()
            .map_err(|e| e.to_string())?;
        let status = resp.status().as_u16();
        if status / 100 != 2 {
            return Err(format!("gcs get {}: {status}", self.name(key)));
        }
        resp.bytes().map(|b| b.to_vec()).map_err(|e| e.to_string())
    }
}

#[cfg(test)]
mod tests;
