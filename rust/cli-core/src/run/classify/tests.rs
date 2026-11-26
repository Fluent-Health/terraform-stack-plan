use std::collections::BTreeMap;

use super::*;
use crate::statemove::{Move, XMove, parse_xmove, render_xmove};

const SRC: &str = "stacks/nonprod/service-projects/dev-svc";
const DST: &str = "stacks/nonprod/workloads/cms/dev-svc";

fn found(dest: &str, key: &str, text: &str) -> FoundXMove {
    let (_, xmove) = parse_xmove(text).unwrap();
    FoundXMove {
        path: format!("{dest}/_tfsp_xmove.{key}.hcl"),
        dest_stack: dest.to_string(),
        key: key.to_string(),
        xmove,
    }
}

fn manifest(src: &str, from: &str, to: &str) -> String {
    format!(
        "# tfstackplan:key=PR-1\nxmove {{\n  source_stack = \"{src}\"\n  moves = {{\n    \"{from}\" = \"{to}\"\n  }}\n}}\n"
    )
}

/// Runs the check with plans and `.tf` files by stack; every stack with a plan or
/// files exists.
fn check(xmoves: &[FoundXMove], plans: &[(&str, &str)], tf: &[(&str, &str)]) -> Checked {
    let plans: BTreeMap<String, Vec<u8>> = plans
        .iter()
        .map(|(k, v)| (k.to_string(), v.as_bytes().to_vec()))
        .collect();
    let tf: BTreeMap<String, Vec<String>> = tf.iter().fold(BTreeMap::new(), |mut m, (k, v)| {
        m.entry(k.to_string()).or_default().push(v.to_string());
        m
    });
    let plan = |s: &str| plans.get(s).cloned();
    let exists = |s: &str| plans.contains_key(s) || tf.contains_key(s);
    let tf_files = |s: &str| tf.get(s).cloned().unwrap_or_default();
    validate_xmoves(
        xmoves,
        &Inputs {
            plan: &plan,
            exists: &exists,
            tf_files: &tf_files,
        },
    )
}

const SRC_OTHER_MODULE: &str = r#"{"format_version":"1.2","prior_state":{"values":{"root_module":{"child_modules":[{"address":"module.other","resources":[{"address":"module.other.r","type":"google_project_iam_member","provider_name":"registry.terraform.io/hashicorp/google"}]}]}}},"resource_changes":[]}"#;

// Go: TestValidateXMoveManifest_warnsForMissingAddresses.
#[test]
fn missing_source_address_is_an_error() {
    let x = found(
        DST,
        "PR-1",
        &manifest(SRC, "module.main.module.cms[0]", "module.cms"),
    );
    let c = check(&[x], &[(SRC, SRC_OTHER_MODULE)], &[]);
    assert!(c.failed);
    assert!(
        c.stderr.contains(
            "\u{274c} xmove PR-1: address \"module.main.module.cms[0]\" not found in source plan (manifest stale or address renamed)"
        ),
        "{}",
        c.stderr
    );
}

// Go: TestValidateXMoveManifest_spentManifestIsNoOp.
#[test]
fn spent_entry_is_info() {
    let dst = r#"{"format_version":"1.2","prior_state":{"values":{"root_module":{"child_modules":[{"address":"module.cms","resources":[{"address":"module.cms.google_sql_database.a","type":"google_sql_database","provider_name":"registry.terraform.io/hashicorp/google"}]}]}}},"resource_changes":[]}"#;
    let x = found(
        DST,
        "PR-1",
        &manifest(SRC, "module.main.module.cms[0]", "module.cms"),
    );
    let c = check(&[x], &[(SRC, SRC_OTHER_MODULE), (DST, dst)], &[]);
    assert!(!c.failed, "{}", c.stderr);
    assert!(c.stderr.contains("xmove/spent") && c.stderr.contains("already applied"));
    assert!(!c.stderr.contains('\u{274c}'));
}

// Go: TestValidateXMoveManifest_warnsForMissingProviders.
#[test]
fn missing_destination_provider_then_configured() {
    let src = r#"{"format_version":"1.2","prior_state":{"values":{"root_module":{"child_modules":[{"address":"module.main","child_modules":[{"address":"module.main.module.cms[0]","resources":[{"address":"module.main.module.cms[0].postgresql_grant.a","type":"postgresql_grant","provider_name":"registry.terraform.io/cyrilgdn/postgresql"}]}]}]}}},"resource_changes":[{"address":"module.main.module.cms[0].postgresql_grant.a","type":"postgresql_grant","name":"a","provider_name":"registry.terraform.io/cyrilgdn/postgresql","change":{"actions":["delete"],"before":{"id":"abc"},"after":null}}]}"#;
    let x = found(
        DST,
        "PR-1",
        &manifest(
            SRC,
            "module.main.module.cms[0].postgresql_grant.a",
            "postgresql_grant.a",
        ),
    );
    let main = r#"resource "postgresql_grant" "a" {}"#;
    let c = check(std::slice::from_ref(&x), &[(SRC, src)], &[(DST, main)]);
    assert!(c.failed && c.stderr.contains("provider"), "{}", c.stderr);
    let providers = "\n\tprovider \"postgresql\" {\n\thost = \"localhost\"\n\t}\n\t";
    let c = check(&[x], &[(SRC, src)], &[(DST, main), (DST, providers)]);
    assert!(!c.failed, "{}", c.stderr);
    assert!(!c.stderr.contains("provider"), "{}", c.stderr);
}

fn rendered(src: &str, key: &str, from: &str, to: &str) -> String {
    render_xmove(
        key,
        &XMove {
            source_stack: src.to_string(),
            pairs: vec![Move::new(from, to)],
        },
    )
}

// Go: TestValidateXMoveManifest_SpentIsGreen.
#[test]
fn whole_manifest_spent_is_green() {
    let x = found(
        "stacks/dst",
        "PR-20",
        &rendered("stacks/src", "PR-20", "module.x", "module.y"),
    );
    let dst = r#"{"format_version":"1.2","prior_state":{"values":{"root_module":{"child_modules":[{"address":"module.y","resources":[{"address":"module.y.google_project.p","type":"google_project","mode":"managed","provider_name":"registry.terraform.io/hashicorp/google"}]}]}}}}"#;
    let c = check(&[x], &[("stacks/dst", dst)], &[]);
    assert!(!c.failed, "{}", c.stderr);
    assert!(c.stderr.contains("spent"));
}

// Go: TestValidateXMoveManifest_SourceNotPlanned.
#[test]
fn source_not_planned_is_an_error() {
    let x = found(
        "stacks/dst",
        "PR-42",
        &rendered("stacks/src", "PR-42", "module.x", "module.x"),
    );
    let c = check(&[x], &[], &[("stacks/dst", "")]);
    assert!(c.failed);
    assert!(c.stderr.contains("source-not-planned"));
    assert!(!c.stderr.contains("no such file or directory"));
    assert_eq!(
        c.stderr,
        "\u{274c} xmove PR-42: xmove/source-not-planned — source stack \"stacks/src\" is not in the changed set; touch it so Terramate plans it, or remove this manifest if the move is already complete\n"
    );
}

// Go: TestValidateXMoveManifest_DataSourceOrphan.
#[test]
fn data_source_orphan_is_a_warning() {
    let x = found(
        "stacks/dst",
        "PR-30",
        &rendered("stacks/src", "PR-30", "module.x", "module.y"),
    );
    let src = r#"{"format_version":"1.2","prior_state":{"values":{"root_module":{"child_modules":[{"address":"module.x","resources":[{"address":"module.x.google_project.p","mode":"managed","type":"google_project","provider_name":"registry.terraform.io/hashicorp/google","values":{}},{"address":"module.x.data.google_secret_manager_secret_version.v","mode":"data","type":"google_secret_manager_secret_version","provider_name":"registry.terraform.io/hashicorp/google","values":{}}]}]}}}}"#;
    let c = check(
        &[x],
        &[("stacks/src", src)],
        &[("stacks/dst", r#"provider "google" {}"#)],
    );
    assert!(!c.failed, "{}", c.stderr);
    assert!(c.stderr.contains("data-source-orphan"));
    assert!(
        c.stderr
            .contains("module.x.data.google_secret_manager_secret_version.v")
    );
}

// Go: TestValidateXMoveManifest_HardErrorWhenNoPriorState.
#[test]
fn no_prior_state_is_an_error() {
    let x = found(
        "dst",
        "PR-99",
        &rendered("src", "PR-99", "module.perms", "module.dest"),
    );
    let src = r#"{"format_version":"1.2","resource_changes":[{"address":"module.perms.google_project_iam_member.x","type":"google_project_iam_member","change":{"actions":["delete"]}}]}"#;
    let c = check(&[x], &[("src", src)], &[("dst", "")]);
    assert!(c.failed);
    assert!(c.stderr.contains("has no prior_state"), "{}", c.stderr);
}
