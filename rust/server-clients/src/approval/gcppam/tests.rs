use std::sync::Mutex;

use super::*;
use crate::testserver::{Req, Resp, TestServer, serve};

const TEST_GRANT: &str = "projects/proj-1/locations/global/entitlements/iam/grants/g1";

// config_test.go

#[test]
fn config_defaults() {
    let c = Config::default();
    assert_eq!(
        c.base_url(),
        "https://privilegedaccessmanager.googleapis.com/v1"
    );
    assert_eq!(c.location(), "global");
    assert_eq!(c.duration(), "28800s");
    let c2 = Config {
        base_url: "http://x".into(),
        location: "us".into(),
        duration: "60s".into(),
        ..Config::default()
    };
    assert_eq!(
        (c2.base_url(), c2.location(), c2.duration()),
        ("http://x", "us", "60s")
    );
}

fn map(pairs: &[(&str, &str)]) -> BTreeMap<String, String> {
    pairs
        .iter()
        .map(|(k, v)| (k.to_string(), v.to_string()))
        .collect()
}

fn pool(n: &[&str]) -> Vec<String> {
    n.iter().map(|s| s.to_string()).collect()
}

#[test]
fn entitlement_name() {
    let c = Config {
        entitlements: map(&[("iam", "iam-elev")]),
        ..Config::default()
    };
    assert_eq!(
        c.entitlement_name("iam", "proj-a"),
        "projects/proj-a/locations/global/entitlements/iam-elev"
    );
    assert_eq!(c.entitlement_name("database", "proj-a"), "");
}

#[test]
fn requester() {
    let c = Config {
        requester_pool: pool(&["sa0", "sa1", "sa2"]),
        ..Config::default()
    };
    assert_eq!(c.requester(7), "sa1");
    assert_eq!(c.requester(0), "sa0");
    assert_eq!(c.requester(-1), "sa2");
    assert_eq!(Config::default().requester(3), "");
}

#[test]
fn lease_requester() {
    let c = Config {
        requester_pool: pool(&["sa0", "sa1", "sa2"]),
        ..Config::default()
    };
    let set = |s: &[&str]| s.iter().map(|x| x.to_string()).collect::<BTreeSet<_>>();
    assert_eq!(c.lease_requester(0, &set(&["sa0"])), "sa1");
    assert_eq!(c.lease_requester(5, &set(&[])), "sa0");
    assert_eq!(
        c.lease_requester(4, &set(&["sa0", "sa1", "sa2"])),
        "sa1",
        "mod fallback"
    );
}

#[test]
fn justification_round_trip() {
    let r = Request {
        class: "iam".into(),
        target: "proj-a".into(),
        pr: 42,
        environment: "staging".into(),
        requester: String::new(),
    };
    let j = justification(&r);
    assert_eq!(j, "PR #42 env=staging");
    assert_eq!(parse_pr_env(&j), Some((42, "staging".into())));
    assert_eq!(parse_pr_env("nonsense"), None);
    // Leftmost full match, as RE2 finds it; \S stops at ASCII whitespace.
    assert_eq!(
        parse_pr_env("x PR #a PR #7 env=prod tail"),
        Some((7, "prod".into()))
    );
    assert_eq!(
        parse_pr_env("PR #7 env=a\u{a0}b c"),
        Some((7, "a\u{a0}b".into()))
    );
    assert_eq!(parse_pr_env("PR #7 env= x"), None);
    assert_eq!(parse_pr_env("PR #99999999999999999999 env=x"), None);
}

#[test]
fn map_state_cases() {
    for (pam, want) in [
        ("APPROVAL_AWAITED", GrantState::Awaiting),
        ("SCHEDULED", GrantState::Awaiting),
        ("ACTIVATING", GrantState::Activating),
        ("ACTIVE", GrantState::Active),
        ("DENIED", GrantState::Denied),
        ("REVOKED", GrantState::Revoked),
        ("EXPIRED", GrantState::Expired),
        ("ENDED", GrantState::Expired),
    ] {
        assert_eq!(map_state(pam), want, "{pam}");
    }
    assert!(!map_state("WEIRD").open());
}

#[test]
fn valid_environment_cases() {
    for (env, ok) in [
        ("staging", true),
        ("prod", true),
        ("fh-dev-svc", true),
        ("", false),
        (" ", false),
        ("stag ing", false),
        ("a\tb", false),
        ("x\n", false),
        ("a\u{a0}b", false),
    ] {
        assert_eq!(valid_environment(env).is_ok(), ok, "{env:?}");
    }
}

#[test]
fn entitlement_name_scope() {
    let c = Config {
        location: "global".into(),
        entitlements: map(&[
            ("iam", "iam-ent"),
            ("database", "db-ent"),
            ("org", "org-ent"),
        ]),
        entitlement_scopes: map(&[("database", "folders"), ("org", "organizations")]),
        ..Config::default()
    };
    for (class, want) in [
        (
            "iam",
            "projects/proj-1/locations/global/entitlements/iam-ent",
        ),
        (
            "database",
            "folders/proj-1/locations/global/entitlements/db-ent",
        ),
        (
            "org",
            "organizations/proj-1/locations/global/entitlements/org-ent",
        ),
        ("missing", ""),
    ] {
        assert_eq!(c.entitlement_name(class, "proj-1"), want, "{class}");
    }
}

// decide_test.go

#[tokio::test]
async fn decide_grant_calls() {
    let seen = Arc::new(Mutex::new(Vec::<Req>::new()));
    let s = Arc::clone(&seen);
    let srv = serve(move |r| {
        s.lock().unwrap().push(r);
        Resp::ok("")
    })
    .await
    .unwrap();
    decide_grant(
        &srv.url,
        "quota-proj",
        "user-token",
        TEST_GRANT,
        true,
        "lgtm",
    )
    .await
    .unwrap();
    {
        let r = seen.lock().unwrap()[0].clone();
        assert_eq!(r.path, format!("/{TEST_GRANT}:approve"));
        assert_eq!(r.header("authorization"), "Bearer user-token");
        assert_eq!(r.header("x-goog-user-project"), "quota-proj");
        assert_eq!(r.json()["reason"], "lgtm");
    }
    decide_grant(&srv.url, "", "user-token", TEST_GRANT, false, "nope")
        .await
        .unwrap();
    let r = seen.lock().unwrap()[1].clone();
    assert!(r.path.ends_with(":deny"));
    assert!(r.headers.get("x-goog-user-project").is_none());
}

#[tokio::test]
async fn decide_grant_surfaces_pam_error_verbatim() {
    let srv = serve(|_| Resp::status(403, r#"{"error":{"message":"Permission denied on grant"}}"#))
        .await
        .unwrap();
    let err = decide_grant(&srv.url, "", "t", TEST_GRANT, true, "")
        .await
        .unwrap_err();
    let DecideError::Rejected { status, body } = &err else {
        panic!("want Rejected, got {err:?}")
    };
    assert_eq!(*status, 403);
    assert!(body.contains("Permission denied on grant"));
    assert_eq!(
        err.to_string(),
        r#"pam: 403: {"error":{"message":"Permission denied on grant"}}"#
    );
}

#[tokio::test]
async fn decide_grant_rejects_bad_name_without_io() {
    let err = decide_grant("http://127.0.0.1:1", "", "t", "grants/g1", true, "")
        .await
        .unwrap_err();
    assert_eq!(
        err.to_string(),
        r#"not a PAM grant resource name: "grants/g1""#
    );
}

#[test]
fn validate_grant_name_cases() {
    validate_grant_name(TEST_GRANT).unwrap();
    validate_grant_name("folders/f/locations/l/entitlements/e/grants/g").unwrap();
    for bad in [
        String::new(),
        "grants/g1".into(),
        "projects/p/grants/g1".into(),
        format!("https://evil/{TEST_GRANT}"),
        format!("{TEST_GRANT}/extra"),
        "projects//locations/l/entitlements/e/grants/g".into(),
        "billing/p/locations/l/entitlements/e/grants/g".into(),
    ] {
        assert!(validate_grant_name(&bad).is_err(), "accepted {bad:?}");
    }
}

// grants_test.go and gcppam_test.go

type Seen = Arc<Mutex<Vec<Req>>>;

/// Go's fakePAM: a PAM server and a backend with stub tokens ("adc-token", "imp-<sa>").
async fn fake_pam<F>(cfg: Config, handler: F) -> (Backend, TestServer, Seen, Arc<Mutex<String>>)
where
    F: Fn(&Req) -> Resp + Send + Sync + 'static,
{
    let seen: Seen = Arc::new(Mutex::new(Vec::new()));
    let s = Arc::clone(&seen);
    let srv = serve(move |r| {
        let resp = handler(&r);
        s.lock().unwrap().push(r);
        resp
    })
    .await
    .unwrap();
    let impersonated = Arc::new(Mutex::new(String::new()));
    let imp = Arc::clone(&impersonated);
    let cfg = Config {
        base_url: srv.url.clone(),
        ..cfg
    };
    let b = Backend::new(
        cfg,
        Arc::new(|| async { Ok::<_, Error>("adc-token".to_string()) }),
        Arc::new(move |sa: String| {
            let imp = Arc::clone(&imp);
            async move {
                *imp.lock().unwrap() = sa.clone();
                Ok::<_, Error>(format!("imp-{sa}"))
            }
        }),
    )
    .unwrap();
    (b, srv, seen, impersonated)
}

fn default_cfg() -> Config {
    Config {
        entitlements: map(&[("iam", "iam-elev")]),
        requester_pool: pool(&["sa0", "sa1"]),
        ..Config::default()
    }
}

fn pr_req(pr: i64, env: &str) -> Request {
    Request {
        class: "iam".into(),
        target: "proj-a".into(),
        pr,
        environment: env.into(),
        requester: String::new(),
    }
}

#[tokio::test]
async fn list_grants_maps_and_correlates() {
    let (b, _srv, seen, _) = fake_pam(default_cfg(), |_| {
        Resp::ok(r#"{"grants":[
            {"name":"projects/proj-a/locations/global/entitlements/iam-elev/grants/g1","state":"ACTIVE","justification":{"unstructuredJustification":"PR #42 env=staging"}},
            {"name":"…/grants/g2","state":"APPROVAL_AWAITED","justification":{"unstructuredJustification":"PR #7 env=prod"}}
        ]}"#)
    })
    .await;
    let got = b.list_grants("iam", "proj-a").await.unwrap();
    assert_eq!(got.len(), 2);
    assert_eq!(got[0].state, GrantState::Active);
    assert_eq!(
        (got[0].request.pr, got[0].request.environment.as_str()),
        (42, "staging")
    );
    assert_eq!(got[1].state, GrantState::Awaiting);
    assert_eq!(got[1].request.pr, 7);
    assert_eq!(
        (
            got[0].request.class.as_str(),
            got[0].request.target.as_str()
        ),
        ("iam", "proj-a")
    );
    let r = seen.lock().unwrap()[0].clone();
    assert_eq!(r.method, "GET");
    assert!(r.path.ends_with("/entitlements/iam-elev/grants"));
    assert_eq!(r.header("authorization"), "Bearer adc-token");
}

#[tokio::test]
async fn list_grants_follows_pages() {
    let (b, _srv, seen, _) = fake_pam(default_cfg(), |r| {
        if r.query.is_empty() {
            Resp::ok(r#"{"grants":[{"name":"g1","state":"ACTIVE"}],"nextPageToken":"p 2"}"#)
        } else {
            Resp::ok(r#"{"grants":[{"name":"g2","state":"DENIED"}]}"#)
        }
    })
    .await;
    let got = b.list_grants("iam", "proj-a").await.unwrap();
    assert_eq!(
        got.iter().map(|g| g.name.as_str()).collect::<Vec<_>>(),
        ["g1", "g2"]
    );
    assert_eq!(seen.lock().unwrap()[1].param("pageToken", false), "p 2");
}

#[tokio::test]
async fn list_grants_unconfigured_class() {
    let (b, _srv, seen, _) = fake_pam(default_cfg(), |_| Resp::ok("{}")).await;
    let err = b.list_grants("database", "proj-a").await.unwrap_err();
    assert_eq!(
        err.to_string(),
        r#"gcppam: no entitlement configured for class "database""#
    );
    assert!(seen.lock().unwrap().is_empty(), "should not call PAM");
}

#[tokio::test]
async fn list_grants_captures_requester() {
    let (b, _srv, _, _) = fake_pam(default_cfg(), |_| {
        Resp::ok(r#"{"grants":[{"name":".../grants/g1","state":"ACTIVE","requester":"sa0","justification":{"unstructuredJustification":"PR #1 env=staging"}}]}"#)
    })
    .await;
    let gs = b.list_grants("iam", "proj-a").await.unwrap();
    assert_eq!(gs.len(), 1);
    assert_eq!(gs[0].requester, "sa0");
}

#[test]
fn backend_implements_approval() {
    fn assert_impl<T: ApprovalBackend>() {}
    assert_impl::<Backend>();
}

#[tokio::test]
async fn request_grant_creates_then_reuses() {
    let existing = Arc::new(Mutex::new(String::new()));
    let ex = Arc::clone(&existing);
    let (b, _srv, seen, _) = fake_pam(default_cfg(), move |r| {
        let mut existing = ex.lock().unwrap();
        match r.method.as_str() {
            "GET" if existing.is_empty() => Resp::ok(r#"{"grants":[]}"#),
            "GET" => Resp::ok(format!(
                r#"{{"grants":[{{"name":"{existing}","state":"APPROVAL_AWAITED","justification":{{"unstructuredJustification":"PR #42 env=staging"}}}}]}}"#
            )),
            _ => {
                assert_eq!(r.header("authorization"), "Bearer imp-sa0");
                *existing = "projects/proj-a/locations/global/entitlements/iam-elev/grants/new".into();
                Resp::ok(format!(r#"{{"name":"{existing}"}}"#))
            }
        }
    })
    .await;
    let r = pr_req(42, "staging");
    let g1 = b.request_grant(&r).await.unwrap();
    assert_eq!(g1.state, GrantState::Awaiting);
    assert!(!g1.name.is_empty());
    let g2 = b.request_grant(&r).await.unwrap();
    assert_eq!(g2.name, g1.name);
    let creates = seen
        .lock()
        .unwrap()
        .iter()
        .filter(|r| r.method == "POST")
        .count();
    assert_eq!(creates, 1, "reuse the open grant");
    let create = seen
        .lock()
        .unwrap()
        .iter()
        .find(|r| r.method == "POST")
        .cloned()
        .unwrap();
    assert_eq!(create.json()["requestedDuration"], "28800s");
    assert_eq!(
        create.json()["justification"]["unstructuredJustification"],
        "PR #42 env=staging"
    );
}

#[tokio::test]
async fn request_grant_leases_unused_requester() {
    let (b, _srv, _, impersonated) = fake_pam(default_cfg(), |r| {
        if r.method == "GET" {
            Resp::ok(r#"{"grants":[{"name":".../grants/g1","state":"ACTIVE","requester":"sa0","justification":{"unstructuredJustification":"PR #1 env=staging"}}]}"#)
        } else {
            Resp::ok(r#"{"name":".../grants/g2"}"#)
        }
    })
    .await;
    b.request_grant(&pr_req(2, "staging")).await.unwrap();
    assert_eq!(
        *impersonated.lock().unwrap(),
        "sa1",
        "sa0 holds an open grant"
    );
}

#[tokio::test]
async fn revoke() {
    let (b, _srv, seen, _) = fake_pam(default_cfg(), |r| {
        if r.method == "GET" {
            Resp::ok(r#"{"grants":[{"name":"projects/proj-a/locations/global/entitlements/iam-elev/grants/g1","state":"ACTIVE","justification":{"unstructuredJustification":"PR #42 env=staging"}}]}"#)
        } else {
            Resp::ok("{}")
        }
    })
    .await;
    b.revoke(&pr_req(42, "staging")).await.unwrap();
    let r = seen
        .lock()
        .unwrap()
        .iter()
        .find(|r| r.method == "POST")
        .cloned()
        .unwrap();
    assert!(r.path.ends_with("/grants/g1:revoke"), "{}", r.path);
    assert_eq!(r.header("authorization"), "Bearer adc-token");
    assert_eq!(r.json()["reason"], "post-apply cleanup");
}

#[tokio::test]
async fn revoke_no_matching_grant() {
    let (b, _srv, seen, _) = fake_pam(default_cfg(), |_| {
        Resp::ok(r#"{"grants":[{"name":"…/grants/other","state":"ACTIVE","justification":{"unstructuredJustification":"PR #99 env=staging"}}]}"#)
    })
    .await;
    b.revoke(&pr_req(42, "staging")).await.unwrap();
    assert!(seen.lock().unwrap().iter().all(|r| r.method == "GET"));
}

#[tokio::test]
async fn request_grant_uses_hinted_requester() {
    let (b, _srv, _, impersonated) = fake_pam(default_cfg(), |r| {
        if r.method == "GET" {
            Resp::ok(r#"{"grants":[]}"#)
        } else {
            Resp::ok(r#"{"name":"projects/proj-a/locations/global/entitlements/iam-elev/grants/hinted"}"#)
        }
    })
    .await;
    let mut r = pr_req(5, "prod");
    r.requester = "sa-pinned".into();
    let g = b.request_grant(&r).await.unwrap();
    assert_eq!(*impersonated.lock().unwrap(), "sa-pinned");
    assert_eq!(g.requester, "sa-pinned");
}

#[tokio::test]
async fn request_grant_rejects_bad_env_without_io() {
    let (b, _srv, seen, _) = fake_pam(default_cfg(), |_| Resp::ok("{}")).await;
    for env in ["stag ing", ""] {
        assert!(b.request_grant(&pr_req(7, env)).await.is_err(), "{env:?}");
    }
    let err = b.request_grant(&pr_req(7, "stag ing")).await.unwrap_err();
    assert!(err.to_string().contains("whitespace"), "{err}");
    assert!(
        seen.lock().unwrap().is_empty(),
        "no HTTP call before validation"
    );
}

#[tokio::test]
async fn request_grant_returns_slot_collision_on_foreign_grant() {
    let (b, _srv, seen, _) = fake_pam(default_cfg(), |r| {
        if r.method == "GET" {
            Resp::ok(r#"{"grants":[{"name":"projects/proj-a/locations/global/entitlements/iam-elev/grants/foreign","state":"ACTIVE","requester":"sa0","justification":{"unstructuredJustification":"PR #99 env=nonprod"}}]}"#)
        } else {
            Resp::status(400, r#"{"error":{"code":400,"message":"INVALID_ARGUMENT: only one open grant per requester"}}"#)
        }
    })
    .await;
    let err = b.request_grant(&pr_req(7, "staging")).await.unwrap_err();
    let Error::SlotCollision(col) = &err else {
        panic!("want SlotCollision, got {err:?}")
    };
    assert_eq!(col.blocking_grant.request.pr, 99);
    let creates = seen
        .lock()
        .unwrap()
        .iter()
        .filter(|r| r.method == "POST")
        .count();
    assert_eq!(creates, 1, "no retry");
}

#[tokio::test]
async fn request_grant_create_error_without_foreign_grant() {
    let (b, srv, _, _) = fake_pam(default_cfg(), |r| {
        if r.method == "GET" {
            Resp::ok(r#"{"grants":[]}"#)
        } else {
            Resp::status(500, "boom")
        }
    })
    .await;
    let err = b.request_grant(&pr_req(7, "staging")).await.unwrap_err();
    assert_eq!(
        err.to_string(),
        format!(
            "gcppam: POST {}/projects/proj-a/locations/global/entitlements/iam-elev/grants -> 500: boom",
            srv.url
        )
    );
}
