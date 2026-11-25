use std::path::Path;

use super::{UiConfig, load};

fn load_ui(hcl: &str) -> Result<Option<UiConfig>, String> {
    let dir = tempfile::tempdir().map_err(|e| e.to_string())?;
    let p = dir.path().join("ui.hcl");
    std::fs::write(&p, hcl).map_err(|e| e.to_string())?;
    load(&p)
}

// Go: TestUIConfigParses.
#[test]
fn ui_config_parses() {
    let u = load_ui(
        r#"
ui {
  public_base_url    = "https://ui.example.com/"
  session_secret_env = "TFSTACKPLAN_UI_SESSION_SECRET"
  tier "nonprod" { url = "https://nonprod.example.com/" }
  tier "prod" {
    url      = "https://prod.example.com"
    audience = "https://prod-audience.example.com"
  }
  oauth {
    client_id         = "1234.apps.googleusercontent.com"
    client_secret_env = "TFSTACKPLAN_UI_OAUTH_SECRET"
    allowed_domain    = "Example.COM"
  }
}
"#,
    )
    .expect("load")
    .expect("ui block not parsed");
    assert_eq!(u.public_base_url, "https://ui.example.com", "trimmed");
    assert_eq!(u.session_secret_env, "TFSTACKPLAN_UI_SESSION_SECRET");
    assert_eq!(u.tiers.len(), 2);
    assert_eq!(
        (u.tiers[0].name.as_str(), u.tiers[0].url.as_str()),
        ("nonprod", "https://nonprod.example.com")
    );
    assert_eq!(
        u.tiers[0].audience, "https://nonprod.example.com",
        "audience defaults to url"
    );
    assert_eq!(
        u.tiers[1].audience, "https://prod-audience.example.com",
        "explicit audience wins"
    );
    let o = u.oauth.expect("oauth");
    assert_eq!(o.client_id, "1234.apps.googleusercontent.com");
    assert_eq!(o.allowed_domain, "example.com", "allowed_domain lowercased");
}

// Go: TestUIConfigValidation.
#[test]
fn ui_config_validation() {
    let cases = [
        ("no tiers", "ui {\n}", "at least one tier"),
        (
            "missing url",
            "ui {\n  tier \"a\" {\n  }\n}",
            "url is required",
        ),
        (
            "duplicate tier",
            "ui {\n  tier \"a\" {\n    url = \"https://x\"\n  }\n  tier \"a\" {\n    url = \"https://y\"\n  }\n}",
            "duplicate tier",
        ),
        (
            "oauth missing client",
            "ui {\n  tier \"a\" {\n    url = \"https://x\"\n  }\n  oauth {\n    allowed_domain = \"x.com\"\n  }\n}",
            "client_id and client_secret_env",
        ),
        (
            "oauth missing domain",
            "ui {\n  tier \"a\" {\n    url = \"https://x\"\n  }\n  oauth {\n    client_id = \"c\"\n    client_secret_env = \"E\"\n  }\n}",
            "allowed_domain is required",
        ),
    ];
    for (name, hcl, want) in cases {
        let err = load_ui(hcl).err().unwrap_or_default();
        assert!(
            err.contains(want),
            "{name}: want error containing {want:?}, got {err:?}"
        );
    }
}

// Go: TestExampleUIConfigParses.
#[test]
fn example_ui_config_parses() {
    let p = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../examples/ui.tfstackplan.hcl");
    let u = load(&p)
        .expect("examples/ui.tfstackplan.hcl must parse")
        .expect("ui block");
    assert!(
        u.tiers.len() >= 2 && u.oauth.is_some(),
        "example should show two tiers and oauth: {u:?}"
    );
}

#[test]
fn loader_errors_read_like_go() {
    assert_eq!(
        load(Path::new("/nonexistent/.tfstackplan.hcl"))
            .err()
            .as_deref(),
        Some("open /nonexistent/.tfstackplan.hcl: no such file or directory")
    );
    let err = load_ui("bogus {}\n").err().unwrap_or_default();
    assert!(
        err.ends_with(": unknown top-level block \"bogus\""),
        "{err}"
    );
    assert_eq!(load_ui("server {}\n").expect("load"), None);
    let err = load_ui("ui {\n  nope = 1\n  tier \"a\" { url = \"x\" }\n}")
        .err()
        .unwrap_or_default();
    assert!(err.starts_with("ui block: Unsupported argument"), "{err}");
    let err = load_ui("ui {\n  tier { url = \"x\" }\n}")
        .err()
        .unwrap_or_default();
    assert!(err.contains("Missing name for tier"), "{err}");
}
