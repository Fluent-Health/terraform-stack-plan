use super::*;

#[test]
fn clean_like_filepath() {
    assert_eq!(clean(""), ".");
    assert_eq!(clean("./a//b/../c/"), "a/c");
    assert_eq!(clean("/../a"), "/a");
    assert_eq!(clean("../../a"), "../../a");
    assert_eq!(clean("a/.."), ".");
}

#[test]
fn join_skips_empty_parts() {
    assert_eq!(join(&["", "a", "", "b/"]), "a/b");
    assert_eq!(join(&["", ""]), "");
    assert_eq!(join(&[".", "x"]), "x");
}

#[test]
fn rel_like_filepath() {
    assert_eq!(rel("/a/b", "/a/b/c/d").as_deref(), Some("c/d"));
    assert_eq!(rel("/a/b", "/a/x").as_deref(), Some("../x"));
    assert_eq!(rel("/a", "/a").as_deref(), Some("."));
    assert_eq!(rel("/", "/a").as_deref(), Some("a"));
    assert_eq!(rel("/a", "b"), None);
}

#[test]
fn errors_read_like_go() {
    let dir = tempfile::Builder::new()
        .prefix("tfsp-gofs-")
        .tempdir()
        .unwrap();
    let missing = dir.path().join("nope").to_string_lossy().into_owned();
    assert_eq!(
        read_file(&missing).unwrap_err(),
        format!("open {missing}: no such file or directory")
    );
    let d = dir.path().to_string_lossy().into_owned();
    assert_eq!(
        read_file(&d).unwrap_err(),
        format!("read {d}: is a directory")
    );
}

#[test]
fn write_then_read() {
    let dir = tempfile::Builder::new()
        .prefix("tfsp-gofs-")
        .tempdir()
        .unwrap();
    let p = dir.path().join("f").to_string_lossy().into_owned();
    write_file(&p, b"x").unwrap();
    assert_eq!(read_file(&p).unwrap(), b"x");
    assert!(is_regular_file(&p) && exists(&p));
    assert_eq!(
        read_dir(&dir.path().to_string_lossy()).unwrap(),
        vec![("f".to_string(), false)]
    );
}
