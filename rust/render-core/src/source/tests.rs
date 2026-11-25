use super::*;

// TestIndexRootAndLocalModule, TestRelToEscape, TestBuildMissingModulesJSON and
// TestBuildSkipsUnparseableFile read files; they live with the binary in
// tfstackplan/src/render/tests.rs. The parsing they rely on is checked here.

#[test]
fn resource_blocks_with_lines() {
    let got = resource_blocks(include_str!(
        "../../../../internal/source/testdata/stack/main.tf"
    ))
    .unwrap();
    assert_eq!(
        got,
        [
            ("google_storage_bucket".to_string(), "state".to_string(), 1),
            (
                "google_project_iam_member".to_string(),
                "editor".to_string(),
                5
            ),
        ]
    );
}

#[test]
fn unparseable_file_is_none() {
    assert!(
        resource_blocks(include_str!(
            "../../../../internal/source/testdata/bad/main.tf"
        ))
        .is_none()
    );
}

#[test]
fn module_key_cases() {
    for (input, want) in [
        ("", ""),
        ("module.a", "a"),
        ("module.a.module.b", "a.b"),
        (r#"module.a[0].module.b["x"]"#, "a.b"),
    ] {
        assert_eq!(module_key(input), want, "module_key({input})");
    }
}

#[test]
fn modules_json_entries() {
    let got = parse_modules_json(include_bytes!(
        "../../../../internal/source/testdata/stack/.terraform/modules/modules.json"
    ))
    .unwrap();
    let keys: Vec<&str> = got.iter().map(|(k, _)| k.as_str()).collect();
    assert_eq!(keys, ["", "net", "remote"]);
    assert!(under_dot_terraform(&got[2].1));
    assert!(!under_dot_terraform(&got[1].1));
}

#[test]
fn index_first_declaration_wins() {
    let mut idx = Index::default();
    idx.add_file("", "a.tf", vec![("t".to_string(), "n".to_string(), 3)]);
    idx.add_file("", "b.tf", vec![("t".to_string(), "n".to_string(), 9)]);
    let loc = idx.lookup("", "t", "n").unwrap();
    assert_eq!((loc.file.as_str(), loc.line), ("a.tf", 3));
    assert!(idx.lookup("module.x", "t", "n").is_none());
}
