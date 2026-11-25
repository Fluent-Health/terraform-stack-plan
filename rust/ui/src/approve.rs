//! In-UI PAM approve and deny (Go: `internal/ui/approve.go`), the incremental-consent popup
//! flow. Stateless: the intent (tier, grant, decision, reason) rides the OAuth `state`
//! parameter sealed with the session codec and bound to the session's email. The callback
//! exchanges the code, spends the user's short-lived access token on the one PAM call and
//! drops it. No user credential is stored; GCP enforces who may approve (a 403 is shown
//! verbatim) and PAM's audit log records the human.

use std::sync::Arc;
use std::time::Duration;

use axum::http::Method;
use jiff::{SignedDuration, Timestamp};
use serde::{Deserialize, Serialize};
use tfstackplan_server_clients::approval::gcppam::{self, DecideError};
use tracing::{info, warn};

use crate::app::App;
use crate::gohttp::{self, Resp};
use crate::oauth::OAuth;
use crate::session::Session;

/// The scope PAM requires; there is no narrower one.
pub(crate) const CLOUD_PLATFORM_SCOPE: &str = "https://www.googleapis.com/auth/cloud-platform";

/// The sealed OAuth state payload (Go's `approveState`, same JSON).
#[derive(Debug, Serialize, Deserialize)]
struct ApproveState {
    /// Must match the callback's session.
    #[serde(default)]
    email: String,
    #[serde(default)]
    tier: String,
    #[serde(default)]
    grant: String,
    #[serde(default)]
    approve: bool,
    #[serde(default)]
    reason: String,
    expires: Timestamp,
}

impl App {
    /// The login client re-targeted at the consent callback with the cloud-platform scope.
    fn consent_oauth(&self, base: &OAuth) -> OAuth {
        let mut c = base.clone();
        c.redirect_url = format!("{}/auth/approve/callback", self.cfg.public_base_url);
        c.scopes = vec![CLOUD_PLATFORM_SCOPE.into()];
        c
    }

    /// `GET /auth/approve?tier=..&grant=..&decision=approve|deny&reason=..`.
    pub(crate) fn handle_approve_start(
        self: &Arc<Self>,
        sess: &Session,
        q: &str,
        method: &Method,
    ) -> Resp {
        let Some(oauth) = self
            .cfg
            .oauth
            .as_ref()
            .filter(|_| !self.cfg.public_base_url.is_empty())
        else {
            return gohttp::error(
                501,
                "approvals are not configured (oauth + public_base_url required)",
            );
        };
        let tier = gohttp::query_get(q, "tier");
        let grant = gohttp::query_get(q, "grant");
        let decision = gohttp::query_get(q, "decision");
        let reason = gohttp::query_get(q, "reason");
        let Some(tier) = self.tier(&tier) else {
            return gohttp::error(404, "unknown tier");
        };
        if decision != b"approve" && decision != b"deny" {
            return gohttp::error(400, "decision must be approve or deny");
        }
        let grant_str = String::from_utf8_lossy(&grant).into_owned();
        if gcppam::validate_grant_name(&grant_str).is_err() {
            return gohttp::error(
                400,
                &format!(
                    "not a PAM grant resource name: {}",
                    gohttp::go_quote(&grant)
                ),
            );
        }
        if reason.len() > 512 {
            return gohttp::error(400, "reason too long");
        }
        let decision = String::from_utf8_lossy(&decision).into_owned();
        // PAM rejects a decision without a reason; an empty submit still succeeds.
        let reason = if reason.is_empty() {
            format!("{decision}d via tfstackplan UI")
        } else {
            String::from_utf8_lossy(&reason).into_owned()
        };
        let sealed = match self.codec.seal_json(&ApproveState {
            email: sess.email.clone(),
            tier: tier.name.clone(),
            grant: grant_str,
            approve: decision == "approve",
            reason,
            expires: Timestamp::now() + SignedDuration::from_mins(2),
        }) {
            Ok(s) => s,
            Err(_) => return gohttp::error(500, "internal error"),
        };
        // include_granted_scopes makes the consent incremental: after the first time the
        // popup round-trips silently.
        let url = self
            .consent_oauth(oauth)
            .auth_code_url(&sealed, &[("include_granted_scopes", "true")]);
        gohttp::redirect(method, &url, 302)
    }

    /// `GET /auth/approve/callback`: check the sealed intent, exchange the code, make the
    /// PAM call as the user, and report to the opener.
    pub(crate) async fn handle_approve_callback(self: &Arc<Self>, sess: &Session, q: &str) -> Resp {
        let Some(oauth) = &self.cfg.oauth else {
            return gohttp::error(501, "approvals are not configured");
        };
        let state = gohttp::query_get_str(q, "state");
        let Ok(st) = self.codec.open_json::<ApproveState>(&state) else {
            return approve_result(
                false,
                "approval request invalid \u{2014} close this window and retry",
            );
        };
        if Timestamp::now() > st.expires {
            return approve_result(
                false,
                "approval request expired \u{2014} close this window and retry",
            );
        }
        if st.email != sess.email {
            return approve_result(false, "approval request was started by a different session");
        }
        let err = gohttp::query_get_str(q, "error");
        if !err.is_empty() {
            // The user cancelled the consent screen, or Google refused it.
            return approve_result(false, &format!("consent not granted: {err}"));
        }
        let code = gohttp::query_get_str(q, "code");
        let tok = match self
            .consent_oauth(oauth)
            .exchange(&self.clients.oauth, &code)
            .await
        {
            Ok(t) => t,
            Err(e) => {
                warn!("ui: approve code exchange failed: {e}");
                return approve_result(false, "code exchange with Google failed");
            }
        };
        let verb = if st.approve { "approved" } else { "denied" };
        let res = gcppam::decide_grant(
            &self.cfg.pam_base_url,
            &self.cfg.quota_project,
            &tok.access_token,
            &st.grant,
            st.approve,
            &st.reason,
        )
        .await;
        drop(tok);
        if let Err(e) = res {
            // PAM's own rejection travels verbatim to the user; the log carries only the
            // status, never PAM's body.
            match &e {
                DecideError::Rejected { status, .. } => {
                    warn!(
                        "ui: pam decide ({verb} {} by {}): HTTP {status}",
                        st.grant, st.email
                    );
                }
                DecideError::Other(m) => {
                    warn!("ui: pam decide ({verb} {} by {}): {m}", st.grant, st.email);
                }
            }
            return approve_result(false, &e.to_string());
        }
        info!("ui: grant {} {verb} by {}", st.grant, st.email);
        // Nudge the tier to reconcile its gates now so the decision reaches watching pages
        // within seconds; the tier's poll loop is the backstop.
        self.nudge_gate_reconcile(&st.tier);
        approve_result(true, &format!("grant {verb}"))
    }

    /// Fires the tier's `POST /api/gate/reconcile` with the ui's own identity, in the
    /// background with a 10 s deadline. Failures only log (an old serve 404s).
    fn nudge_gate_reconcile(self: &Arc<Self>, tier: &str) {
        let Some(tier) = self.tier(tier.as_bytes()).cloned() else {
            return;
        };
        let hc = self.clients.api.clone();
        tokio::spawn(async move {
            let url = if tier.url.ends_with('/') {
                format!("{}api/gate/reconcile", tier.url)
            } else {
                format!("{}/api/gate/reconcile", tier.url)
            };
            let mut req = hc.post(&url).timeout(Duration::from_secs(10));
            if let Some(t) = &tier.token {
                match t.token().await {
                    Ok(b) => req = req.bearer_auth(b),
                    Err(e) => {
                        warn!("ui: gate-reconcile nudge {}: {e}", tier.name);
                        return;
                    }
                }
            }
            match req.send().await {
                Err(e) => warn!(
                    "ui: gate-reconcile nudge {}: {}",
                    tier.name,
                    e.without_url()
                ),
                Ok(r) if r.status().as_u16() / 100 != 2 => {
                    warn!(
                        "ui: gate-reconcile nudge {}: HTTP {}",
                        tier.name,
                        r.status().as_u16()
                    );
                }
                Ok(_) => {}
            }
        });
    }
}

/// The result payload, keys in Go's sorted map order.
#[derive(Serialize)]
struct Payload<'a> {
    message: &'a str,
    ok: bool,
    #[serde(rename = "type")]
    kind: &'a str,
}

/// The popup page (Go's `approveResultTmpl`): reports to the opener and closes, or just
/// shows the message. The payload is Go-escaped JSON, safe in a script even with an
/// attacker-influenced PAM error body; the message is HTML-escaped.
fn approve_result(ok: bool, message: &str) -> Resp {
    let payload = gohttp::go_json(&Payload {
        message,
        ok,
        kind: "tfsp-approval",
    });
    gohttp::html(format!(
        "<!doctype html>\n<html><body style=\"font-family: system-ui; padding: 2em; text-align: center\">\n<p>{}</p>\n<script>\n  var payload = {payload};\n  if (window.opener) {{\n    window.opener.postMessage(payload, window.location.origin);\n    window.close();\n  }}\n</script>\n</body></html>",
        gohttp::html_escape(message)
    ))
}

#[cfg(test)]
mod tests;
