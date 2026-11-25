use super::*;

#[test]
fn query_escape_matches_go() {
    assert_eq!(
        query_escape("stacks/foo/default.tflock"),
        "stacks%2Ffoo%2Fdefault.tflock"
    );
    assert_eq!(query_escape("a b+c&d=e"), "a+b%2Bc%26d%3De");
    assert_eq!(query_escape("é"), "%C3%A9");
}

#[test]
fn path_escape_matches_go() {
    assert_eq!(path_escape("my-bucket"), "my-bucket");
    assert_eq!(path_escape("a/b;c,d?e"), "a%2Fb%3Bc%2Cd%3Fe");
    assert_eq!(path_escape("a b$&+:=@"), "a%20b$&+:=@");
}
