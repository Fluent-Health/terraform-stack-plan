use super::*;

#[test]
fn format_g_switches_to_exponent_like_go() {
    assert_eq!(format_g(1e6), "1e+06");
    assert_eq!(format_g(123456.7), "123456.7");
    assert_eq!(format_g(1e-5), "1e-05");
    assert_eq!(format_g(0.0001), "0.0001");
    assert_eq!(format_g(1.5e300), "1.5e+300");
    assert_eq!(format_g(-0.0), "-0");
    assert_eq!(format_g(f64::INFINITY), "+Inf");
    assert_eq!(format_g(f64::NAN), "NaN");
}

#[test]
fn format_f_never_uses_exponent() {
    assert_eq!(format_f(1e21), "1000000000000000000000");
    assert_eq!(format_f(1.25e-7), "0.000000125");
    assert_eq!(format_f(42.0), "42");
}

#[test]
fn json_float_trims_exponent_zero() {
    assert_eq!(format_json_float(1e-7), "1e-7");
    assert_eq!(format_json_float(1e21), "1e+21");
    assert_eq!(format_json_float(1e20), "100000000000000000000");
    assert_eq!(format_json_float(0.5), "0.5");
}

#[test]
fn exact_i64_matches_go_conversion() {
    assert_eq!(as_exact_i64(3.0), Some(3));
    assert_eq!(as_exact_i64(3.5), None);
    assert_eq!(as_exact_i64(9_223_372_036_854_775_808.0), None);
    assert_eq!(as_exact_i64(-9_223_372_036_854_775_808.0), Some(i64::MIN));
}

#[test]
fn quote_escapes_like_strconv() {
    assert_eq!(quote("a\"b\\c"), r#""a\"b\\c""#);
    assert_eq!(quote("\u{a0}"), r#""\u00a0""#);
    assert_eq!(quote("\u{7f}"), r#""\x7f""#);
    assert_eq!(quote("\u{1}\n\t"), r#""\x01\n\t""#);
    assert_eq!(quote("é日本😀"), "\"é日本😀\"");
    assert_eq!(quote("\u{200b}"), r#""\u200b""#);
    assert_eq!(quote("\u{e0001}"), r#""\U000e0001""#);
}

#[test]
fn is_print_matches_go_tables() {
    assert!(is_print(' '));
    assert!(!is_print('\u{ad}'));
    assert!(is_print('\u{a1}'));
    assert!(!is_print('\u{feff}'));
    assert!(is_print('😀'));
}
