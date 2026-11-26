use super::*;

#[test]
fn clean_and_join_match_go() {
    assert_eq!(clean(""), ".");
    assert_eq!(clean("./a//b/../c/"), "a/c");
    assert_eq!(clean("/../a"), "/a");
    assert_eq!(clean("../a/.."), "..");
    assert_eq!(
        join(&[".", "stacks/a", "_tfsp_move.PR-1.tf"]),
        "stacks/a/_tfsp_move.PR-1.tf"
    );
    assert_eq!(join(&["/tmp/x/", "a"]), "/tmp/x/a");
    assert_eq!(join(&["", ""]), "");
}
