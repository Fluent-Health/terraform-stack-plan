use super::*;
use crate::model::{Action, Counts};

fn re(p: &str) -> Option<GoRegex> {
    Some(GoRegex::new(p).unwrap())
}

fn stack(changes: Vec<RawChange>) -> RawStack {
    RawStack {
        name: "s".to_string(),
        counts: Counts::default(),
        changes,
    }
}

fn ch(typ: &str, action: Action, actions: &[&str], raw: &[(&str, &str)]) -> RawChange {
    RawChange {
        address: String::new(),
        type_: typ.to_string(),
        actions: actions.iter().map(|a| a.to_string()).collect(),
        action,
        attrs: Vec::new(),
        sensitivity_only: false,
        name: String::new(),
        module_address: String::new(),
        provider_name: String::new(),
        raw: raw
            .iter()
            .map(|(k, v)| (k.to_string(), Value::Str(v.to_string())))
            .collect(),
        moved: false,
        move_direction: String::new(),
        previous_address: String::new(),
        imported: false,
        import_id: String::new(),
    }
}

fn create(typ: &str, raw: &[(&str, &str)]) -> RawChange {
    ch(typ, Action::Add, &["create"], raw)
}

fn at(addr: &str, c: RawChange) -> RawChange {
    RawChange {
        address: addr.to_string(),
        ..c
    }
}

fn rule(name: &str, pattern: &str, min: i64, emit: &[&str]) -> Rule {
    Rule {
        name: name.to_string(),
        icon: "🔐".to_string(),
        type_pattern: re(pattern),
        min_count: min,
        emit_attributes: emit.iter().map(|s| s.to_string()).collect(),
        ..Rule::default()
    }
}

fn find<'a>(cats: &'a [Category], name: &str) -> Option<&'a Category> {
    cats.iter().find(|c| c.name == name)
}

fn values(c: Option<&Category>, attr: &str) -> Vec<String> {
    c.and_then(|c| c.attributes.as_ref())
        .and_then(|a| a.get(attr))
        .cloned()
        .unwrap_or_default()
}

fn none() -> Set {
    Set::default()
}

#[test]
fn all_matching_rules_fire() {
    let rules = vec![
        rule("iam", "_iam_", 1, &[]),
        Rule {
            name: "destructive".to_string(),
            icon: "💣".to_string(),
            actions: vec!["delete".to_string()],
            min_count: 1,
            ..Rule::default()
        },
    ];
    let s = stack(vec![ch(
        "google_project_iam_member",
        Action::Destroy,
        &["delete"],
        &[],
    )]);
    let got = classify(&s, &rules, &none());
    let names: Vec<&str> = got.iter().map(|c| c.name.as_str()).collect();
    assert_eq!(names, ["iam", "destructive"]);
}

#[test]
fn no_match_yields_empty() {
    let rules = vec![rule("iam", "_iam_", 1, &[])];
    assert!(
        classify(
            &stack(vec![create("google_storage_bucket", &[])]),
            &rules,
            &none()
        )
        .is_empty()
    );
}

#[test]
fn actions_and_min_count() {
    let rules = vec![Rule {
        name: "destructive".to_string(),
        actions: vec!["delete".to_string()],
        min_count: 2,
        ..Rule::default()
    }];
    let one = stack(vec![ch("x", Action::Destroy, &["delete"], &[])]);
    assert!(classify(&one, &rules, &none()).is_empty());
    let two = stack(vec![
        ch("x", Action::Destroy, &["delete"], &[]),
        ch("y", Action::Destroy, &["delete"], &[]),
    ]);
    assert!(find(&classify(&two, &rules, &none()), "destructive").is_some());
}

#[test]
fn emit_attributes_from_matched_changes_only() {
    let rules = vec![rule("iam", "_iam_", 1, &["project"])];
    let s = stack(vec![
        ch(
            "google_project_iam_member",
            Action::Change,
            &["update"],
            &[("project", "p1")],
        ),
        create("google_storage_bucket", &[("project", "p2")]),
    ]);
    let got = classify(&s, &rules, &none());
    assert_eq!(values(find(&got, "iam"), "project"), ["p1"]);
}

#[test]
fn emit_attributes_dedupe_and_sort() {
    let rules = vec![rule("iam", "_iam_", 1, &["project"])];
    let s = stack(vec![
        create("google_project_iam_member", &[("project", "p2")]),
        create("google_project_iam_member", &[("project", "p1")]),
        create("google_project_iam_member", &[("project", "p2")]),
    ]);
    assert_eq!(
        values(find(&classify(&s, &rules, &none()), "iam"), "project"),
        ["p1", "p2"]
    );
}

#[test]
fn emit_attributes_nil_when_none_configured() {
    let rules = vec![rule("iam", "_iam_", 1, &[])];
    let s = stack(vec![create(
        "google_project_iam_member",
        &[("project", "p1")],
    )]);
    let got = classify(&s, &rules, &none());
    assert!(find(&got, "iam").unwrap().attributes.is_none());
}

#[test]
fn emit_attributes_nil_when_no_values_found() {
    let rules = vec![rule("iam", "_iam_", 1, &["project"])];
    let s = stack(vec![create(
        "google_organization_iam_binding",
        &[("role", "roles/x")],
    )]);
    let got = classify(&s, &rules, &none());
    assert!(
        find(&got, "iam")
            .expect("iam category")
            .attributes
            .is_none()
    );
}

#[test]
fn emit_multiple_attributes() {
    let rules = vec![rule("iam", "_iam_", 1, &["project", "role"])];
    let s = stack(vec![
        create(
            "google_project_iam_member",
            &[("project", "p1"), ("role", "roles/viewer")],
        ),
        create(
            "google_project_iam_member",
            &[("project", "p2"), ("role", "roles/viewer")],
        ),
    ]);
    let got = classify(&s, &rules, &none());
    assert_eq!(values(find(&got, "iam"), "project"), ["p1", "p2"]);
    assert_eq!(values(find(&got, "iam"), "role"), ["roles/viewer"]);
}

#[test]
fn below_min_count_does_not_fire() {
    let rules = vec![rule("iam", "_iam_", 2, &["project"])];
    let s = stack(vec![create(
        "google_project_iam_member",
        &[("project", "p1")],
    )]);
    assert!(classify(&s, &rules, &none()).is_empty());
}

#[test]
fn state_ops_do_not_classify() {
    let rules = vec![rule("iam", "_iam_", 1, &[])];
    let mut mv = ch("google_project_iam_member", Action::Noop, &["no-op"], &[]);
    mv.moved = true;
    mv.previous_address = "google_project_iam_member.old".to_string();
    let mut im = ch("google_project_iam_member", Action::Noop, &["no-op"], &[]);
    im.imported = true;
    im.import_id = "x".to_string();
    let fg = ch(
        "google_project_iam_member",
        Action::Forget,
        &["forget"],
        &[],
    );
    for (name, c) in [("move-only", mv), ("import-only", im), ("forget", fg)] {
        assert!(
            classify(&stack(vec![c]), &rules, &none()).is_empty(),
            "{name}"
        );
    }
}

#[test]
fn classify_skips_move_targets() {
    let rules = vec![rule("iam", "^google_project_iam_member$", 1, &["project"])];
    let s = stack(vec![
        at(
            "module.cl.google_project_iam_member.x",
            create("google_project_iam_member", &[("project", "p-move")]),
        ),
        at(
            "module.other.google_project_iam_member.y",
            create("google_project_iam_member", &[("project", "p-real")]),
        ),
    ]);
    let targets = Set::new(["module.cl.google_project_iam_member.x"]);
    let got = classify(&s, &rules, &targets);
    assert_eq!(values(find(&got, "iam"), "project"), ["p-real"]);
}

#[test]
fn classify_all_move_targets_no_category() {
    let rules = vec![rule("iam", "^google_project_iam_member$", 1, &[])];
    let s = stack(vec![at(
        "module.cl.google_project_iam_member.x",
        create("google_project_iam_member", &[("project", "p-move")]),
    )]);
    let targets = Set::new(["module.cl.google_project_iam_member.x"]);
    assert!(classify(&s, &rules, &targets).is_empty());
}

#[test]
fn classify_skips_module_level_move_target() {
    let rules = vec![rule("iam", "^google_project_iam_member$", 1, &[])];
    let s = stack(vec![
        at(
            "module.content_library.google_project_iam_member.editor",
            create("google_project_iam_member", &[("project", "p-move")]),
        ),
        at(
            "module.content_library.google_project_iam_member.viewer",
            create("google_project_iam_member", &[("project", "p-move")]),
        ),
    ]);
    assert!(classify(&s, &rules, &Set::new(["module.content_library"])).is_empty());
}

#[test]
fn classify_skips_previous_address_matches() {
    let rules = vec![rule("iam", "^google_project_iam_member$", 1, &[])];
    let mut c = at(
        "module.agent[0].google_project_iam_member.x",
        create("google_project_iam_member", &[]),
    );
    c.previous_address = "module.agent.google_project_iam_member.x".to_string();
    let targets = Set::new(["module.agent.google_project_iam_member.x"]);
    assert!(classify(&stack(vec![c]), &rules, &targets).is_empty());
}

#[test]
fn mutating_change_still_classifies_when_also_moved() {
    let rules = vec![rule("iam", "_iam_", 1, &[])];
    let mut c = ch(
        "google_project_iam_member",
        Action::Change,
        &["update"],
        &[],
    );
    c.moved = true;
    c.previous_address = "google_project_iam_member.old".to_string();
    assert!(find(&classify(&stack(vec![c]), &rules, &none()), "iam").is_some());
}

fn cat(name: &str, icon: &str, attrs: &[(&str, &[&str])]) -> Category {
    Category {
        name: name.to_string(),
        icon: icon.to_string(),
        attributes: Some(
            attrs
                .iter()
                .map(|(k, vs)| (k.to_string(), vs.iter().map(|v| v.to_string()).collect()))
                .collect(),
        ),
    }
}

#[test]
fn summarize_unions_across_stacks() {
    let rules = vec![
        Rule {
            name: "iam".to_string(),
            ..Rule::default()
        },
        Rule {
            name: "sql-server".to_string(),
            ..Rule::default()
        },
    ];
    let per_stack = vec![
        vec![cat("iam", "🔐", &[("project", &["p1", "p2"])])],
        vec![
            cat("iam", "🔐", &[("project", &["p2", "p3"])]),
            cat("sql-server", "🗄", &[("instance", &["db1"])]),
        ],
        vec![],
    ];
    let got = summarize(&per_stack, &rules);
    let names: Vec<&str> = got.iter().map(|c| c.name.as_str()).collect();
    assert_eq!(names, ["iam", "sql-server"]);
    assert_eq!(values(got.first(), "project"), ["p1", "p2", "p3"]);
    assert_eq!(values(got.get(1), "instance"), ["db1"]);
}

#[test]
fn summarize_empty() {
    let rules = vec![Rule {
        name: "iam".to_string(),
        ..Rule::default()
    }];
    assert!(summarize(&[vec![], vec![]], &rules).is_empty());
}

#[test]
fn project_fallback_to_stack_project_for_projectless_iam() {
    let rules = vec![rule("iam", "_iam_", 1, &["project"])];
    let s = stack(vec![
        create(
            "google_storage_bucket_iam_member",
            &[
                ("bucket", "dev-svc-cms"),
                ("role", "roles/storage.objectViewer"),
            ],
        ),
        create("google_storage_transfer_job", &[("project", "dev-svc")]),
    ]);
    assert_eq!(
        values(find(&classify(&s, &rules, &none()), "iam"), "project"),
        ["dev-svc"]
    );
}

#[test]
fn project_fallback_skipped_when_stack_project_ambiguous() {
    let rules = vec![rule("iam", "_iam_", 1, &["project"])];
    let s = stack(vec![
        create("google_storage_bucket_iam_member", &[("bucket", "b")]),
        create("google_storage_transfer_job", &[("project", "p1")]),
        create("google_pubsub_topic", &[("project", "p2")]),
    ]);
    assert!(values(find(&classify(&s, &rules, &none()), "iam"), "project").is_empty());
}

#[test]
fn project_fallback_does_not_override_explicit_iam_project() {
    let rules = vec![rule("iam", "_iam_", 1, &["project"])];
    let s = stack(vec![
        create("google_project_iam_member", &[("project", "p1")]),
        create("google_storage_transfer_job", &[("project", "p1")]),
    ]);
    assert_eq!(
        values(find(&classify(&s, &rules, &none()), "iam"), "project"),
        ["p1"]
    );
}

fn build_cache_derivation() -> Derivation {
    Derivation {
        attribute: "project".to_string(),
        type_pattern: re("^google_storage_(bucket|managed_folder)_iam_"),
        from_attribute: "bucket".to_string(),
        pattern: GoRegex::new("^(?P<value>.+)-build-cache$").unwrap(),
    }
}

fn derive_rule(d: Derivation) -> Vec<Rule> {
    vec![Rule {
        derivations: vec![d],
        ..rule("iam", "_iam_", 1, &["project"])
    }]
}

#[test]
fn project_derivation_from_attribute_per_resource() {
    let rules = derive_rule(build_cache_derivation());
    let t = "google_storage_managed_folder_iam_member";
    let s = stack(vec![
        create(t, &[("bucket", "dev-svc-build-cache")]),
        create(t, &[("bucket", "test-svc-build-cache")]),
        create(t, &[("bucket", "stage-svc-build-cache")]),
    ]);
    assert_eq!(
        values(find(&classify(&s, &rules, &none()), "iam"), "project"),
        ["dev-svc", "stage-svc", "test-svc"]
    );
}

#[test]
fn project_derivation_does_not_override_explicit() {
    let rules = derive_rule(build_cache_derivation());
    let s = stack(vec![create(
        "google_storage_bucket_iam_member",
        &[("project", "p1"), ("bucket", "p2-build-cache")],
    )]);
    assert_eq!(
        values(find(&classify(&s, &rules, &none()), "iam"), "project"),
        ["p1"]
    );
}

#[test]
fn project_derivation_respects_type_pattern() {
    let rules = derive_rule(Derivation {
        type_pattern: re("^google_storage_managed_folder_iam_"),
        ..build_cache_derivation()
    });
    let s = stack(vec![create(
        "google_storage_bucket_iam_member",
        &[("bucket", "dev-svc-build-cache")],
    )]);
    assert!(values(find(&classify(&s, &rules, &none()), "iam"), "project").is_empty());
}

#[test]
fn project_derivation_falls_back_to_stack_project_when_unmatched() {
    let rules = derive_rule(build_cache_derivation());
    let s = stack(vec![
        create(
            "google_storage_bucket_iam_member",
            &[("bucket", "dev-svc-cms")],
        ),
        create("google_storage_transfer_job", &[("project", "dev-svc")]),
    ]);
    assert_eq!(
        values(find(&classify(&s, &rules, &none()), "iam"), "project"),
        ["dev-svc"]
    );
}

// presets_test.go

#[test]
fn iam_matches_across_providers() {
    let r = preset_rule("iam", "", Vec::new()).expect("iam preset");
    assert_eq!(r.icon, "🔐");
    let tp = r.type_pattern.unwrap();
    for ty in [
        "google_project_iam_member",
        "google-beta_storage_bucket_iam_binding",
        "aws_iam_role",
        "azurerm_role_assignment",
    ] {
        assert!(tp.is_match(ty), "should match {ty}");
    }
    for ty in ["google_storage_bucket", "aws_s3_bucket"] {
        assert!(!tp.is_match(ty), "should not match {ty}");
    }
}

#[test]
fn icon_override() {
    assert_eq!(preset_rule("iam", "⚠️", Vec::new()).unwrap().icon, "⚠️");
}

#[test]
fn unknown_preset() {
    assert!(preset_rule("nope", "", Vec::new()).is_none());
}

#[test]
fn emit_attributes_propagated() {
    let r = preset_rule("iam", "", vec!["project".to_string()]).unwrap();
    assert_eq!(r.emit_attributes, ["project"]);
}

// alias_test.go: Go checks that classify.Category is the shared domain type. Here there is one
// Category type, in model; the test keeps the attribute round trip.
#[test]
fn classify_category_is_domain_alias() {
    let c = cat("iam", "", &[("project", &["p1"])]);
    assert_eq!(values(Some(&c), "project"), ["p1"]);
}
