use jiff::{SignedDuration, Timestamp};

use super::{Codec, Session};

fn session(email: &str, ttl: SignedDuration) -> Session {
    Session {
        email: email.into(),
        expires: Timestamp::now() + ttl,
    }
}

// Go: TestSessionCodec.
#[test]
fn session_codec() {
    let c = Codec::new("s3cret").expect("codec");
    let tok = c
        .seal(&session("a@example.com", SignedDuration::from_hours(1)))
        .expect("seal");
    let s = c.open(&tok).expect("round trip");
    assert_eq!(s.email, "a@example.com");

    // Expired sessions are rejected.
    let tok2 = c
        .seal(&session("a@example.com", SignedDuration::from_mins(-1)))
        .expect("seal");
    assert_eq!(c.open(&tok2).err().as_deref(), Some("session expired"));

    // Tampering breaks the AEAD seal.
    let tampered = format!("{}xx", &tok[..tok.len() - 2]);
    assert!(c.open(&tampered).is_err(), "tampered token accepted");

    // A different secret cannot open it.
    let c2 = Codec::new("other").expect("codec");
    assert!(c2.open(&tok).is_err(), "foreign secret opened the session");
}

#[test]
fn empty_secret_is_refused() {
    assert_eq!(
        Codec::new("").err().as_deref(),
        Some("session secret is empty")
    );
}

/// A cookie Go's codec sealed (secret "s3cret", expiring 2099) opens here: a cutover
/// between the two implementations logs nobody out.
#[test]
fn opens_a_go_sealed_session() {
    const GO_SEALED: &str = "U-EM7vv9gw6LmQb_N9TMNbwb-fALS0lf6Atf9_-QXfmkZCakBeC4vw6bhAmXoxLzY5YYmBqqK-8lsvtwowOHDAI3Her-nsQJZYiSTW55XxDeYABwoF0";
    let c = Codec::new("s3cret").expect("codec");
    let s = c.open(GO_SEALED).expect("go-sealed session");
    assert_eq!(s.email, "a@example.com");
    assert_eq!(
        s.expires,
        "2099-01-01T00:00:00Z".parse::<Timestamp>().expect("ts")
    );
}

#[test]
fn short_and_undecodable_tokens_are_rejected() {
    let c = Codec::new("s3cret").expect("codec");
    assert_eq!(
        c.open("AAAA").err().as_deref(),
        Some("session: token too short")
    );
    assert!(
        c.open("not base64!")
            .err()
            .is_some_and(|e| e.starts_with("session: token decode:"))
    );
    assert!(c.open("").is_err());
}
