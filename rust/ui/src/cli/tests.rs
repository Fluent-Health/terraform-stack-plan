use std::sync::Arc;
use std::time::Duration;

use super::{Shutdown, build_config, parse_flags, serve};
use crate::config::{UiConfig, UiOAuth, UiTier};
use crate::testutil::{session_app, tier};

fn args(a: &[&str]) -> Vec<String> {
    a.iter().map(|s| s.to_string()).collect()
}

#[test]
fn flags_like_go() {
    assert_eq!(
        parse_flags(&[]),
        Ok((".tfstackplan.hcl".into(), ":8081".into()))
    );
    assert_eq!(
        parse_flags(&args(&["-config", "a.hcl", "--addr=:9"])),
        Ok(("a.hcl".into(), ":9".into()))
    );
    assert_eq!(
        parse_flags(&args(&["x", "-config", "a"])),
        Ok((".tfstackplan.hcl".into(), ":8081".into()))
    );
    assert_eq!(parse_flags(&args(&["--help"])), Err(2));
    assert_eq!(parse_flags(&args(&["-nope"])), Err(2));
    assert_eq!(parse_flags(&args(&["-config"])), Err(2));
}

fn ui_cfg() -> UiConfig {
    UiConfig {
        public_base_url: "https://ui.example.com".into(),
        session_secret_env: "S".into(),
        github_webhook_secret_env: String::new(),
        tiers: vec![UiTier {
            name: "a".into(),
            url: "https://a".into(),
            audience: "https://a".into(),
        }],
        oauth: Some(UiOAuth {
            client_id: "c".into(),
            client_secret_env: "O".into(),
            allowed_domain: "example.com".into(),
            quota_project: "q".into(),
        }),
    }
}

#[tokio::test]
async fn build_config_errors_like_go() {
    let mut u = ui_cfg();
    u.session_secret_env.clear();
    assert_eq!(
        build_config(&u, |_| String::new()).await.err().as_deref(),
        Some("ui: session_secret_env is required")
    );
    let u = ui_cfg();
    assert_eq!(
        build_config(&u, |_| String::new()).await.err().as_deref(),
        Some("ui: S is empty \u{2014} set the session secret")
    );
    assert_eq!(
        build_config(&u, |k| if k == "S" { "x".into() } else { String::new() })
            .await
            .err()
            .as_deref(),
        Some("ui oauth: O is empty \u{2014} set the OAuth client secret")
    );
    let mut u = ui_cfg();
    u.public_base_url.clear();
    assert_eq!(
        build_config(&u, |_| "x".into()).await.err().as_deref(),
        Some("ui oauth: public_base_url is required (the OAuth redirect base)")
    );
}

/// SIGTERM: a request already in flight finishes; the server then stops.
#[tokio::test]
async fn in_flight_request_finishes_after_sigterm() {
    // A tier that takes 600 ms to answer.
    let tl = tokio::net::TcpListener::bind("127.0.0.1:0")
        .await
        .expect("bind");
    let tier_url = format!("http://{}", tl.local_addr().expect("addr"));
    let slow = axum::Router::new().fallback(|| async {
        tokio::time::sleep(Duration::from_millis(600)).await;
        "[]"
    });
    tokio::spawn(async move {
        let _ = axum::serve(tl, slow).await;
    });
    let (app, sess) = session_app(vec![tier("t", &tier_url)]);
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0")
        .await
        .expect("bind");
    let addr = listener.local_addr().expect("addr");
    let (stop_tx, stop_rx) = tokio::sync::oneshot::channel::<()>();
    let server = tokio::spawn(serve(
        listener,
        Arc::clone(&app),
        async move {
            let _ = stop_rx.await;
        },
        Shutdown {
            linger: Duration::from_millis(100),
            drain: Duration::from_secs(5),
        },
    ));
    let hc = tfstackplan_server_clients::http::client(Duration::from_secs(10)).expect("client");
    let inflight = tokio::spawn({
        let hc = hc.clone();
        async move {
            hc.get(format!("http://{addr}/api/tiers/t/approvals"))
                .header("cookie", sess)
                .send()
                .await
        }
    });
    tokio::time::sleep(Duration::from_millis(200)).await;
    let _ = stop_tx.send(());
    let resp = inflight
        .await
        .expect("join")
        .expect("in-flight request failed");
    assert_eq!(resp.status().as_u16(), 200);
    assert_eq!(resp.text().await.expect("body"), "[]");
    tokio::time::timeout(Duration::from_secs(5), server)
        .await
        .expect("server did not stop")
        .expect("join");
    assert!(
        hc.get(format!("http://{addr}/ready")).send().await.is_err(),
        "still accepting"
    );
}
