use super::*;

#[test]
fn sprint_matches_fmt() {
    assert_eq!(Value::Nil.sprint(), "<nil>");
    assert_eq!(Value::Float(3.0).sprint(), "3");
    assert_eq!(
        Value::Uint(18_446_744_073_709_551_615).sprint(),
        "18446744073709551615"
    );
    assert_eq!(
        Value::Seq(vec![Value::str("a"), Value::Int(1)]).sprint(),
        "[a 1]"
    );
    assert_eq!(
        Value::AnyMap(vec![
            (Value::Int(2), Value::str("b")),
            (Value::Int(1), Value::str("a"))
        ])
        .sprint(),
        "map[1:a 2:b]"
    );
    assert_eq!(Value::list(&["x", "y"]).sprint(), "{[x y]}");
}

#[test]
fn timestamps_print_like_time_time() {
    let utc = Timestamp {
        year: 2024,
        month: 1,
        day: 15,
        hour: 0,
        minute: 0,
        second: 0,
        nanos: 0,
        offset: None,
    };
    assert_eq!(utc.go_string(), "2024-01-15 00:00:00 +0000 UTC");
    assert_eq!(utc.rfc3339(), "2024-01-15T00:00:00Z");
    let off = Timestamp {
        nanos: 500_000_000,
        offset: Some(5 * 3600 + 1800),
        hour: 10,
        ..utc
    };
    assert_eq!(off.go_string(), "2024-01-15 10:00:00.5 +0530 +0530");
    assert_eq!(off.rfc3339(), "2024-01-15T10:00:00.5+05:30");
}

#[test]
fn json_rejects_what_go_rejects() {
    assert!(Value::Float(f64::INFINITY).to_json().is_err());
    assert!(Value::AnyMap(vec![]).to_json().is_err());
}
