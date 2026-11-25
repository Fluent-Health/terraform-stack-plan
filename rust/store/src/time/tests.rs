use super::*;

#[test]
fn writes_go_time_string() {
    let ts: Timestamp = "2026-06-24T12:30:00.1Z".parse().unwrap();
    assert_eq!(to_sql(ts), "2026-06-24 12:30:00.1 +0000 UTC");
    let ts: Timestamp = "2026-06-24T12:30:00Z".parse().unwrap();
    assert_eq!(to_sql(ts), "2026-06-24 12:30:00 +0000 UTC");
    let ts: Timestamp = "2026-06-24T12:30:00.123456789Z".parse().unwrap();
    assert_eq!(to_sql(ts), "2026-06-24 12:30:00.123456789 +0000 UTC");
}

#[test]
fn reads_every_form() {
    let want: Timestamp = "2026-06-24T12:30:00.5Z".parse().unwrap();
    assert_eq!(from_sql("2026-06-24 12:30:00.5 +0000 UTC").unwrap(), want);
    assert_eq!(from_sql("2026-06-24T12:30:00.5Z").unwrap(), want);
    assert_eq!(
        from_sql("2026-06-24 12:30:00").unwrap(),
        "2026-06-24T12:30:00Z".parse::<Timestamp>().unwrap()
    );
    assert!(from_sql("yesterday").is_err());
}
