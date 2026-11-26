use super::*;

fn lines(s: &str) -> Vec<String> {
    split_lines(s)
}

#[test]
fn split_lines_adds_a_final_newline_piece() {
    assert_eq!(split_lines("a\n"), vec!["a\n", "\n"]);
    assert_eq!(split_lines("a"), vec!["a\n"]);
    assert_eq!(split_lines(""), vec!["\n"]);
    assert_eq!(split_lines("a\nb"), vec!["a\n", "b\n"]);
}

#[test]
fn unified_diff_hunks_and_ranges() {
    let out = unified_diff(&lines("a\nb\nc\n"), &lines("a\nB\nc\n"), 3);
    assert_eq!(out, "@@ -1,4 +1,4 @@\n a\n-b\n+B\n c\n \n");
}

#[test]
fn unified_diff_of_equal_input_is_empty() {
    assert_eq!(unified_diff(&lines("x\n"), &lines("x\n"), 3), "");
}

#[test]
fn unified_diff_splits_distant_changes() {
    let a: String = (0..20).map(|i| format!("{i}\n")).collect();
    let b: String = (0..20)
        .map(|i| match i {
            1 => "one\n".to_string(),
            18 => "eighteen\n".to_string(),
            _ => format!("{i}\n"),
        })
        .collect();
    let out = unified_diff(&lines(&a), &lines(&b), 1);
    assert_eq!(out.matches("@@ -").count(), 2, "{out}");
    assert!(
        out.ends_with("@@ -18,3 +18,3 @@\n 17\n-18\n+eighteen\n 19\n"),
        "{out}"
    );
    assert!(
        out.starts_with("@@ -1,3 +1,3 @@\n 0\n-1\n+one\n 2\n"),
        "{out}"
    );
}

#[test]
fn empty_ranges_use_previous_line() {
    let out = unified_diff(&lines(""), &lines("a\n"), 0);
    assert_eq!(out, "@@ -0,0 +1 @@\n+a\n");
}
