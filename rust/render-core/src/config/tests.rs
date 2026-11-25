use super::*;

macro_rules! testdata {
    ($name:literal) => {
        load(
            concat!("testdata/", $name),
            include_str!(concat!("../../../../internal/config/testdata/", $name)),
        )
    };
}

// TestDiscoverWalksUpToRepoRoot lives with the binary (it touches the file system):
// tfstackplan/src/render/tests.rs.

#[test]
fn example_client_config_parses() {
    let cfg = load(
        "examples/.tfstackplan.hcl",
        include_str!("../../../../examples/.tfstackplan.hcl"),
    )
    .expect("examples/.tfstackplan.hcl must parse");
    assert!(cfg.classification.is_some());
    // Go also checks the gating `class` block was decoded; this port only validates it.
}

#[test]
fn load_full_ordered() {
    let c = testdata!("full.hcl").unwrap();
    let cl = c.classification.expect("classification block");
    assert_eq!(cl.default.name, "safe");
    assert_eq!(cl.default.icon, "✅");
    let names: Vec<&str> = cl.rules.iter().map(|r| r.name.as_str()).collect();
    assert_eq!(names, ["iam", "destructive"]);
    assert_eq!(cl.rules[0].icon, "⚠️");
    assert_eq!(c.diff.max_attribute_lines, 200);
    assert!(c.diff.detect);
    assert_eq!(c.diff.overrides.len(), 1);
    assert_eq!(c.diff.overrides[0].differ, "yaml");
}

#[test]
fn load_shorthand_default() {
    let cl = testdata!("shorthand.hcl").unwrap().classification.unwrap();
    assert_eq!(cl.default.name, "safe");
    assert_eq!(cl.default.icon, "");
    assert_eq!(cl.rules.len(), 1);
    assert_eq!(cl.rules[0].name, "iam");
}

#[test]
fn load_preset_with_derive() {
    let cl = testdata!("derive.hcl").unwrap().classification.unwrap();
    assert_eq!(cl.rules.len(), 1);
    let r = &cl.rules[0];
    assert_eq!(r.derivations.len(), 1);
    let d = &r.derivations[0];
    assert_eq!(d.attribute, "project");
    assert_eq!(d.from_attribute, "bucket");
    assert!(
        d.type_pattern
            .as_ref()
            .is_some_and(|p| p.is_match("google_storage_managed_folder_iam_member"))
    );
    let m = d
        .pattern
        .find_submatch("dev-svc-build-cache")
        .expect("match");
    assert_eq!(m[d.pattern.subexp_index("value").unwrap()], "dev-svc");
}

#[test]
fn derive_bad_pattern_fails() {
    let err = testdata!("derive_badpattern.hcl").unwrap_err();
    assert!(err.contains("pattern"), "{err}");
}

#[test]
fn unknown_preset_fails() {
    let err = testdata!("badpreset.hcl").unwrap_err();
    assert_eq!(err, r#"unknown preset "does-not-exist" (available: [iam])"#);
}

#[test]
fn diff_resolve() {
    let c = testdata!("full.hcl").unwrap();
    assert_eq!(c.diff.resolve("kubernetes_manifest", "manifest"), "yaml");
    assert_eq!(c.diff.resolve("google_storage_bucket", "content"), "");
    assert_eq!(c.diff.resolve("kubernetes_manifest", "other_attr"), "");
}

#[test]
fn bad_regex_fails() {
    let err = testdata!("badregex.hcl").unwrap_err();
    assert_eq!(
        err,
        "rule \"broken\": bad resource_type_pattern: error parsing regexp: missing closing ]: `[unclosed`"
    );
}

#[test]
fn unknown_top_level_block_fails() {
    let err = testdata!("unknownblock.hcl").unwrap_err();
    assert_eq!(
        err,
        r#"testdata/unknownblock.hcl: unknown top-level block "nonsense""#
    );
}

#[test]
fn load_links() {
    let l = testdata!("links.hcl").unwrap().links.expect("links");
    assert_eq!(l.resource, "https://gh/o/r/blob/{sha}/{file}#L{line}");
    assert_eq!(l.stack, "https://gh/o/r/tree/{sha}/{stack_dir}");
    assert_eq!(l.header.len(), 2);
    assert_eq!(l.header[0].label, "Build #{build_id}");
    assert_eq!(l.header[1].url, "https://gh/o/r/pull/{pr}");
}

#[test]
fn load_emit_attributes() {
    let rules = testdata!("emit.hcl").unwrap().classification.unwrap().rules;
    assert_eq!(rules.len(), 2);
    assert_eq!(rules[0].emit_attributes, ["project"]);
    assert_eq!(rules[1].emit_attributes, ["name", "id"]);
}

// TestParseCacheConfig and TestParseCacheConfigDefaultsAndEnv check the cache block's values,
// which render does not use. The port only checks that such files load.
#[test]
fn parse_cache_config() {
    let src = "cache {\n\tbucket  = \"my-custom-bucket\"\n\tprefix  = \"custom/prefix\"\n\tversion = \"v3\"\n}\n";
    assert!(load(".tfstackplan.hcl", src).is_ok());
}

#[test]
fn parse_cache_config_defaults_and_env() {
    assert!(load(".tfstackplan.hcl", "cache {}\n").is_ok());
}

#[test]
fn decode_errors_read_like_hcl() {
    let src = "classification {\n  rule \"r\" {\n    colour = \"x\"\n  }\n}\n";
    assert_eq!(
        load("p.hcl", src).unwrap_err(),
        r#"rule "r": p.hcl:3,5-11: Unsupported argument; An argument named "colour" is not expected here."#
    );
    let src = "classification {\n  default {\n    icon = \"x\"\n  }\n}\n";
    assert_eq!(
        load("p.hcl", src).unwrap_err(),
        r#"default block: p.hcl:2,11-11: Missing required argument; The argument "name" is required, but no definition was found."#
    );
}

#[test]
fn glob_match_like_path_match() {
    assert!(glob_match("manifest", "manifest"));
    assert!(glob_match("mani*", "manifest"));
    assert!(glob_match("m?nifest", "manifest"));
    assert!(glob_match("[a-m]anifest", "manifest"));
    assert!(!glob_match("[^m]anifest", "manifest"));
    assert!(!glob_match("*", "a/b"));
    assert!(!glob_match("[", "a"));
}
