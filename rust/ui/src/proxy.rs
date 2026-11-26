//! The per-tier proxies (Go: `internal/ui/proxy.go` and `streams.go`). Contract reads call
//! the tier's own route with an OIDC token minted for that tier's audience and copy the
//! status, content type and body through untouched. The stream routes (execution events,
//! logs, plan fragments) do the same with Last-Event-ID and Accept forwarded and every
//! chunk passed on as it arrives. One tier down is a 502 naming it; others are unaffected.
//!
//! Upstream 4xx and 5xx are logged by tier, status and endpoint path. Bodies, queries and
//! tokens never reach a log.

use std::sync::Arc;

use axum::body::Body;
use axum::http::{HeaderMap, HeaderValue, header};
use futures_util::stream;
use tracing::warn;

use crate::app::{App, Tier};
use crate::gohttp::{self, Resp};

/// A request with the tier's bearer, or the mint failure.
async fn bearer(tier: &Tier) -> Result<Option<String>, String> {
    match &tier.token {
        None => Ok(None),
        Some(t) => t.token().await.map(Some).map_err(|e| e.to_string()),
    }
}

/// `<tier url>/<rel>`, as the generated client resolves `./<rel>` against the URL with a
/// trailing slash.
fn api_url(tier: &Tier, rel: &str) -> String {
    if tier.url.ends_with('/') {
        format!("{}{rel}", tier.url)
    } else {
        format!("{}/{rel}", tier.url)
    }
}

fn log_upstream(tier: &str, url: &str, status: u16) {
    if status >= 400 {
        warn!(
            tier,
            status,
            path = tfstackplan_server_clients::http::endpoint_path(url),
            "ui: tier answered with an error"
        );
    }
}

impl App {
    /// A contract read (Go's typed-request proxies plus `relay`).
    pub(crate) async fn relay(self: &Arc<Self>, tier_name: &[u8], rel: &str) -> Resp {
        let Some(tier) = self.tier(tier_name) else {
            return gohttp::error(404, "unknown tier");
        };
        let unreachable = || gohttp::error(502, &format!("tier {} unreachable", tier.name));
        let url = api_url(tier, rel);
        let mut req = self.clients.api.get(&url);
        match bearer(tier).await {
            Ok(Some(b)) => req = req.bearer_auth(b),
            Ok(None) => {}
            Err(_) => return unreachable(),
        }
        let mut resp = match req.send().await {
            Ok(r) => r,
            Err(_) => return unreachable(),
        };
        let status = resp.status().as_u16();
        log_upstream(&tier.name, &url, status);
        let ct = resp.headers().get(header::CONTENT_TYPE).cloned();
        let mut body = Vec::new();
        // A body cut short still sends what arrived, as Go's io.Copy after WriteHeader.
        while let Ok(Some(chunk)) = resp.chunk().await {
            body.extend_from_slice(&chunk);
        }
        let ct = match ct {
            Some(ct) if !ct.is_empty() => Some(ct),
            // Go sniffs the first write when no content type was set.
            _ if !body.is_empty() => Some(HeaderValue::from_static(gohttp::sniff(&body))),
            _ => None,
        };
        let mut r = gohttp::status(status, body);
        if let Some(ct) = ct {
            r.headers_mut().insert(header::CONTENT_TYPE, ct);
        }
        r
    }

    /// A stream route (Go's `proxyStream`): `path` and the raw query go to the tier.
    pub(crate) async fn proxy_stream(
        self: &Arc<Self>,
        h: &HeaderMap,
        raw_query: &str,
        tier_name: &[u8],
        path: &str,
    ) -> Resp {
        let Some(tier) = self.tier(tier_name) else {
            return gohttp::error(404, "unknown tier");
        };
        let unreachable = || gohttp::error(502, &format!("tier {} unreachable", tier.name));
        let mut url = format!("{}{path}", tier.url);
        if !raw_query.is_empty() {
            url = format!("{url}?{raw_query}");
        }
        let mut req = self.clients.stream.get(&url);
        for name in ["last-event-id", "accept"] {
            if let Some(v) = h.get(name).filter(|v| !v.is_empty()) {
                req = req.header(name, v.clone());
            }
        }
        match bearer(tier).await {
            Ok(Some(b)) => req = req.bearer_auth(b),
            Ok(None) => {}
            Err(e) => {
                warn!("ui: tier {}: mint stream token: {e}", tier.name);
                return unreachable();
            }
        }
        let resp = match req.send().await {
            Ok(r) => r,
            Err(_) => return unreachable(),
        };
        let status = resp.status().as_u16();
        log_upstream(&tier.name, &url, status);
        let mut copied = HeaderMap::new();
        for name in [header::CONTENT_TYPE, header::CACHE_CONTROL] {
            if let Some(v) = resp.headers().get(&name).filter(|v| !v.is_empty()) {
                copied.insert(name, v.clone());
            }
        }
        // Each chunk goes out as it arrives (hyper flushes every frame). An upstream read
        // error ends the response cleanly, as Go's loop returns.
        let name = tier.name.clone();
        let body = stream::unfold(Some(resp), move |resp| {
            let name = name.clone();
            async move {
                let mut resp = resp?;
                match resp.chunk().await {
                    Ok(Some(c)) => Some((Ok::<_, std::io::Error>(c), Some(resp))),
                    Ok(None) => None,
                    Err(e) => {
                        warn!("ui: stream from tier {name} ended: {}", e.without_url());
                        None
                    }
                }
            }
        });
        let mut r = gohttp::status(status, Body::from_stream(body));
        r.headers_mut().extend(copied);
        r
    }
}

#[cfg(test)]
mod tests;
