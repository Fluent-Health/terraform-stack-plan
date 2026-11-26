use super::*;

const HCL_BASE: &str = r#"
env_uniqueness {
  project_token_template = "acme-{env}"

  source {
    tier_input = "tier_class"
  }

  allow {
    unit   = "svc/api/instances/api"
    key    = "shared_secret"
    envs   = ["dev", "prod"]
    reason = "shared sandbox credential by design"
  }
}
"#;

const UNIT_CLEAN: &str = r#"
environments:
  dev:
    inputs:
      tier_class: nonprod
      shared_secret: "12345678-1234-1234-1234-123456789012"
      project_id: app-dev
  prod:
    inputs:
      tier_class: prod
      shared_secret: "12345678-1234-1234-1234-123456789012"
      project_id: app-prod
"#;

const UNIT_UNJUSTIFIED: &str = r#"
environments:
  dev:
    inputs:
      tier_class: nonprod
      shared_secret: "12345678-1234-1234-1234-123456789012"
      project_id: app-dev
      account_id: "999999999999"
  prod:
    inputs:
      tier_class: prod
      shared_secret: "12345678-1234-1234-1234-123456789012"
      project_id: app-prod
      account_id: "999999999999"
"#;

fn repo(hcl: &str, unit: &str) -> tempfile::TempDir {
    let dir = tempfile::tempdir().unwrap();
    std::fs::write(dir.path().join(".tfstackplan.hcl"), hcl).unwrap();
    let inst = dir.path().join("components/svc/api/instances");
    std::fs::create_dir_all(&inst).unwrap();
    std::fs::write(inst.join("api.tm.yml"), unit).unwrap();
    dir
}

fn run_in(dir: &tempfile::TempDir, extra: &[&str]) -> ExitCode {
    let mut args = vec![
        "--dir".to_string(),
        dir.path().to_string_lossy().into_owned(),
    ];
    args.extend(extra.iter().map(|s| s.to_string()));
    run(&args)
}

// Go: TestRunUniquenessClean.
#[test]
fn clean() {
    assert_eq!(run_in(&repo(HCL_BASE, UNIT_CLEAN), &[]), ExitCode::SUCCESS);
}

// Go: TestRunUniquenessUnjustifiedViolation.
#[test]
fn unjustified_violation() {
    assert_eq!(
        run_in(&repo(HCL_BASE, UNIT_UNJUSTIFIED), &[]),
        ExitCode::from(1)
    );
}

// Go: TestRunUniquenessNoBlock.
#[test]
fn no_block() {
    assert_eq!(run_in(&repo("", UNIT_CLEAN), &[]), ExitCode::from(2));
}

// Go: TestRunUniquenessFormatJSON.
#[test]
fn format_json() {
    assert_eq!(
        run_in(&repo(HCL_BASE, UNIT_CLEAN), &["--format", "json"]),
        ExitCode::SUCCESS
    );
}

// Go: TestRunUniquenessBadFormat.
#[test]
fn bad_format() {
    assert_eq!(
        run_in(&repo(HCL_BASE, UNIT_CLEAN), &["--format", "xml"]),
        ExitCode::from(2)
    );
}

// Go: TestDispatchRoutesUniqueness.
#[test]
fn dispatch_routes_uniqueness() {
    let ported = crate::PORTED
        .iter()
        .find(|(n, _)| *n == "uniqueness")
        .unwrap()
        .1;
    let dir = repo(HCL_BASE, UNIT_CLEAN);
    assert_eq!(
        ported(&[
            "--dir".to_string(),
            dir.path().to_string_lossy().into_owned()
        ]),
        ExitCode::SUCCESS
    );
}

#[test]
fn load_units_walks_and_matches_like_go() {
    let dir = repo(HCL_BASE, UNIT_CLEAN);
    std::fs::create_dir_all(dir.path().join("components/x/other")).unwrap();
    std::fs::write(dir.path().join("components/x/other/api.tm.yml"), UNIT_CLEAN).unwrap();
    let root = dir.path().to_string_lossy().into_owned();
    let src = SourceBlock::default();
    let units = load_units(&root, &src).unwrap();
    assert_eq!(units.len(), 1);
    assert_eq!(units[0].id, "svc/api/instances/api");
    let err = load_units("/definitely/missing", &src).unwrap_err();
    assert_eq!(
        err,
        "env_uniqueness: walking /definitely/missing: lstat /definitely/missing: no such file or directory"
    );
}
