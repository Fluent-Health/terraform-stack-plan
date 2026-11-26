use std::path::Path;

use super::*;

fn write(dir: &Path, rel: &str, body: &str) {
    let p = dir.join(rel);
    std::fs::create_dir_all(p.parent().unwrap()).unwrap();
    std::fs::write(p, body).unwrap();
}

fn s(p: &Path) -> String {
    p.to_string_lossy().into_owned()
}

// Go: TestRenderClassificationReconcilesXMove.
#[test]
fn reconciles_a_pending_cross_state_move() {
    let tmp = tempfile::tempdir().unwrap();
    let dir = tmp.path();
    let src = "stacks/nonprod/service-projects/dev-svc";
    let dst = "stacks/nonprod/workloads/cms/dev-svc";
    write(
        dir,
        &format!("{src}/tfplan.json"),
        r#"{"format_version":"1.2","prior_state":{"format_version":"0.1","values":{"root_module":{"child_modules":[{"address":"module.main","child_modules":[{"address":"module.main.module.cms[0]","resources":[{"address":"module.main.module.cms[0].google_project_iam_member.a","type":"google_project_iam_member","provider_name":"registry.terraform.io/hashicorp/google"}]}]}]}}},"resource_changes":[{"address":"module.main.module.cms[0].google_project_iam_member.a","type":"google_project_iam_member","name":"a","change":{"actions":["delete"],"before":{"project":"p1","role":"roles/viewer"},"after":null}}]}"#,
    );
    write(
        dir,
        &format!("{dst}/tfplan.json"),
        r#"{"format_version":"1.2","resource_changes":[
      {"address":"module.cms.google_project_iam_member.a","type":"google_project_iam_member","name":"a",
       "change":{"actions":["create"],"before":null,"after":{"project":"p1","role":"roles/viewer"}}}]}"#,
    );
    write(
        dir,
        &format!("{dst}/_tfsp_xmove.PR-1.hcl"),
        &format!(
            "# tfstackplan:key=PR-1\nxmove {{\n  source_stack = \"{src}\"\n  moves = {{\n    \"module.main.module.cms[0]\" = \"module.cms\"\n  }}\n}}\n"
        ),
    );
    write(dir, &format!("{dst}/providers.tf"), r#"provider "google" {}"#);
    write(
        dir,
        ".tfstackplan.hcl",
        "classification {\n  default {\n    name = \"safe\"\n    icon = \"✅\"\n  }\n  preset \"iam\" {\n    icon            = \"🔐\"\n    emit_attributes = [\"project\"]\n  }\n}\n",
    );
    let res = render_classification(
        &s(dir),
        &[src.to_string(), dst.to_string()],
        &s(&dir.join(".tfstackplan.hcl")),
    )
    .unwrap();
    for (stack, cats) in &res.categories {
        assert!(
            cats.iter().all(|c| c.name != "iam"),
            "{stack} classified iam: {cats:?}"
        );
    }
    assert!(res.moving.contains(&dst.to_string()), "{:?}", res.moving);
}

// Go: TestRenderClassificationFailsClosedOnCorruptXMove.
#[test]
fn fails_closed_on_a_corrupt_xmove() {
    let tmp = tempfile::tempdir().unwrap();
    let dir = tmp.path();
    write(dir, "stacks/a/tfplan.json", r#"{"format_version":"1.2","resource_changes":[]}"#);
    write(
        dir,
        "stacks/a/_tfsp_xmove.PR-1.hcl",
        "# tfstackplan:key=PR-1\nxmove { not valid hcl\n",
    );
    write(
        dir,
        ".tfstackplan.hcl",
        "classification {\n  default {\n    name = \"safe\"\n    icon = \"✅\"\n  }\n}\n",
    );
    assert!(
        render_classification(
            &s(dir),
            &["stacks/a".to_string()],
            &s(&dir.join(".tfstackplan.hcl"))
        )
        .is_err()
    );
}

// Go: TestRenderClassificationCarriesCounts.
#[test]
fn carries_counts() {
    let tmp = tempfile::tempdir().unwrap();
    let dir = tmp.path();
    write(
        dir,
        "stacks/a/tfplan.json",
        r#"{"format_version":"1.2","resource_changes":[
      {"address":"null_resource.x","type":"null_resource","name":"x",
       "change":{"actions":["create"],"before":null,"after":{}}}]}"#,
    );
    write(
        dir,
        ".tfstackplan.hcl",
        "classification {\n  default {\n    name = \"safe\"\n    icon = \"✅\"\n  }\n}\n",
    );
    let res = render_classification(
        &s(dir),
        &["stacks/a".to_string()],
        &s(&dir.join(".tfstackplan.hcl")),
    )
    .unwrap();
    assert_eq!(
        res.counts["stacks/a"],
        Counts {
            add: 1,
            ..Counts::default()
        }
    );
}

// Go: TestGatherPlans (cmd/tfstackplan/plan_test.go).
#[test]
fn gather_plans_copies_present_plans_only() {
    let tmp = tempfile::tempdir().unwrap();
    let dir = tmp.path();
    write(dir, "stacks/a/tfplan.json", "{\"a\":1}");
    std::fs::create_dir_all(dir.join("stacks/b")).unwrap();
    let plans = gather_plans(&s(dir), &["stacks/a".to_string(), "stacks/b".to_string()]).unwrap();
    assert_eq!(
        std::fs::read_to_string(plans.path().join("stacks/a/tfplan.json")).unwrap(),
        "{\"a\":1}"
    );
    assert!(!plans.path().join("stacks/b").exists());
}

#[test]
fn gating_classes_from_class_bindings() {
    let tmp = tempfile::tempdir().unwrap();
    let dir = tmp.path();
    write(
        dir,
        ".tfstackplan.hcl",
        "class \"iam\" {\n  required = true\n}\nclass \"network\" {}\n",
    );
    assert_eq!(gating_classes("", &s(dir)), vec!["iam", "network"]);
    assert!(gating_classes(&s(&dir.join("missing.hcl")), &s(dir)).is_empty());
}
