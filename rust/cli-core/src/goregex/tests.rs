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
fn errors_read_like_go() {
    assert_eq!(
        compile("[x").unwrap_err(),
        "error parsing regexp: missing closing ]: `[x`"
    );
}
