use super::*;
use crate::statemove::plan::Change;

/// A plan from (address, type, action) rows (Go's `planFrom`).
pub(crate) fn plan_from(rows: &[(&str, &str, &str)]) -> Plan {
    Plan {
        format_version: "1.2".into(),
        resource_changes: rows
            .iter()
            .map(|(a, t, act)| ResourceChange {
                address: a.to_string(),
                r#type: t.to_string(),
                change: Some(Change {
                    actions: vec![act.to_string()],
                    ..Default::default()
                }),
                ..Default::default()
            })
            .collect(),
        prior_state: None,
    }
}

// Go: TestValidateMoveResource.
#[test]
fn validate_move_resource() {
    let p = plan_from(&[
        ("aws_s3_bucket.old", "aws_s3_bucket", "delete"),
        ("aws_s3_bucket.new", "aws_s3_bucket", "create"),
    ]);
    validate_move(&p, "aws_s3_bucket.old", "aws_s3_bucket.new").unwrap();
}

// Go: TestValidateMoveModulePrefix.
#[test]
fn validate_move_module_prefix() {
    let p = plan_from(&[
        ("module.a.aws_s3_bucket.x", "aws_s3_bucket", "delete"),
        ("module.a.aws_iam_role.y", "aws_iam_role", "delete"),
        ("module.b.aws_s3_bucket.x", "aws_s3_bucket", "create"),
        ("module.b.aws_iam_role.y", "aws_iam_role", "create"),
    ]);
    validate_move(&p, "module.a", "module.b").unwrap();
}

// Go: TestValidateMoveForEachAndCount.
#[test]
fn validate_move_for_each_and_count() {
    let p = plan_from(&[
        ("aws_iam_member.m", "aws_iam_member", "delete"),
        (r#"aws_iam_member.m["admin"]"#, "aws_iam_member", "create"),
    ]);
    validate_move(&p, "aws_iam_member.m", r#"aws_iam_member.m["admin"]"#).unwrap();
}

// Go: TestValidateMoveFailsClosed.
#[test]
fn validate_move_fails_closed() {
    let p1 = plan_from(&[("aws_s3_bucket.old", "aws_s3_bucket", "delete")]);
    assert_eq!(
        validate_move(&p1, "aws_s3_bucket.old", "aws_s3_bucket.new").unwrap_err(),
        "move aws_s3_bucket.old → aws_s3_bucket.new: \"aws_s3_bucket.old\" is destroyed but \"aws_s3_bucket.new\" is not created (plan not in order)"
    );
    let p2 = plan_from(&[
        ("aws_s3_bucket.old", "aws_s3_bucket", "delete"),
        ("aws_iam_role.new", "aws_iam_role", "create"),
    ]);
    assert!(validate_move(&p2, "aws_s3_bucket.old", "aws_iam_role.new").is_err());
    let p3 = plan_from(&[
        ("module.a.aws_s3_bucket.x", "aws_s3_bucket", "delete"),
        ("module.b.aws_s3_bucket.x", "aws_s3_bucket", "create"),
        ("module.b.aws_s3_bucket.z", "aws_s3_bucket", "create"),
    ]);
    assert!(validate_move(&p3, "module.a", "module.b").is_err());
    let p4 = plan_from(&[("aws_s3_bucket.new", "aws_s3_bucket", "create")]);
    assert!(validate_move(&p4, "aws_s3_bucket.old", "aws_s3_bucket.new").is_err());
}

#[test]
fn validate_move_rejects_empty_and_identical() {
    let p = plan_from(&[]);
    assert_eq!(
        validate_move(&p, "", "x").unwrap_err(),
        "move: empty address (from=\"\" to=\"x\")"
    );
    assert_eq!(
        validate_move(&p, "x", "x").unwrap_err(),
        "move: from and to are identical (\"x\")"
    );
}

#[test]
fn matches_respects_boundaries() {
    assert!(matches("module.x", "module.x"));
    assert!(matches("module.x.r.y", "module.x"));
    assert!(matches("module.x[0]", "module.x"));
    assert!(!matches("module.x_other.r", "module.x"));
}
