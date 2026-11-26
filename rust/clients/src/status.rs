//! The reads `run status` makes: one execution, and its server-sent event stream.

use std::io::{BufRead, BufReader};
use std::time::Duration;

use crate::TokenFn;

/// A read-only client for `/api/execution/*`.
pub struct StatusClient {
    base_url: String,
    token: Option<TokenFn>,
    http: reqwest::blocking::Client,
    stream: reqwest::blocking::Client,
}

impl StatusClient {
    /// A client for `base_url` (no trailing slash).
    pub fn new(base_url: &str, token: Option<TokenFn>) -> Result<Self, String> {
        Ok(Self {
            base_url: base_url.to_string(),
            token,
            http: crate::http_client(Duration::from_secs(10))?,
            // The event stream stays open while the execution runs; bound it by
            // the longest run we expect rather than leaving it unbounded.
            stream: crate::http_client(Duration::from_secs(6 * 60 * 60))?,
        })
    }

    fn get(
        &self,
        http: &reqwest::blocking::Client,
        path: &str,
    ) -> Result<reqwest::blocking::Response, String> {
        let mut req = http.get(format!("{}{path}", self.base_url));
        if let Some(tok) = &self.token {
            let t = tok().map_err(|e| format!("api token: {e}"))?;
            req = req.bearer_auth(t);
        }
        req.send().map_err(|e| crate::server::transport(&e))
    }

    /// `GET /api/execution/<id>`: the body on 200, else `http <status>`.
    pub fn execution(&self, id: &str) -> Result<Vec<u8>, String> {
        let resp = self.get(&self.http, &format!("/api/execution/{id}"))?;
        let status = resp.status().as_u16();
        if status != 200 {
            return Err(format!("http {status}"));
        }
        resp.bytes().map(|b| b.to_vec()).map_err(|e| e.to_string())
    }

    /// Reads `GET /api/execution/<id>/events` line by line until the stream ends
    /// or `on_line` returns `false`.
    pub fn events(&self, id: &str, mut on_line: impl FnMut(&str) -> bool) -> Result<(), String> {
        let resp = self.get(&self.stream, &format!("/api/execution/{id}/events"))?;
        let status = resp.status().as_u16();
        if status != 200 {
            return Err(format!("events stream http {status}"));
        }
        for line in BufReader::new(resp).lines() {
            let line = line.map_err(|e| e.to_string())?;
            if !on_line(&line) {
                break;
            }
        }
        Ok(())
    }
}
