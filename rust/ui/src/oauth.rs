//! The OAuth 2.0 authorization-code client, the slice of `golang.org/x/oauth2` the ui uses:
//! `AuthCodeURL` and `Exchange` with the client credentials in the form body (Google's
//! endpoint style). Errors name the endpoint and the status, never a body: a token
//! endpoint's error body can echo the code.

use serde_json::{Map, Value};

use crate::gohttp::{self, Mode};

/// Google's authorization endpoint.
pub const GOOGLE_AUTH_URL: &str = "https://accounts.google.com/o/oauth2/auth";
/// Google's token endpoint.
pub const GOOGLE_TOKEN_URL: &str = "https://oauth2.googleapis.com/token";

/// An authorization-code client (Go's `oauth2.Config`).
#[derive(Clone)]
pub struct OAuth {
    pub client_id: String,
    pub client_secret: String,
    pub auth_url: String,
    pub token_url: String,
    pub redirect_url: String,
    pub scopes: Vec<String>,
}

impl std::fmt::Debug for OAuth {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("OAuth")
            .field("client_id", &self.client_id)
            .field("auth_url", &self.auth_url)
            .field("token_url", &self.token_url)
            .field("redirect_url", &self.redirect_url)
            .field("scopes", &self.scopes)
            .finish_non_exhaustive()
    }
}

/// A token endpoint's answer.
#[derive(Debug)]
pub struct Token {
    pub access_token: String,
    /// The whole response, for `Extra("id_token")`.
    pub raw: Map<String, Value>,
}

impl OAuth {
    /// Go's `AuthCodeURL(state, extra...)`: the parameters sorted by key, as
    /// `url.Values.Encode` writes them.
    pub fn auth_code_url(&self, state: &str, extra: &[(&str, &str)]) -> String {
        let mut v: Vec<(String, String)> = vec![
            ("response_type".into(), "code".into()),
            ("client_id".into(), self.client_id.clone()),
        ];
        if !self.redirect_url.is_empty() {
            v.push(("redirect_uri".into(), self.redirect_url.clone()));
        }
        if !self.scopes.is_empty() {
            v.push(("scope".into(), self.scopes.join(" ")));
        }
        if !state.is_empty() {
            v.push(("state".into(), state.into()));
        }
        for (k, val) in extra {
            v.retain(|(key, _)| key != k);
            v.push(((*k).into(), (*val).into()));
        }
        v.sort_by(|a, b| a.0.cmp(&b.0));
        let enc: Vec<String> = v
            .iter()
            .map(|(k, val)| {
                format!(
                    "{}={}",
                    gohttp::escape(k.as_bytes(), Mode::QueryComponent),
                    gohttp::escape(val.as_bytes(), Mode::QueryComponent)
                )
            })
            .collect();
        let sep = if self.auth_url.contains('?') {
            '&'
        } else {
            '?'
        };
        format!("{}{sep}{}", self.auth_url, enc.join("&"))
    }

    /// Go's `Exchange(ctx, code)`: the authorization-code grant.
    pub async fn exchange(&self, hc: &reqwest::Client, code: &str) -> Result<Token, String> {
        let mut form = vec![
            ("client_id", self.client_id.as_str()),
            ("code", code),
            ("grant_type", "authorization_code"),
        ];
        if !self.redirect_url.is_empty() {
            form.push(("redirect_uri", &self.redirect_url));
        }
        if !self.client_secret.is_empty() {
            form.push(("client_secret", &self.client_secret));
        }
        let resp = hc
            .post(&self.token_url)
            .form(&form)
            .send()
            .await
            .map_err(|e| format!("oauth2: token request: {}", e.without_url()))?;
        let status = resp.status().as_u16();
        let path = tfstackplan_server_clients::http::endpoint_path(&self.token_url).to_string();
        if status / 100 != 2 {
            tfstackplan_server_clients::http::log_non_2xx(
                "oauth",
                &reqwest::Method::POST,
                &self.token_url,
                status,
            );
            return Err(format!("oauth2: cannot fetch token: {status} from {path}"));
        }
        let ct = resp
            .headers()
            .get(reqwest::header::CONTENT_TYPE)
            .and_then(|v| v.to_str().ok())
            .unwrap_or("")
            .to_string();
        let body = resp
            .bytes()
            .await
            .map_err(|e| format!("oauth2: cannot fetch token: {}", e.without_url()))?;
        let body = &body[..body.len().min(1 << 20)];
        let mime = ct.split(';').next().unwrap_or("").trim();
        let raw: Map<String, Value> =
            if mime == "application/x-www-form-urlencoded" || mime == "text/plain" {
                gohttp::parse_query(&String::from_utf8_lossy(body))
                    .into_iter()
                    .map(|(k, v)| {
                        (
                            String::from_utf8_lossy(&k).into_owned(),
                            Value::String(String::from_utf8_lossy(&v).into_owned()),
                        )
                    })
                    .rev()
                    .collect()
            } else {
                serde_json::from_slice(body)
                    .map_err(|e| format!("oauth2: cannot parse json token response: {e}"))?
            };
        let access_token = raw
            .get("access_token")
            .and_then(Value::as_str)
            .unwrap_or("")
            .to_string();
        if access_token.is_empty() {
            return Err("oauth2: server response missing access_token".into());
        }
        Ok(Token { access_token, raw })
    }
}

#[cfg(test)]
mod tests;
