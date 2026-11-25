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
}

#[test]
fn floats_match_go() {
    assert_eq!(format_v(1.5), "1.5");
    assert_eq!(format_v(3.0), "3");
    assert_eq!(format_v(1e20), "100000000000000000000");
    assert_eq!(format_v(1e21), "1e+21");
    assert_eq!(format_v(1.2345678901234568e24), "1.2345678901234568e+24");
    assert_eq!(format_v(0.0001), "0.0001");
    assert_eq!(format_v(0.00001), "1e-05");
    assert_eq!(format_v(-2.5e-7), "-2.5e-07");
    assert_eq!(format_e(1e21), "1e+21");
    assert_eq!(format_f(1.2345678901234568e24), "1234567890123456800000000");
}
