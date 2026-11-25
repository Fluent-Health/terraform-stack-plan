use std::collections::BTreeMap;
use std::sync::{Arc, Mutex};

use aws_lc_rs::hmac;
use axum::body::Body;
use axum::http::Request;
use tfstackplan_server_clients::testserver::{Resp, TestServer, serve};

use crate::app::{App, Config, Tier};
use crate::testutil::{dead_url, send, session_app, tier};

#[derive(Debug, Clone, Default)]
struct Seen {
    body: String,
    event: String,
    bearer: String,
    sig: String,
}

type Got = Arc<Mutex<BTreeMap<String, Seen>>>;

fn sign(secret: &str, body: &str) -> String {
    let key = hmac::Key::new(hmac::HMAC_SHA256, secret.as_bytes());
    format!(
        "sha256={}",
        hex_encode(hmac::sign(&key, body.as_bytes()).as_ref())
    )
}

fn hex_encode(b: &[u8]) -> String {
    b.iter().map(|x| format!("{x:02x}")).collect()
}

async fn tier_server(name: &str, got: &Got) -> TestServer {
    let (name, got) = (name.to_string(), Arc::clone(got));
    serve(move |r| {
        if r.path != "/github/webhook" {
            return Resp::status(418, format!("wrong path {}", r.path));
        }
        got.lock().expect("lock").insert(
            name.clone(),
            Seen {
                body: String::from_utf8_lossy(&r.body).into_owned(),
                event: r.header("x-github-event").into(),
                bearer: r.header("authorization").into(),
                sig: r.header("x-hub-signature-256").into(),
            },
        );
        Resp::status(204, "")
    })
    .await
    .expect("tier")
}

fn relay_app(tiers: Vec<Tier>) -> Arc<App> {
    App::new(Config {
        session_secret: "s3cret".into(),
        github_webhook_secret: "gh-app-secret".into(),
        tiers,
        ..Config::default()
    })
    .expect("app")
}

fn delivery(body: &str, sig: &str, event: Option<&str>) -> Request<Body> {
    let mut b = Request::builder()
        .method("POST")
        .uri("/github/webhook")
        .header("x-hub-signature-256", sig);
    if let Some(e) = event {
        b = b.header("x-github-event", e);
    }
    b.body(Body::from(body.to_string())).expect("request")
}

// Go: TestGitHubRelay.
#[tokio::test]
async fn github_relay() {
    let got: Got = Arc::default();
    let t1 = tier_server("nonprod", &got).await;
    let t2 = tier_server("prod", &got).await;
    let dead = dead_url().await;
    let h = relay_app(vec![tier("nonprod", &t1.url), tier("prod", &t2.url)]);

    let body = r#"{"action":"rerequested"}"#;
    let sig = sign("gh-app-secret", body);
    let rr = send(&h, delivery(body, &sig, Some("check_run"))).await;
    assert_eq!(rr.status, 202, "{}", rr.body);
    for name in ["nonprod", "prod"] {
        let s = got
            .lock()
            .expect("lock")
            .get(name)
            .cloned()
            .unwrap_or_default();
        assert_eq!(
            (s.body.as_str(), s.event.as_str()),
            (body, "check_run"),
            "{name}"
        );
        // A verbatim pipe: GitHub's signature travels on, no relay-minted credentials.
        assert_eq!(
            (s.sig.as_str(), s.bearer.as_str()),
            (sig.as_str(), ""),
            "{name}"
        );
    }

    // With a secret, a bad signature dies at the relay and nothing is forwarded.
    got.lock().expect("lock").clear();
    let rr = send(&h, delivery(body, "sha256=deadbeef", None)).await;
    assert_eq!((rr.status, rr.body.as_str()), (401, "invalid signature\n"));
    assert!(got.lock().expect("lock").is_empty());

    // Without a secret the relay forwards blindly (the serves reject bad signatures).
    let (h_off, _) = session_app(vec![tier("nonprod", &t1.url)]);
    let rr = send(&h_off, delivery(body, "sha256=deadbeef", None)).await;
    assert_eq!(rr.status, 202);
    assert_eq!(got.lock().expect("lock").len(), 1);

    // One tier down still 202; all down 502.
    let h2 = relay_app(vec![tier("up", &t1.url), tier("down", &dead)]);
    assert_eq!(
        send(&h2, delivery(body, &sign("gh-app-secret", body), None))
            .await
            .status,
        202
    );
    let h3 = relay_app(vec![tier("down", &dead)]);
    let rr = send(&h3, delivery(body, &sign("gh-app-secret", body), None)).await;
    assert_eq!(
        (rr.status, rr.body.as_str()),
        (502, "no tier accepted the delivery\n")
    );
}

#[tokio::test]
async fn oversized_delivery_is_413() {
    let h = relay_app(vec![tier("t", "http://127.0.0.1:9")]);
    let big = "x".repeat((2 << 20) + 1);
    let rr = send(&h, delivery(&big, "sha256=00", None)).await;
    assert_eq!((rr.status, rr.body.as_str()), (413, "payload too large\n"));
}
