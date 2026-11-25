use super::*;

#[test]
fn perl_classes_are_ascii_only() {
    let re = GoRegex::new(r"^\d+$").unwrap();
    assert!(re.is_match("123"));
    assert!(!re.is_match("١٢٣"));
    let w = GoRegex::new(r"^\w+$").unwrap();
    assert!(!w.is_match("é"));
    let class = GoRegex::new(r"^[\d_]+$").unwrap();
    assert!(class.is_match("1_2"));
    assert!(!class.is_match("٣"));
    let b = GoRegex::new(r"\bx\b").unwrap();
    assert!(b.is_match("a x b"));
}

#[test]
fn errors_read_like_go() {
    assert_eq!(
        GoRegex::new("([unclosed").unwrap_err(),
        "error parsing regexp: missing closing ]: `[unclosed`"
    );
    assert_eq!(
        GoRegex::new("(a").unwrap_err(),
        "error parsing regexp: missing closing ): `(a`"
    );
    assert_eq!(
        GoRegex::new("a)").unwrap_err(),
        "error parsing regexp: unexpected ): `a)`"
    );
    assert_eq!(
        GoRegex::new("*a").unwrap_err(),
        "error parsing regexp: missing argument to repetition operator: `*`"
    );
    assert_eq!(
        GoRegex::new(r"\q").unwrap_err(),
        "error parsing regexp: invalid escape sequence: `\\q`"
    );
}

#[test]
fn submatches_and_names() {
    let re = GoRegex::new(r"^(?P<value>.+)-build-cache$").unwrap();
    assert_eq!(re.num_subexp(), 1);
    assert_eq!(re.subexp_index("value"), Some(1));
    assert_eq!(re.subexp_index("nope"), None);
    assert_eq!(
        re.find_submatch("p1-build-cache").unwrap(),
        vec!["p1-build-cache", "p1"]
    );
    let opt = GoRegex::new("(a)|(b)").unwrap();
    assert_eq!(opt.find_submatch("b").unwrap(), vec!["b", "", "b"]);
}
