use super::*;

#[test]
fn quote_matches_strconv() {
    assert_eq!(quote("abc"), "\"abc\"");
    assert_eq!(quote("a\"b\\c"), "\"a\\\"b\\\\c\"");
    assert_eq!(quote("tab\there\n"), "\"tab\\there\\n\"");
    assert_eq!(quote("\u{1}\u{7f}"), "\"\\x01\\x7f\"");
    assert_eq!(quote("→ ✅"), "\"→ ✅\"");
    assert_eq!(quote("\u{200b}"), "\"\\u200b\"");
    assert_eq!(quote("\u{a0}"), "\"\\u00a0\"");
    // Go's tables: unassigned and format characters are escaped, emoji are not.
    assert_eq!(quote("\u{378}"), "\"\\u0378\"");
    assert_eq!(quote("\u{890}"), "\"\\u0890\"");
    assert_eq!(quote("\u{E0001}"), "\"\\U000e0001\"");
    assert_eq!(quote("\u{1F600}"), "\"\u{1F600}\"");
}

#[test]
fn floats_match_go() {
    assert_eq!(format_v(1.5), "1.5");
    assert_eq!(format_v(3.0), "3");
    assert_eq!(format_v(100000.0), "100000");
    assert_eq!(format_v(1000000.0), "1e+06");
    assert_eq!(format_v(1e20), "1e+20");
    assert_eq!(format_v(123456.0), "123456");
    assert_eq!(format_v(1234567.5), "1.2345675e+06");
    assert_eq!(format_v(1e21), "1e+21");
    assert_eq!(format_v(1.2345678901234568e24), "1.2345678901234568e+24");
    assert_eq!(format_v(0.0001), "0.0001");
    assert_eq!(format_v(0.00001), "1e-05");
    assert_eq!(format_v(-2.5e-7), "-2.5e-07");
    assert_eq!(format_f(1.2345678901234568e24), "1234567890123456800000000");
}
