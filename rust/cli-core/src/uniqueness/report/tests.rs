use super::*;
use crate::uniqueness::{Kind, Severity, Value};

fn sample() -> Report {
    Report {
        unjustified: vec![Violation {
            unit: "svc/api".into(),
            key: "account_id".into(),
            value: Value::str("999<9>"),
            envs: vec!["dev".into(), "prod".into()],
            kind: Kind::Duplicate,
            severity: Severity::Violation,
        }],
        stale: vec![AllowRule {
            unit: "svc/api".into(),
            key: "old_id".into(),
            envs: vec![],
            reason: "gone".into(),
            expires: String::new(),
        }],
        report_only: vec![Violation {
            unit: "svc/api".into(),
            key: "hosts".into(),
            value: Value::list(&["a.example.com"]),
            envs: vec!["dev".into(), "test".into()],
            kind: Kind::Duplicate,
            severity: Severity::ReportOnly,
        }],
    }
}

#[test]
fn text_matches_go_layout() {
    assert_eq!(
        render_text(&sample()),
        "Report-only findings (not blocking):\n  svc/api: hosts duplicated across [dev test] (duplicate)\nUnjustified violations:\n  svc/api: account_id across [dev prod] (duplicate)\nStale allow rules (no longer match anything):\n  svc/api: old_id\n"
    );
    assert_eq!(
        render_text(&Report::default()),
        "tfstackplan uniqueness: ok — no unjustified violations or stale allows\n"
    );
}

#[test]
fn json_matches_go_encoder() {
    let want = r#"{
  "Unjustified": [
    {
      "Unit": "svc/api",
      "Key": "account_id",
      "Value": "999\u003c9\u003e",
      "Envs": [
        "dev",
        "prod"
      ],
      "Kind": "duplicate",
      "Severity": "violation"
    }
  ],
  "Stale": [
    {
      "Unit": "svc/api",
      "Key": "old_id",
      "Envs": [],
      "Reason": "gone",
      "Expires": ""
    }
  ],
  "ReportOnly": [
    {
      "Unit": "svc/api",
      "Key": "hosts",
      "Value": {
        "Elems": [
          "a.example.com"
        ]
      },
      "Envs": [
        "dev",
        "test"
      ],
      "Kind": "duplicate",
      "Severity": "report-only"
    }
  ]
}
"#;
    assert_eq!(render_json(&sample()).unwrap(), want);
    assert_eq!(
        render_json(&Report::default()).unwrap(),
        "{\n  \"Unjustified\": null,\n  \"Stale\": null,\n  \"ReportOnly\": null\n}\n"
    );
}
