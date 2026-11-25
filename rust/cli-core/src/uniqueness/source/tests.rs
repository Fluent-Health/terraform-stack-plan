use super::*;

fn load(rel: &str, content: &str) -> Result<Unit, LoadError> {
    load_unit(rel, content, "components/", "environments", "inputs")
}

// Go: TestLoadUnits (the fixture internal/uniqueness/testdata/repo).
#[test]
fn load_units_fixture() {
    let src = std::fs::read_to_string(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../../internal/uniqueness/testdata/repo/components/x/api/instances/api.tm.yml"
    ))
    .unwrap();
    let rel = "components/x/api/instances/api.tm.yml";
    assert!(match_glob(rel, "components/", "instances/*.tm.yml").unwrap());
    let u = load(rel, &src).unwrap();
    assert_eq!(u.id, "x/api/instances/api");
    assert_eq!(u.envs, vec!["dev", "prod"]);
    assert_eq!(u.inputs["prod"]["project_id"], Value::str("app-prod"));
    assert_eq!(u.inputs["dev"]["project_id"], Value::str("app-dev"));
    assert_eq!(u.inputs["prod"]["tier_class"], Value::str("prod"));
    assert_eq!(u.inputs["dev"]["tier_class"], Value::str("nonprod"));
}

// Go: TestLoadUnitsCustomGlobAndPaths.
#[test]
fn custom_glob_and_paths() {
    let (prefix, suffix) = split_glob("stacks/**/manifests/*.tm.yml").unwrap();
    assert_eq!(
        (prefix.as_str(), suffix.as_str()),
        ("stacks/", "manifests/*.tm.yml")
    );
    assert!(match_glob("stacks/y/manifests/svc.tm.yml", &prefix, &suffix).unwrap());
    assert!(!match_glob("stacks/y/other/svc.tm.yml", &prefix, &suffix).unwrap());
    assert!(!match_glob("elsewhere/manifests/svc.tm.yml", &prefix, &suffix).unwrap());
    let u = load_unit(
        "stacks/y/manifests/svc.tm.yml",
        "\nenvs:\n  stage:\n    config:\n      region: eu-west-1\n",
        &prefix,
        "envs",
        "config",
    )
    .unwrap();
    assert_eq!(u.id, "y/manifests/svc");
    assert_eq!(u.envs, vec!["stage"]);
    assert_eq!(u.inputs["stage"]["region"], Value::str("eu-west-1"));
}

// Go: TestLoadUnitsSkipsNoEnvironmentsPath.
#[test]
fn skips_no_environments_path() {
    let err = load(
        "components/b/instances/x.tm.yml",
        "\nmetadata:\n  name: x\nspec:\n  inputs:\n    project_id: x\n",
    )
    .unwrap_err();
    assert_eq!(err, LoadError::NoEnvironments);
    assert!(
        load(
            "components/a/instances/a.tm.yml",
            "\nenvironments:\n  dev:\n    inputs:\n      x: 1\n"
        )
        .is_ok()
    );
}

// Go: TestLoadUnitsEmptyEnvironmentsMapLoads.
#[test]
fn empty_environments_map_loads() {
    let u = load("components/z/instances/e.tm.yml", "\nenvironments: {}\n").unwrap();
    assert!(u.envs.is_empty());
}

// Go: TestLoadUnitsMalformedEnvironmentsStillErrors.
#[test]
fn malformed_environments_still_errors() {
    let err = load(
        "components/z/instances/bad.tm.yml",
        "\nenvironments: \"not a map\"\n",
    )
    .unwrap_err();
    assert_eq!(
        err,
        LoadError::Fatal(
            "env_uniqueness: components/z/instances/bad.tm.yml: \"environments\" is not a map"
                .into()
        )
    );
}

// Go: TestLoadUnitsEmptyInputs.
#[test]
fn empty_inputs() {
    let u = load(
        "components/z/instances/empty.tm.yml",
        "\nenvironments:\n  dev: {}\n",
    )
    .unwrap();
    assert!(u.inputs["dev"].is_empty());
}

// Go: TestLoadUnitsDeepNesting (the fixture internal/uniqueness/testdata/deep).
#[test]
fn deep_nesting() {
    let src = std::fs::read_to_string(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../../internal/uniqueness/testdata/deep/components/a/b/c/instances/deep.tm.yml"
    ))
    .unwrap();
    let rel = "components/a/b/c/instances/deep.tm.yml";
    assert!(match_glob(rel, "components/", "instances/*.tm.yml").unwrap());
    let u = load(rel, &src).unwrap();
    assert_eq!(u.id, "a/b/c/instances/deep");
    assert_eq!(u.envs, vec!["dev", "prod"]);
    assert_eq!(u.inputs["prod"]["project_id"], Value::str("app-prod"));
}

// Go: TestLoadUnitsUnparseableYAML.
#[test]
fn unparseable_yaml() {
    let err = load(
        "components/z/instances/bad.tm.yml",
        "\nenvironments: [dev\n    inputs: {project_id: app-dev}\n",
    )
    .unwrap_err();
    assert!(
        matches!(&err, LoadError::Fatal(m) if m.starts_with("env_uniqueness: parsing components/z/instances/bad.tm.yml: yaml: ")),
        "{err:?}"
    );
}

// Go: TestLoadUnitsMalformedGlob.
#[test]
fn malformed_glob() {
    assert_eq!(
        split_glob("components/instances/*.tm.yml").unwrap_err(),
        "env_uniqueness: source glob \"components/instances/*.tm.yml\" must have the <dir>/**/<pattern> shape"
    );
}

#[test]
fn bad_block_and_inputs_shapes() {
    let err = load(
        "components/z/instances/b.tm.yml",
        "environments:\n  dev: [1]\n",
    )
    .unwrap_err();
    assert_eq!(
        err,
        LoadError::Fatal(
            "env_uniqueness: components/z/instances/b.tm.yml: environment \"dev\": block is not a map ([]interface {})".into()
        )
    );
    let err = load(
        "components/z/instances/b.tm.yml",
        "environments:\n  dev:\n    inputs: 3\n",
    )
    .unwrap_err();
    assert_eq!(
        err,
        LoadError::Fatal(
            "env_uniqueness: components/z/instances/b.tm.yml: environment \"dev\": \"inputs\" is not a map (int)".into()
        )
    );
}
