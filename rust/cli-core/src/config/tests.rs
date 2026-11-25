use super::*;

fn load(src: &str) -> Result<Config, String> {
    parse(src, "test.hcl")
}

// Go: TestEnvUniquenessBlock.
#[test]
fn env_uniqueness_block() {
    let cfg = load(
        r#"
env_uniqueness {
  protected_tier          = "prod"
  project_token_template  = "acme-{env}"

  environment "dev"  { tier = "nonprod" }
  environment "prod" { tier = "prod" }

  source {
    tier_input = "tier_class"
  }

  extra_env_tokens = {
    dev = ["development"]
  }
  extra_scoped_segments = ["svc/api"]
  extra_key_patterns    = ["*_url"]

  allow {
    unit   = "svc/api"
    key    = "app-dev"
    envs   = ["dev", "test"]
    reason = "shared sandbox project by design"
  }
}
"#,
    )
    .unwrap();
    let eu = cfg.env_uniqueness.unwrap();
    assert_eq!(eu.protected_tier, "prod");
    assert_eq!(eu.project_token_template, "acme-{env}");
    assert_eq!(
        eu.environments,
        vec![
            EnvBlock {
                name: "dev".into(),
                tier: "nonprod".into()
            },
            EnvBlock {
                name: "prod".into(),
                tier: "prod".into()
            },
        ]
    );
    assert_eq!(eu.source.tier_input, "tier_class");
    assert_eq!(eu.source.glob, "components/**/instances/*.tm.yml");
    assert_eq!(eu.source.environments_path, "environments");
    assert_eq!(eu.source.inputs_path, "inputs");
    assert_eq!(
        eu.extra_env_tokens,
        vec![("dev".to_string(), vec!["development".to_string()])]
    );
    assert_eq!(eu.extra_scoped_segments, vec!["svc/api"]);
    assert_eq!(eu.extra_key_patterns, vec!["*_url"]);
    assert_eq!(eu.allows.len(), 1);
    let a = &eu.allows[0];
    assert_eq!(
        (a.unit.as_str(), a.key.as_str(), a.reason.as_str()),
        ("svc/api", "app-dev", "shared sandbox project by design")
    );
    assert_eq!(a.envs, vec!["dev", "test"]);
}

// Go: TestEnvUniquenessDefaultsNoSource.
#[test]
fn env_uniqueness_defaults_no_source() {
    let eu = load("env_uniqueness {}\n").unwrap().env_uniqueness.unwrap();
    assert_eq!(eu.protected_tier, "prod");
    assert_eq!(eu.source.glob, "components/**/instances/*.tm.yml");
    assert_eq!(eu.source.environments_path, "environments");
    assert_eq!(eu.source.inputs_path, "inputs");
}

// Go: TestEnvUniquenessAllowRequiresReason.
#[test]
fn env_uniqueness_allow_requires_reason() {
    let err = load(
        r#"
env_uniqueness {
  allow {
    unit   = "svc/api"
    key    = "app-dev"
    envs   = ["dev"]
    reason = "   "
  }
}
"#,
    )
    .unwrap_err();
    assert_eq!(
        err,
        "env_uniqueness: allow svc/api.app-dev: reason is required"
    );
}

// Go: TestUnknownTopLevelBlockFails.
#[test]
fn unknown_top_level_block_fails() {
    let err = load("bogus {}\n").unwrap_err();
    assert_eq!(err, "test.hcl: unknown top-level block \"bogus\"");
}

// Go: TestExampleClientConfigParses (the parts this module decodes).
#[test]
fn example_configs_parse() {
    for f in [
        "../examples/.tfstackplan.hcl",
        "../examples/serve.tfstackplan.hcl",
        "../examples/ui.tfstackplan.hcl",
    ] {
        let path = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("..")
            .join(f);
        let src = std::fs::read_to_string(&path).unwrap();
        parse(&src, f).unwrap_or_else(|e| panic!("{f}: {e}"));
    }
}

#[test]
fn servers_and_url_selection() {
    let cfg = load(
        r#"
server { url = "https://default.example" }
server "staging" {
  url         = "https://staging.example"
  environment = "stage"
}
"#,
    )
    .unwrap();
    assert_eq!(cfg.servers.len(), 2);
    assert_eq!(cfg.server.as_ref().unwrap().url, "https://default.example");
    assert_eq!(cfg.server_url_for("stage"), "https://staging.example");
    assert_eq!(cfg.server_url_for("staging"), "https://staging.example");
    assert_eq!(cfg.server_url_for("prod"), "https://default.example");
    assert_eq!(cfg.server_url_for(""), "https://default.example");
}

#[test]
fn server_rejects_unknown_arguments() {
    let err = load("server { nope = 1 }\n").unwrap_err();
    assert!(
        err.starts_with("server block: Unsupported argument"),
        "{err}"
    );
}
