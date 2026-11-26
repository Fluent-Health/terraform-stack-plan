use super::*;

#[test]
fn identical_runs_need_no_wildcard() {
    assert_eq!(merge("stdout", &["same\n"; 3], false).unwrap(), "same\n");
}

#[test]
fn a_difference_fails_without_opt_in() {
    let err = merge("stdout", &["id=1\n", "id=1\n", "id=2\n"], false).unwrap_err();
    assert!(err.to_string().contains("wildcards = true"), "{err}");
}

#[test]
fn opted_in_wildcards_cover_only_the_differing_part() {
    let got = merge(
        "stdout",
        &[
            "a\nrun id=123 ok\nz\n",
            "a\nrun id=98 ok\nz\n",
            "a\nrun id=4 ok\nz\n",
        ],
        true,
    )
    .unwrap();
    assert_eq!(got, "a\nrun id=[..] ok\nz\n");
}

#[test]
fn a_long_difference_is_refused_even_with_opt_in() {
    let long = "x".repeat(MAX_WILDCARD + 1);
    let other = format!("a{long}");
    let err = merge("stdout", &[&long, &other, "y"], true).unwrap_err();
    assert!(err.to_string().contains("more than a [..]"), "{err}");
}

#[test]
fn different_line_counts_fail() {
    assert!(merge("stdout", &["a\n", "a\nb\n", "a\n"], true).is_err());
}

#[test]
fn merge_wildcards_whole_random_tokens() {
    let got = merge(
        "stdout",
        &[
            "dir /tmp/x-12346\n",
            "dir /tmp/x-98716\n",
            "dir /tmp/x-55516\n",
        ],
        true,
    )
    .unwrap();
    assert_eq!(got, "dir /tmp/x-[..]\n");
}
