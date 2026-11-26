use aws_lc_rs::hmac;

use super::*;

/// The test suite's `signPayload`.
fn sign_payload(secret: &str, body: &[u8]) -> String {
    let key = hmac::Key::new(hmac::HMAC_SHA256, secret.as_bytes());
    format!("sha256={}", hex::encode(hmac::sign(&key, body).as_ref()))
}

#[test]
fn signature_accepts_a_valid_mac() {
    let body = br#"{"action":"closed","pull_request":{"number":1}}"#;
    let header = sign_payload("correct-secret", body);
    assert!(verify_github_signature(b"correct-secret", &header, body));
}

#[test]
fn signature_rejects_bad_hex() {
    // TestGitHubWebhookRejectsBadSignature
    assert!(!verify_github_signature(
        b"correct-secret",
        "sha256=badhex",
        b"{}"
    ));
}

#[test]
fn signature_rejects_wrong_secret_body_prefix_and_missing_header() {
    let body = b"payload";
    let good = sign_payload("s", body);
    assert!(!verify_github_signature(b"other", &good, body));
    assert!(!verify_github_signature(b"s", &good, b"payload2"));
    assert!(!verify_github_signature(
        b"s",
        &good.replace("sha256=", "sha1="),
        body
    ));
    assert!(!verify_github_signature(b"s", "", body));
    // A truncated MAC is not a prefix match.
    assert!(!verify_github_signature(
        b"s",
        &good[..good.len() - 2],
        body
    ));
}

#[test]
fn check_names() {
    assert_eq!(check_run_name(""), "plan");
    assert_eq!(check_run_name("staging"), "plan/staging");
    assert_eq!(consolidated_check_name(""), "terraform");
    assert_eq!(consolidated_check_name("nonprod"), "terraform/nonprod");
    assert_eq!(apply_lock_name(""), "apply-lock");
    assert_eq!(apply_lock_name("prod"), "apply-lock/prod");
}
