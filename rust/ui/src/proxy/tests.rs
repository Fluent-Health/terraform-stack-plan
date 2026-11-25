use std::sync::{Arc, Mutex};

use tfstackplan_server_clients::testserver::{Resp, serve};

use crate::testutil::{dead_url, get, req, send, session_app, tier_with_token};

#[derive(Default, Clone)]
struct Seen {
    auth: String,
    query: String,
    last_id: String,
    path: String,
}

// Go: TestTierProxy.
#[tokio::test]
async fn tier_proxy() {
    let seen = Arc::new(Mutex::new(Seen::default()));
    let s = Arc::clone(&seen);
    let tier_srv = serve(move |r| {
        *s.lock().expect("lock") = Seen {
            auth: r.header("authorization").into(),
            query: r.query.clone(),
            path: r.path.clone(),
            ..Seen::default()
        };
        let json = |b: &str| Resp::ok(b).header("Content-Type", "application/json");
        match r.path.as_str() {
            "/api/executions" => json(r#"[{"id":"e1"}]"#),
            "/api/approvals" => json(r#"[{"pr":7}]"#),
            "/api/pr/7" => json(r#"{"n":7}"#),
            "/api/inspect/pool" => json(
                r#"{"environment":"nonprod","slots":[{"requester":"sa-1@fh.com","occupied":false}],"waiting":[]}"#,
            ),
            "/api/merge-queue" => {
                json(r#"{"branch":"main","entries":[{"position":1,"pr":774,"state":"MERGEABLE"}]}"#)
            }
            "/api/lifecycle" => json(r#"[{"key":"plan","label":"plan","state":"done"}]"#),
            p if p.starts_with("/api/execution/") => {
                if p.ends_with("/nope") {
                    Resp::status(404, "not found\n")
                } else {
                    json(r#"{"ID":"e1"}"#)
                }
            }
            p => Resp::status(418, format!("unexpected path {p}")),
        }
    })
    .await
    .expect("tier");
    let dead = dead_url().await;
    let (app, sess) = session_app(vec![
        tier_with_token("nonprod", &tier_srv.url, "s2s-token"),
        tier_with_token("prod", &dead, "s2s-token"),
    ]);
    let s = Some(sess.as_str());

    // Executions: body, query and bearer pass through.
    let rr = get(&app, "/api/tiers/nonprod/executions?pr=7&limit=5", s).await;
    assert_eq!((rr.status, rr.body.trim()), (200, r#"[{"id":"e1"}]"#));
    assert_eq!(rr.header("content-type"), "application/json");
    let got = seen.lock().expect("lock").clone();
    assert_eq!(got.auth, "Bearer s2s-token");
    assert_eq!(got.query, "pr=7&limit=5");

    let cases = [
        ("/api/tiers/nonprod/approvals", r#"[{"pr":7}]"#),
        ("/api/tiers/nonprod/executions/e1", r#"{"ID":"e1"}"#),
        ("/api/tiers/nonprod/pr/7", r#"{"n":7}"#),
        (
            "/api/tiers/nonprod/inspect/pool",
            r#"{"environment":"nonprod","slots":[{"requester":"sa-1@fh.com","occupied":false}],"waiting":[]}"#,
        ),
        (
            "/api/tiers/nonprod/merge-queue",
            r#"{"branch":"main","entries":[{"position":1,"pr":774,"state":"MERGEABLE"}]}"#,
        ),
        (
            "/api/tiers/nonprod/lifecycle?pr=42",
            r#"[{"key":"plan","label":"plan","state":"done"}]"#,
        ),
    ];
    for (path, want) in cases {
        let rr = get(&app, path, s).await;
        assert_eq!((rr.status, rr.body.trim()), (200, want), "{path}");
    }
    assert_eq!(seen.lock().expect("lock").query, "pr=42");

    // Tier-side non-2xx statuses pass through untouched.
    assert_eq!(
        get(&app, "/api/tiers/nonprod/executions/nope", s)
            .await
            .status,
        404
    );

    // The tiers listing keeps config order.
    let rr = get(&app, "/api/tiers", s).await;
    let tiers: Vec<serde_json::Value> = serde_json::from_str(&rr.body).expect("json");
    assert_eq!(tiers.len(), 2);
    assert_eq!(tiers[0]["name"], "nonprod");
    assert!(
        rr.body.starts_with(r#"[{"name":"nonprod","url":""#),
        "{}",
        rr.body
    );

    // A dead tier is a 502 naming the tier; the others are unaffected.
    for path in [
        "/api/tiers/prod/executions",
        "/api/tiers/prod/pr/7",
        "/api/tiers/prod/inspect/pool",
        "/api/tiers/prod/merge-queue",
        "/api/tiers/prod/lifecycle?pr=42",
    ] {
        let rr = get(&app, path, s).await;
        assert_eq!(
            (rr.status, rr.body.as_str()),
            (502, "tier prod unreachable\n"),
            "{path}"
        );
    }
    // Unknown tier: 404.
    let rr = get(&app, "/api/tiers/stage/executions", s).await;
    assert_eq!((rr.status, rr.body.as_str()), (404, "unknown tier\n"));
    // No session: 401 without touching the tier.
    seen.lock().expect("lock").path.clear();
    assert_eq!(
        get(&app, "/api/tiers/nonprod/executions", None)
            .await
            .status,
        401
    );
    assert_eq!(seen.lock().expect("lock").path, "");
}

/// The generated wrappers bind parameters before the session check, with the router's
/// error texts.
#[tokio::test]
async fn parameter_errors_match_the_generated_router() {
    let (app, sess) = session_app(vec![tier_with_token("t", "http://127.0.0.1:9", "x")]);
    let cases = [
        (
            "/api/tiers/t/pr/abc",
            "Invalid format for parameter n: error binding string parameter: strconv.ParseInt: parsing \"abc\": invalid syntax\n",
        ),
        (
            "/api/tiers/t/lifecycle",
            "Query argument pr is required, but not found\n",
        ),
        (
            "/api/tiers/t/executions?pr=1&pr=2",
            "Invalid format for parameter pr: multiple values for single value parameter 'pr'\n",
        ),
        (
            "/api/tiers/t/executions?limit=99999999999999999999",
            "Invalid format for parameter limit: error binding string parameter: strconv.ParseInt: parsing \"99999999999999999999\": value out of range\n",
        ),
        (
            "/api/tiers/%25zz/approvals",
            "Invalid format for parameter tier: error unescaping path parameter 'tier': invalid URL escape \"%zz\"\n",
        ),
    ];
    for (path, want) in cases {
        for cookie in [None, Some(sess.as_str())] {
            let rr = get(&app, path, cookie).await;
            assert_eq!((rr.status, rr.body.as_str()), (400, want), "{path}");
            assert_eq!(rr.header("x-content-type-options"), "nosniff");
        }
    }
}

// Go: TestStreamProxies.
#[tokio::test]
async fn stream_proxies() {
    let seen = Arc::new(Mutex::new(Seen::default()));
    let s = Arc::clone(&seen);
    let tier_srv = serve(move |r| {
        *s.lock().expect("lock") = Seen {
            auth: r.header("authorization").into(),
            query: r.query.clone(),
            last_id: r.header("last-event-id").into(),
            path: r.path.clone(),
        };
        match r.path.as_str() {
            "/api/execution/e1/events" => {
                Resp::ok("data: changed\n\nevent: superseded\ndata: e2\n\n")
                    .header("Content-Type", "text/event-stream")
                    .header("Cache-Control", "no-cache")
            }
            p if p.starts_with("/logs/e1/") => Resp::ok("id: 42\ndata: terraform says hi\n\n")
                .header("Content-Type", "text/event-stream"),
            p if p.starts_with("/plan/e1/") => {
                Resp::ok("<h3>stacks/a</h3>").header("Content-Type", "text/html; charset=utf-8")
            }
            p => Resp::status(418, format!("unexpected {p}")),
        }
    })
    .await
    .expect("tier");
    let (app, sess) = session_app(vec![tier_with_token("nonprod", &tier_srv.url, "s2s-token")]);
    let s = Some(sess.as_str());

    // SSE events: bearer attached, both events relayed, headers kept.
    let rr = get(&app, "/api/tiers/nonprod/executions/e1/events", s).await;
    assert_eq!(rr.status, 200);
    assert_eq!(rr.header("content-type"), "text/event-stream");
    assert_eq!(rr.header("cache-control"), "no-cache");
    assert!(
        rr.body.contains("data: changed") && rr.body.contains("event: superseded"),
        "{}",
        rr.body
    );
    assert_eq!(seen.lock().expect("lock").auth, "Bearer s2s-token");

    // Logs: follow and Last-Event-ID forwarded for resume.
    let mut r = req("GET", "/api/tiers/nonprod/logs/e1/stacks/a?follow=1", s);
    r.headers_mut()
        .insert("last-event-id", "17".parse().expect("hv"));
    let rr2 = send(&app, r).await;
    assert_eq!(rr2.status, 200);
    assert!(rr2.body.contains("terraform says hi"));
    let got = seen.lock().expect("lock").clone();
    assert_eq!(
        (got.last_id.as_str(), got.query.as_str()),
        ("17", "follow=1")
    );
    assert_eq!(got.path, "/logs/e1/stacks/a");

    // Plan fragment.
    let rr = get(&app, "/api/tiers/nonprod/plan/e1/stacks/a", s).await;
    assert_eq!(rr.status, 200);
    assert!(rr.body.contains("<h3>stacks/a</h3>"));

    // Session and tier guards hold on the stream routes too.
    assert_eq!(
        get(&app, "/api/tiers/nonprod/executions/e1/events", None)
            .await
            .status,
        401
    );
    assert_eq!(
        get(&app, "/api/tiers/nope/logs/e1/stacks/a", s)
            .await
            .status,
        404
    );
}

#[tokio::test]
async fn stream_token_failure_is_502() {
    let f: tfstackplan_server_clients::TokenFunc =
        Arc::new(|| async { Err::<String, _>(tfstackplan_server_clients::Error::msg("no creds")) });
    let (app, sess) = session_app(vec![crate::app::Tier {
        name: "t".into(),
        url: "http://127.0.0.1:9".into(),
        token: Some(f),
    }]);
    for path in [
        "/api/tiers/t/executions/e1/events",
        "/api/tiers/t/approvals",
    ] {
        let rr = get(&app, path, Some(&sess)).await;
        assert_eq!(
            (rr.status, rr.body.as_str()),
            (502, "tier t unreachable\n"),
            "{path}"
        );
    }
}
