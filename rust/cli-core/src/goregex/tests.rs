use super::*;

#[test]
fn perl_classes_are_ascii_like_go() {
    let re = compile(r"^\d{6,}$").unwrap();
    assert!(re.is_match("123456"));
    assert!(
        !re.is_match("١٢٣٤٥٦"),
        "Arabic-Indic digits are not \\d in Go"
    );
    let re = compile(r"^[\d_]+$").unwrap();
    assert!(re.is_match("1_2"));
    assert!(!re.is_match("١"));
    let re = compile(r"\w+").unwrap();
    assert_eq!(re.find("é_ab").unwrap().as_str(), "_ab");
}

#[test]
fn other_escapes_and_classes_pass_through() {
    assert_eq!(translate(r"a\.b[]x]"), r"a\.b[]x]");
    assert_eq!(translate(r"[^\]]"), r"[^\]]");
    assert!(compile(r"provider\s+\x22").is_ok());
}
