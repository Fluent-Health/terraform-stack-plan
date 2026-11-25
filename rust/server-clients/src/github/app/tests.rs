use std::sync::{Arc, Mutex};
use std::time::{Duration, UNIX_EPOCH};

use aws_lc_rs::encoding::AsDer;
use aws_lc_rs::rsa::KeySize;
use aws_lc_rs::signature::{KeyPair as _, RSA_PKCS1_2048_8192_SHA256, UnparsedPublicKey};
use base64::engine::general_purpose::STANDARD;

use super::*;
use crate::testserver::{Req, Resp, TestServer, serve};

const TOKEN_PATH: &str = "/app/installations/67890/access_tokens";

fn test_key() -> RsaKeyPair {
    RsaKeyPair::generate(KeySize::Rsa2048).unwrap()
}

fn pem(label: &str, der: &[u8]) -> Vec<u8> {
    let b64 = STANDARD.encode(der);
    let mut out = format!("-----BEGIN {label}-----\n");
    for line in b64.as_bytes().chunks(64) {
        out.push_str(std::str::from_utf8(line).unwrap());
        out.push('\n');
    }
    out.push_str(&format!("-----END {label}-----\n"));
    out.into_bytes()
}

/// Reads one DER tag-length-value; returns (tag, contents, rest).
fn tlv(b: &[u8]) -> (u8, &[u8], &[u8]) {
    let tag = b[0];
    let (len, hdr) = if b[1] < 0x80 {
        (usize::from(b[1]), 2)
    } else {
        let n = usize::from(b[1] & 0x7f);
        let len = b[2..2 + n]
            .iter()
            .fold(0usize, |a, &x| (a << 8) | usize::from(x));
        (len, 2 + n)
    };
    (tag, &b[hdr..hdr + len], &b[hdr + len..])
}

/// The PKCS#1 RSAPrivateKey inside a PKCS#8 PrivateKeyInfo.
fn pkcs1_from_pkcs8(der: &[u8]) -> Vec<u8> {
    let (_, info, _) = tlv(der);
    let (_, _version, rest) = tlv(info);
    let (_, _alg, rest) = tlv(rest);
    let (tag, key, _) = tlv(rest);
    assert_eq!(tag, 0x04);
    key.to_vec()
}

fn pkcs8_der(k: &RsaKeyPair) -> Vec<u8> {
    k.as_der().unwrap().as_ref().to_vec()
}

fn pkcs1_pem(k: &RsaKeyPair) -> Vec<u8> {
    pem("RSA PRIVATE KEY", &pkcs1_from_pkcs8(&pkcs8_der(k)))
}

fn new_test_client(srv: &TestServer) -> RealClient {
    RealClient::new("12345", "67890", &pkcs1_pem(&test_key()))
        .unwrap()
        .with_base_url(&srv.url)
}

fn token_ok() -> Resp {
    Resp::ok(r#"{"token":"ghs_test"}"#)
}

/// Records every request and answers through `f`.
async fn fake_github<F>(f: F) -> (TestServer, Arc<Mutex<Vec<Req>>>)
where
    F: Fn(&Req) -> Resp + Send + Sync + 'static,
{
    let seen = Arc::new(Mutex::new(Vec::new()));
    let s = Arc::clone(&seen);
    let srv = serve(move |r| {
        let resp = f(&r);
        s.lock().unwrap().push(r);
        resp
    })
    .await
    .unwrap();
    (srv, seen)
}

#[test]
fn parse_rsa_private_key_pkcs1_and_pkcs8() {
    let k = test_key();
    parse_rsa_private_key(&pkcs1_pem(&k)).unwrap();
    parse_rsa_private_key(&pem("PRIVATE KEY", &pkcs8_der(&k))).unwrap();
    assert!(parse_rsa_private_key(b"not a pem").is_err());
}

#[test]
fn app_jwt_structure_and_signature() {
    let k = test_key();
    let now = UNIX_EPOCH + Duration::from_secs(1_700_000_000);
    let tok = app_jwt("12345", &k, now).unwrap();
    let parts: Vec<&str> = tok.split('.').collect();
    assert_eq!(parts.len(), 3);
    let hdr: Value = serde_json::from_slice(&URL_SAFE_NO_PAD.decode(parts[0]).unwrap()).unwrap();
    assert_eq!(hdr["alg"], "RS256");
    assert_eq!(hdr["typ"], "JWT");
    let c: Value = serde_json::from_slice(&URL_SAFE_NO_PAD.decode(parts[1]).unwrap()).unwrap();
    assert_eq!(c["iss"], "12345");
    assert_eq!(c["iat"], 1_700_000_000 - 60, "iat backdated 60s");
    assert_eq!(c["exp"].as_i64().unwrap() - c["iat"].as_i64().unwrap(), 540);
    let sig = URL_SAFE_NO_PAD.decode(parts[2]).unwrap();
    UnparsedPublicKey::new(&RSA_PKCS1_2048_8192_SHA256, k.public_key().as_ref())
        .verify(format!("{}.{}", parts[0], parts[1]).as_bytes(), &sig)
        .unwrap();
}

#[test]
fn new_real_client_validates() {
    let pem = pkcs1_pem(&test_key());
    assert!(RealClient::new("", "67890", &pem).is_err());
    assert!(RealClient::new("12345", "", &pem).is_err());
    assert!(RealClient::new("12345", "67890", b"bad").is_err());
}

#[test]
fn real_client_implements_github() {
    fn assert_impl<T: GitHub>() {}
    assert_impl::<RealClient>();
}

#[tokio::test]
async fn pr_head_sha_mints_token_and_reads() {
    let (srv, seen) = fake_github(|r| match (r.method.as_str(), r.path.as_str()) {
        ("POST", TOKEN_PATH) => {
            assert!(r.header("authorization").starts_with("Bearer "));
            token_ok()
        }
        ("GET", "/repos/o/r/pulls/7") => Resp::ok(r#"{"head":{"sha":"deadbeef"}}"#),
        _ => Resp::status(404, ""),
    })
    .await;
    let c = new_test_client(&srv);
    assert_eq!(c.pr_head_sha("o/r", 7).await.unwrap(), "deadbeef");
    let seen = seen.lock().unwrap();
    assert_eq!(seen.len(), 2, "one token mint, one API call");
    assert_eq!(seen[1].header("authorization"), "Bearer ghs_test");
}

#[tokio::test]
async fn installation_token_is_cached_until_near_expiry() {
    let exp = jiff::Timestamp::now() + jiff::SignedDuration::from_hours(1);
    let (srv, seen) = fake_github(move |r| match r.path.as_str() {
        TOKEN_PATH => Resp::ok(format!(r#"{{"token":"ghs_test","expires_at":"{exp}"}}"#)),
        _ => Resp::ok(r#"{"head":{"sha":"x"}}"#),
    })
    .await;
    let c = new_test_client(&srv);
    c.pr_head_sha("o/r", 1).await.unwrap();
    c.pr_head_sha("o/r", 2).await.unwrap();
    let mints = seen
        .lock()
        .unwrap()
        .iter()
        .filter(|r| r.path == TOKEN_PATH)
        .count();
    assert_eq!(mints, 1);
}

#[test]
fn split_repo_cases() {
    assert!(split_repo("noslash").is_err());
    assert!(split_repo("/name").is_err());
    assert!(split_repo("owner/").is_err());
    assert_eq!(split_repo("owner/name").unwrap(), ("owner", "name"));
    assert_eq!(
        split_repo("noslash").unwrap_err().to_string(),
        r#"github: invalid repo "noslash" (want owner/name)"#
    );
}

#[tokio::test]
async fn create_check_run() {
    let (srv, seen) = fake_github(|r| match (r.method.as_str(), r.path.as_str()) {
        (_, TOKEN_PATH) => token_ok(),
        ("POST", "/repos/o/r/check-runs") => Resp::ok(r#"{"id":555}"#),
        _ => Resp::status(404, ""),
    })
    .await;
    let c = new_test_client(&srv);
    let id = c
        .create_check_run("o/r", "sha123", "plan/staging", "https://srv/live/e1")
        .await
        .unwrap();
    assert_eq!(id, 555);
    let raw = seen.lock().unwrap()[1].json();
    assert_eq!(raw["name"], "plan/staging");
    assert_eq!(raw["head_sha"], "sha123");
    assert_eq!(raw["status"], "in_progress");
    assert_eq!(raw["details_url"], "https://srv/live/e1");
}

#[tokio::test]
async fn update_check_run_terminal_sets_conclusion() {
    let (srv, seen) = fake_github(|r| match (r.method.as_str(), r.path.as_str()) {
        (_, TOKEN_PATH) => token_ok(),
        ("PATCH", "/repos/o/r/check-runs/555") => Resp::ok("{}"),
        _ => Resp::status(404, ""),
    })
    .await;
    let c = new_test_client(&srv);
    c.update_check_run(
        "o/r",
        555,
        &CheckRunUpdate {
            summary: "### progress".into(),
            text: "# report".into(),
            details_url: "u".into(),
            conclusion: "action_required".into(),
            ..CheckRunUpdate::default()
        },
    )
    .await
    .unwrap();
    let raw = seen.lock().unwrap()[1].json();
    assert_eq!(raw["status"], "completed");
    assert_eq!(raw["conclusion"], "action_required");
    assert_eq!(raw["output"]["summary"], "### progress");
}

#[tokio::test]
async fn update_check_run_running_omits_conclusion() {
    let (srv, seen) = fake_github(|r| match r.path.as_str() {
        TOKEN_PATH => token_ok(),
        _ => Resp::ok("{}"),
    })
    .await;
    let c = new_test_client(&srv);
    c.update_check_run(
        "o/r",
        555,
        &CheckRunUpdate {
            summary: "x".into(),
            ..CheckRunUpdate::default()
        },
    )
    .await
    .unwrap();
    let raw = seen.lock().unwrap()[1].json();
    assert!(raw.get("status").is_none() && raw.get("conclusion").is_none());
}

#[tokio::test]
async fn post_status() {
    let (srv, seen) = fake_github(|r| match r.path.as_str() {
        TOKEN_PATH => token_ok(),
        _ => Resp::ok("{}"),
    })
    .await;
    let c = new_test_client(&srv);
    c.post_status(
        "o/r",
        "sha123",
        "plan/staging",
        "pending",
        "planning 1/2 stacks",
        "https://srv/live/e1",
    )
    .await
    .unwrap();
    let r = seen.lock().unwrap()[1].clone();
    assert_eq!(r.path, "/repos/o/r/statuses/sha123");
    let raw = r.json();
    assert_eq!(raw["state"], "pending");
    assert_eq!(raw["context"], "plan/staging");
    assert_eq!(raw["description"], "planning 1/2 stacks");
    assert_eq!(raw["target_url"], "https://srv/live/e1");
}

#[tokio::test]
async fn do_propagates_non_2xx() {
    let (srv, _) = fake_github(|r| match r.path.as_str() {
        TOKEN_PATH => token_ok(),
        _ => Resp::status(500, r#"{"message":"boom"}"#),
    })
    .await;
    let c = new_test_client(&srv);
    let err = c
        .create_check_run("o/r", "sha", "staging", "")
        .await
        .unwrap_err();
    assert_eq!(
        err.to_string(),
        format!(
            r#"github: POST {}/repos/o/r/check-runs: 500: {{"message":"boom"}}"#,
            srv.url
        )
    );
}

#[tokio::test]
async fn mint_token_failure_propagates() {
    let (srv, seen) = fake_github(|r| match r.path.as_str() {
        TOKEN_PATH => Resp::status(401, r#"{"message":"bad jwt"}"#),
        _ => Resp::ok("{}"),
    })
    .await;
    let c = new_test_client(&srv);
    let err = c
        .post_status("o/r", "sha", "plan/staging", "pending", "x", "")
        .await
        .unwrap_err();
    assert_eq!(
        err.to_string(),
        r#"github: resolve token: github: installation token exchange 401: {"message":"bad jwt"}"#
    );
    assert!(
        seen.lock().unwrap().iter().all(|r| r.path == TOKEN_PATH),
        "API must not be called"
    );
}

#[tokio::test]
async fn update_check_run_emits_title() {
    let (srv, seen) = fake_github(|r| match r.path.as_str() {
        TOKEN_PATH => token_ok(),
        _ => Resp::ok("{}"),
    })
    .await;
    let c = new_test_client(&srv);
    c.update_check_run(
        "o/r",
        555,
        &CheckRunUpdate {
            title: "Terraform apply".into(),
            summary: "x".into(),
            ..CheckRunUpdate::default()
        },
    )
    .await
    .unwrap();
    assert_eq!(
        seen.lock().unwrap()[1].json()["output"]["title"],
        "Terraform apply"
    );
}

#[tokio::test]
async fn pr_abandoned() {
    for (body, want) in [
        (r#"{"state":"closed","merged":false}"#, true),
        (r#"{"state":"closed","merged":true}"#, false),
        (r#"{"state":"open","merged":false}"#, false),
    ] {
        let (srv, _) = fake_github(move |r| match (r.method.as_str(), r.path.as_str()) {
            ("POST", TOKEN_PATH) => token_ok(),
            ("GET", "/repos/o/r/pulls/7") => Resp::ok(body),
            _ => Resp::status(404, ""),
        })
        .await;
        let c = new_test_client(&srv);
        assert_eq!(c.pr_abandoned("o/r", 7).await.unwrap(), want, "{body}");
    }
}

#[tokio::test]
async fn rerequest_check_run() {
    let (srv, seen) = fake_github(|r| match (r.method.as_str(), r.path.as_str()) {
        (_, TOKEN_PATH) => token_ok(),
        ("POST", "/repos/owner/repo/check-runs/4567/rerequest") => Resp::status(201, ""),
        _ => Resp::status(404, ""),
    })
    .await;
    let c = new_test_client(&srv);
    c.rerequest_check_run("owner/repo", 4567).await.unwrap();
    assert_eq!(seen.lock().unwrap().len(), 2);
}

#[tokio::test]
async fn merge_group_prs() {
    let (srv, _) = fake_github(|r| match r.path.as_str() {
        TOKEN_PATH => token_ok(),
        "/repos/o/r/commits/abc/pulls" => Resp::ok(r#"[{"number":3},{"number":9}]"#),
        _ => Resp::status(404, ""),
    })
    .await;
    let c = new_test_client(&srv);
    assert_eq!(c.merge_group_prs("o/r", "abc").await.unwrap(), vec![3, 9]);
    assert_eq!(
        c.merge_group_prs("o/r", "").await.unwrap_err().to_string(),
        "headSHA is required"
    );
}

#[tokio::test]
async fn merge_queue_parses_and_degrades() {
    for (body, want) in [
        (
            r#"{"data":{"repository":{"defaultBranchRef":{"name":"main"},"mergeQueue":{"entries":{"nodes":[{"position":1,"state":"QUEUED","pullRequest":{"number":12}},{"position":2,"state":"LOCKED","pullRequest":null}]}}}}}"#,
            MergeQueueResult {
                branch: "main".into(),
                entries: vec![
                    MergeQueueEntry {
                        position: 1,
                        pr: 12,
                        state: "QUEUED".into(),
                    },
                    MergeQueueEntry {
                        position: 2,
                        pr: 0,
                        state: "LOCKED".into(),
                    },
                ],
            },
        ),
        (
            r#"{"data":{"repository":{"defaultBranchRef":{"name":"main"},"mergeQueue":null}}}"#,
            MergeQueueResult {
                branch: "main".into(),
                entries: vec![],
            },
        ),
        (
            r#"{"data":{"repository":null},"errors":[{"message":"x"}]}"#,
            MergeQueueResult::default(),
        ),
    ] {
        let (srv, seen) = fake_github(move |r| match r.path.as_str() {
            TOKEN_PATH => token_ok(),
            "/graphql" => Resp::ok(body),
            _ => Resp::status(404, ""),
        })
        .await;
        let c = new_test_client(&srv);
        assert_eq!(c.merge_queue("o/r").await.unwrap(), want);
        let q = seen.lock().unwrap()[1].json();
        assert_eq!(q["variables"]["owner"], "o");
        assert_eq!(q["variables"]["name"], "r");
    }
}

#[test]
fn github_api_base_env() {
    assert_eq!(github_api_base(None), "https://api.github.com");
    assert_eq!(github_api_base(Some("")), "https://api.github.com");
    assert_eq!(
        github_api_base(Some("http://127.0.0.1:9999/")),
        "http://127.0.0.1:9999"
    );
}
