use super::*;
use crate::statemove::StateResource;
use crate::statemove::plan::{Change, State, StateModule, StateValues};

fn del_with_id(addr: &str, typ: &str, id: &str) -> ResourceChange {
    ResourceChange {
        address: addr.into(),
        r#type: typ.into(),
        change: Some(Change {
            actions: vec!["delete".into()],
            before: serde_json::json!({ "id": id }),
            ..Default::default()
        }),
        ..Default::default()
    }
}

fn create(addr: &str, typ: &str) -> ResourceChange {
    ResourceChange {
        address: addr.into(),
        r#type: typ.into(),
        change: Some(Change {
            actions: vec!["create".into()],
            ..Default::default()
        }),
        ..Default::default()
    }
}

fn plan(rcs: Vec<ResourceChange>) -> Plan {
    Plan {
        format_version: "1.2".into(),
        resource_changes: rcs,
        prior_state: None,
    }
}

fn with_prior(addrs: &[&str]) -> Plan {
    Plan {
        format_version: "1.2".into(),
        resource_changes: vec![],
        prior_state: Some(State {
            format_version: "1.0".into(),
            values: Some(StateValues {
                root_module: Some(StateModule {
                    resources: addrs
                        .iter()
                        .map(|a| {
                            StateResource::managed(a, "registry.terraform.io/hashicorp/google")
                        })
                        .collect(),
                    child_modules: vec![],
                }),
            }),
        }),
    }
}

// Go: TestClassifyCrossStack.
#[test]
fn classify_cross_stack_ops() {
    let src = plan(vec![del_with_id(
        "aws_s3_bucket.old",
        "aws_s3_bucket",
        "my-bucket",
    )]);
    let dst = plan(vec![create("module.d.aws_s3_bucket.new", "aws_s3_bucket")]);
    let (s, d) = classify_cross_stack(
        &src,
        &dst,
        "aws_s3_bucket.old",
        "module.d.aws_s3_bucket.new",
    )
    .unwrap();
    assert_eq!(s, vec![Op::removed("aws_s3_bucket.old")]);
    assert_eq!(
        d,
        vec![Op::import("module.d.aws_s3_bucket.new", "my-bucket")]
    );
}

// Go: TestClassifyCrossStackFailsClosed.
#[test]
fn classify_cross_stack_fails_closed() {
    let src = plan(vec![del_with_id("a.x", "a", "i")]);
    let dst = plan(vec![create("b.y", "b")]);
    assert!(classify_cross_stack(&src, &dst, "a.x", "b.y").is_err());

    let mut no_id = del_with_id("a.x", "a", "");
    if let Some(c) = no_id.change.as_mut() {
        c.before = serde_json::json!({});
    }
    let err = classify_cross_stack(
        &plan(vec![no_id]),
        &plan(vec![create("a.y", "a")]),
        "a.x",
        "a.y",
    )
    .unwrap_err();
    assert_eq!(
        err,
        "cross-stack move a.x → a.y: \"a.x\" has no string before.id to import with"
    );

    let err = classify_cross_stack(
        &plan(vec![del_with_id("a.x", "a", "i")]),
        &plan(vec![]),
        "a.x",
        "a.y",
    )
    .unwrap_err();
    assert_eq!(
        err,
        "cross-stack move a.x → a.y: \"a.x\" is destroyed but \"a.y\" is not created at the destination"
    );
}

// Go: TestCheckXMoveSource_OkWhenFromPresentInPriorState.
#[test]
fn check_xmove_source_ok() {
    let p = with_prior(&["module.perms.google_project_iam_member.x"]);
    check_xmove_source(&p, "module.perms").unwrap();
}

// Go: TestCheckXMoveSource_ErrorWhenNoPriorState.
#[test]
fn check_xmove_source_no_prior_state() {
    let err = check_xmove_source(&plan(vec![]), "module.perms").unwrap_err();
    assert_eq!(err, "source stack has no prior state — nothing to move");
}

// Go: TestCheckXMoveSource_ErrorWhenFromNotInPriorState.
#[test]
fn check_xmove_source_from_missing() {
    let p = with_prior(&["module.other.google_project_iam_member.x"]);
    assert_eq!(
        check_xmove_source(&p, "module.perms").unwrap_err(),
        "nothing under \"module.perms\" in source prior state (wrong from address or stack?)"
    );
}

#[test]
fn cross_stack_pairs_fan_out_sorted() {
    let src = plan(vec![
        del_with_id("module.a.r.y", "r", "2"),
        del_with_id("module.a.r.x", "r", "1"),
    ]);
    let dst = plan(vec![
        create("module.b.r.x", "r"),
        create("module.b.r.y", "r"),
    ]);
    assert_eq!(
        cross_stack_pairs(&src, &dst, "module.a", "module.b").unwrap(),
        vec![
            Move::new("module.a.r.x", "module.b.r.x"),
            Move::new("module.a.r.y", "module.b.r.y")
        ]
    );
}
