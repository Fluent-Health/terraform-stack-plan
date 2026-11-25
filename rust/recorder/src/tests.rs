use super::*;

#[test]
fn merge_wildcards_only_the_differing_part() {
    let got = merge("a\nrun id=123 ok\nz\n", "a\nrun id=98 ok\nz\n").unwrap();
    assert_eq!(got, "a\nrun id=[..] ok\nz\n");
}

#[test]
fn merge_keeps_identical_output() {
    assert_eq!(merge("same\n", "same\n").unwrap(), "same\n");
}

#[test]
fn merge_rejects_different_line_counts() {
    assert!(merge("a\n", "a\nb\n").is_err());
}

#[test]
fn merge_wildcards_whole_random_tokens() {
    let got = merge("dir /tmp/x-12346\n", "dir /tmp/x-98716\n").unwrap();
    assert_eq!(got, "dir /tmp/x-[..]\n");
}
