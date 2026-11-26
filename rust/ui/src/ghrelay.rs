//! The GitHub App webhook relay (Go: `internal/ui/ghrelay.go`). A GitHub App has one
//! webhook URL while there may be several tier serves, so the relay forwards each delivery
//! verbatim, signature headers included, to every tier's `/github/webhook`, and each serve
//! verifies GitHub's HMAC itself. A compromised relay cannot forge events. With a webhook
//! secret configured the relay also verifies before fanning out, so garbage dies here as a
//! 401 in the App's delivery log.

use std::sync::Arc;

use axum::body::Body;
use axum::http::{HeaderMap, Request};
use futures_util::future::join_all;
use tfstackplan_server_clients::github::verify_github_signature;
use tracing::warn;

use crate::app::{App, Tier};
use crate::gohttp::{self, Resp};

/// The delivery headers the serves need, signatures included.
const RELAY_HEADERS: [&str; 5] = [
    "Content-Type",
    "X-GitHub-Event",
    "X-GitHub-Delivery",
    "X-Hub-Signature-256",
    "X-Hub-Signature",
];

/// GitHub caps payloads at 25 MB; ours are far smaller.
const RELAY_BODY_LIMIT: usize = 2 << 20;

impl App {
    /// `POST /github/webhook` (public: authenticity is GitHub's HMAC). 202 when at least
    /// one tier accepted the delivery; 502 when none did, so the failure shows in the App's
    /// delivery log for redelivery.
    pub(crate) async fn handle_github_relay(self: &Arc<Self>, req: Request<Body>) -> Resp {
        let (parts, body) = req.into_parts();
        let Ok(body) = axum::body::to_bytes(body, RELAY_BODY_LIMIT).await else {
            warn!("ui: /github/webhook: body over the {RELAY_BODY_LIMIT}-byte limit, refused");
            return gohttp::error(413, "payload too large");
        };
        if !self.cfg.github_webhook_secret.is_empty() {
            let sig = parts
                .headers
                .get("X-Hub-Signature-256")
                .and_then(|v| v.to_str().ok())
                .unwrap_or("");
            if !verify_github_signature(self.cfg.github_webhook_secret.as_bytes(), sig, &body) {
                return gohttp::error(401, "invalid signature");
            }
        }
        let oks = join_all(
            self.cfg
                .tiers
                .iter()
                .map(|t| relay_to(&self.clients.api, t, &parts.headers, body.to_vec())),
        )
        .await;
        if oks.into_iter().any(|ok| ok) {
            return gohttp::status(202, Body::empty());
        }
        gohttp::error(502, "no tier accepted the delivery")
    }
}

async fn relay_to(hc: &reqwest::Client, tier: &Tier, hdr: &HeaderMap, body: Vec<u8>) -> bool {
    let mut req = hc.post(format!("{}/github/webhook", tier.url)).body(body);
    for h in RELAY_HEADERS {
        if let Some(v) = hdr.get(h).filter(|v| !v.is_empty()) {
            req = req.header(h, v.clone());
        }
    }
    let resp = match req.send().await {
        Ok(r) => r,
        Err(e) => {
            warn!("ui: github relay to {}: {}", tier.name, e.without_url());
            return false;
        }
    };
    let status = resp.status().as_u16();
    // Drain the body so the connection can be reused.
    let _ = resp.bytes().await;
    if status / 100 != 2 {
        warn!("ui: github relay to {}: {status}", tier.name);
        return false;
    }
    true
}

#[cfg(test)]
mod tests;
