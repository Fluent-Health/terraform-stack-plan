use super::*;

#[test]
fn expiry_keeps_wall_clock_and_writes_z() {
    assert_eq!(
        format_expiry("2026-01-02T03:04:05Z").unwrap(),
        "2026-01-02T03:04:05Z"
    );
    assert_eq!(
        format_expiry("2026-01-02T10:04:05.123456+05:30").unwrap(),
        "2026-01-02T10:04:05Z"
    );
    assert_eq!(format_expiry("").unwrap(), "0001-01-01T00:00:00Z");
    assert!(format_expiry("yesterday").is_err());
}

#[test]
fn line_pads_the_stack_to_40() {
    assert_eq!(
        line("stacks/dev/a", 7, "2026-01-02T03:04:05Z"),
        "  stack=stacks/dev/a                              pr=#7  expires=2026-01-02T03:04:05Z\n"
    );
}
