use super::*;

#[test]
fn load_valid() {
    let path = "testdata/moves.json";
    let m = Manifest::parse(
        path,
        include_bytes!("../../../../internal/moveset/testdata/moves.json"),
    )
    .unwrap();
    let stack = m.0.keys().next().expect("one stack in the fixture").clone();
    assert!(m.targets(&stack).covers("module.content_library"));
    assert!(m.targets("no/such/stack").is_empty());
}

#[test]
fn set_covers() {
    let s = Set::new([
        "module.content_library",
        "module.standalone.google_project_iam_member.x",
    ]);
    let cases = [
        ("module.content_library", true),
        (
            "module.content_library.google_project_iam_member.editor",
            true,
        ),
        ("module.content_library.google_storage_bucket.b", true),
        ("module.standalone.google_project_iam_member.x", true),
        (
            r#"module.standalone.google_project_iam_member.x["roles/secretmanager.viewer"]"#,
            true,
        ),
        ("module.standalone.google_project_iam_member.x_other", false),
        ("module.content_library_other.google_x.y", false),
        ("module.other.google_project_iam_member.z", false),
    ];
    for (addr, want) in cases {
        assert_eq!(s.covers(addr), want, "covers({addr})");
    }
}

#[test]
fn load_empty_path_is_noop() {
    // Load("") is the binary's job (no manifest, no read); the empty manifest it uses:
    assert!(Manifest::default().targets("anything").is_empty());
}

#[test]
fn parse_errors_read_like_go() {
    assert_eq!(
        Manifest::parse("m.json", b"{").unwrap_err(),
        r#"parse state-moves "m.json": unexpected end of JSON input"#
    );
    assert_eq!(
        Manifest::parse("m.json", br#"{"s": "x"}"#).unwrap_err(),
        r#"parse state-moves "m.json": json: cannot unmarshal string into Go value of type []string"#
    );
}
