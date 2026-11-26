use super::*;

fn token(payload: &str) -> String {
    format!("h.{}.s", URL_SAFE_NO_PAD.encode(payload))
}

// Go: TestDecodeWhoamiJWTSubject (cmd/tfstackplan/whoami_test.go), with made-up claims.
#[test]
fn email_wins_over_sub() {
    assert_eq!(
        subject(&token(r#"{"email":"bot@example.com","sub":"123"}"#)).unwrap(),
        "bot@example.com"
    );
    assert_eq!(subject(&token(r#"{"sub":"123"}"#)).unwrap(), "123");
    assert_eq!(subject(&token(r#"{}"#)).unwrap(), "");
}

#[test]
fn unsigned_token_from_go_test() {
    // {"alg":"none"} . {"email":"admin@example.com"} . (empty signature)
    let t = "eyJhbGciOiJub25lIn0.eyJlbWFpbCI6ImFkbWluQGV4YW1wbGUuY29tIn0.";
    assert_eq!(subject(t).unwrap(), "admin@example.com");
}

#[test]
fn malformed_tokens_fail() {
    assert_eq!(
        subject("invalid-jwt-token").unwrap_err(),
        "invalid token format"
    );
    assert_eq!(subject("nodots").unwrap_err(), "invalid token format");
    assert!(subject("a.!!!.c").is_err());
    assert!(subject(&token("not json")).is_err());
}
