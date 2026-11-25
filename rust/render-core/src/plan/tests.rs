use super::*;

fn load(name: &str) -> RawStack {
    let data: &[u8] = match name {
        "create.json" => include_bytes!("../../../../internal/plan/testdata/create.json"),
        "forget.json" => include_bytes!("../../../../internal/plan/testdata/forget.json"),
        "mixed.json" => include_bytes!("../../../../internal/plan/testdata/mixed.json"),
        "sensitive.json" => include_bytes!("../../../../internal/plan/testdata/sensitive.json"),
        "update.json" => include_bytes!("../../../../internal/plan/testdata/update.json"),
        other => panic!("no fixture {other}"),
    };
    parse("stack", data).unwrap()
}

fn s(v: &str) -> Value {
    Value::Str(v.to_string())
}

fn attr<'a>(c: &'a RawChange, name: &str) -> Option<&'a RawAttr> {
    c.attrs.iter().find(|a| a.name == name)
}

fn change(addr: &str, action: Action) -> RawChange {
    RawChange {
        address: addr.to_string(),
        type_: String::new(),
        actions: Vec::new(),
        action,
        attrs: Vec::new(),
        sensitivity_only: false,
        name: String::new(),
        module_address: String::new(),
        provider_name: String::new(),
        raw: BTreeMap::new(),
        moved: false,
        move_direction: String::new(),
        previous_address: String::new(),
        imported: false,
        import_id: String::new(),
    }
}

fn moved(addr: &str, action: Action, prev: &str) -> RawChange {
    RawChange {
        moved: true,
        previous_address: prev.to_string(),
        ..change(addr, action)
    }
}

#[test]
fn parse_counts_and_attrs() {
    let rs = load("update.json");
    assert_eq!(
        rs.counts,
        Counts {
            change: 1,
            ..Counts::default()
        }
    );
    assert_eq!(rs.changes.len(), 1);
    let c = &rs.changes[0];
    assert_eq!(c.action, Action::Change);
    assert_eq!(c.type_, "google_project_iam_member");
    assert_eq!(c.attrs.len(), 1);
    assert_eq!(c.attrs[0].name, "role");
}

#[test]
fn parse_mixed_actions() {
    let rs = load("mixed.json");
    assert_eq!(
        rs.counts,
        Counts {
            add: 1,
            destroy: 1,
            replace: 1,
            ..Counts::default()
        }
    );
    assert_eq!(rs.changes.len(), 3, "no-op should be excluded");
    for c in &rs.changes {
        match c.action {
            Action::Add => {
                assert!(!c.attrs.is_empty());
                assert_eq!(c.attrs[0].name, "name");
                assert_eq!(c.attrs[0].after, s("b"));
            }
            Action::Destroy => {
                assert!(!c.attrs.is_empty());
                assert_eq!(c.attrs[0].name, "name");
                assert_eq!(c.attrs[0].before, s("b"));
            }
            _ => {}
        }
    }
}

#[test]
fn forget_action_surfaced() {
    let rs = load("forget.json");
    assert_eq!(
        rs.counts,
        Counts {
            change: 1,
            forget: 1,
            ..Counts::default()
        }
    );
    assert_eq!(rs.changes.len(), 2);
    let fg = rs
        .changes
        .iter()
        .find(|c| c.address == "a.forget")
        .expect("forget change");
    assert_eq!(fg.action, Action::Forget);
    assert!(!fg.attrs.is_empty());
    assert_eq!(fg.attrs[0].before, s("b"));
}

#[test]
fn moved_and_imported() {
    let data = br#"{
      "format_version": "1.2",
      "resource_changes": [
        {"address":"a.new","previous_address":"a.old","type":"t","name":"new",
         "change":{"actions":["no-op"],"before":{"x":"1"},"after":{"x":"1"},
           "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}},
        {"address":"b.imported","type":"t","name":"imported",
         "change":{"actions":["no-op"],"importing":{"id":"i-0abc"},
           "before":{"x":"1"},"after":{"x":"1"},
           "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}},
        {"address":"c.skip","type":"t","name":"skip",
         "change":{"actions":["no-op"],"before":{"x":"1"},"after":{"x":"1"},
           "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}}
      ]
    }"#;
    let rs = parse("s", data).unwrap();
    assert_eq!((rs.counts.moved, rs.counts.import), (1, 1));
    assert_eq!(rs.changes.len(), 2, "plain no-op c.skip is dropped");
    let mv = rs.changes.iter().find(|c| c.address == "a.new").unwrap();
    assert!(mv.moved && mv.previous_address == "a.old" && mv.action == Action::Noop);
    let im = rs
        .changes
        .iter()
        .find(|c| c.address == "b.imported")
        .unwrap();
    assert!(im.imported && im.import_id == "i-0abc" && im.action == Action::Noop);
}

#[test]
fn sensitive_and_unknown_and_partial_change() {
    let rs = load("sensitive.json");
    assert_eq!(rs.changes.len(), 1);
    let c = &rs.changes[0];
    assert!(attr(c, "unchanged").is_none());
    assert!(attr(c, "secret_data").is_some_and(|a| a.sensitive));
    assert!(attr(c, "computed_id").is_some_and(|a| a.unknown));
}

#[test]
fn nested_sensitive_marker_does_not_flag_whole_attr() {
    let data = br#"{
      "format_version":"1.2",
      "resource_changes":[
        {"address":"k.app","type":"kubernetes_deployment_v1","name":"app",
         "change":{"actions":["update"],
           "before":{"spec":{"cpu":"334m","password":"x"}},
           "after": {"spec":{"cpu":"300m","password":"x"}},
           "after_unknown":{},
           "before_sensitive":{"spec":{"password":true}},
           "after_sensitive": {"spec":{"password":true}}}}
      ]}"#;
    let rs = parse("s", data).unwrap();
    let spec = attr(&rs.changes[0], "spec").expect("spec attr");
    assert!(
        !spec.sensitive,
        "nested sensitivity must not flag the whole attr"
    );
    let sub = spec.after_sensitive.as_map().expect("nested subtree");
    assert_eq!(sub.get("password"), Some(&Value::Bool(true)));
}

#[test]
fn parse_create_extracts_after_attrs() {
    let rs = load("create.json");
    assert_eq!(rs.counts.add, 1);
    let c = &rs.changes[0];
    assert_eq!(attr(c, "account_id").unwrap().after, s("app-api"));
    assert!(attr(c, "unique_id").unwrap().unknown);
}

#[test]
fn parse_carries_module_and_name() {
    let data = br#"{"format_version":"1.2","resource_changes":[
      {"address":"module.net.google_compute_firewall.web","module_address":"module.net",
       "type":"google_compute_firewall","name":"web",
       "change":{"actions":["update"],"before":{"a":"1"},"after":{"a":"2"},
         "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}}]}"#;
    let rs = parse("s", data).unwrap();
    let c = &rs.changes[0];
    assert_eq!(c.name, "web");
    assert_eq!(c.module_address, "module.net");
}

#[test]
fn parse_raw_retains_unchanged_scalars_skips_sensitive_and_nested() {
    let data = br#"{
      "format_version": "1.2",
      "resource_changes": [
        {"address":"google_project_iam_member.x","type":"google_project_iam_member","name":"x",
         "change":{"actions":["update"],
           "before":{"role":"roles/viewer","project":"p1","secret":"old","labels":{"a":"b"}},
           "after":{"role":"roles/editor","project":"p1","secret":"new","labels":{"a":"b"}},
           "after_unknown":{},
           "before_sensitive":{"secret":true},"after_sensitive":{"secret":true}}}
      ]
    }"#;
    let rs = parse("s", data).unwrap();
    assert_eq!(rs.changes.len(), 1);
    let raw = &rs.changes[0].raw;
    assert_eq!(raw.get("project"), Some(&s("p1")));
    assert_eq!(raw.get("role"), Some(&s("roles/editor")));
    assert!(!raw.contains_key("secret"));
    assert!(!raw.contains_key("labels"));
}

#[test]
fn parse_raw_prefers_after_falls_back_to_before() {
    let data = br#"{
      "format_version": "1.2",
      "resource_changes": [
        {"address":"google_project_iam_member.x","type":"google_project_iam_member","name":"x",
         "change":{"actions":["delete"],
           "before":{"project":"p9"},"after":null,
           "before_sensitive":{},"after_sensitive":{}}}
      ]
    }"#;
    let rs = parse("s", data).unwrap();
    assert_eq!(rs.changes[0].raw.get("project"), Some(&s("p9")));
}

#[test]
fn parse_raw_skips_sensitive_either_side_and_computed() {
    let data = br#"{
      "format_version": "1.2",
      "resource_changes": [
        {"address":"x.y","type":"t","name":"y",
         "change":{"actions":["update"],
           "before":{"role":"roles/a","mode":"old","id":"old-id"},
           "after":{"role":"roles/b","mode":null,"id":null},
           "after_unknown":{"id":true},
           "before_sensitive":{},"after_sensitive":{"mode":true}}}
      ]
    }"#;
    let rs = parse("s", data).unwrap();
    let raw = &rs.changes[0].raw;
    assert_eq!(raw.get("role"), Some(&s("roles/b")));
    assert!(!raw.contains_key("mode"));
    assert!(!raw.contains_key("id"));
}

#[test]
fn parse_raw_derives_project_from_gcp_parent_when_computed() {
    let data = br#"{
      "format_version": "1.2",
      "resource_changes": [
        {"address":"module.w.google_secret_manager_secret_iam_member.ci[\"x\"]",
         "type":"google_secret_manager_secret_iam_member","name":"ci",
         "change":{"actions":["create"],"before":null,
           "after":{"secret_id":"projects/example-svc/secrets/some_token","role":"roles/secretmanager.secretAccessor","member":"serviceAccount:ci@example-svc.iam.gserviceaccount.com"},
           "after_unknown":{"id":true,"project":true,"etag":true},
           "before_sensitive":false,"after_sensitive":{}}}
      ]
    }"#;
    let rs = parse("s", data).unwrap();
    assert_eq!(rs.changes[0].raw.get("project"), Some(&s("example-svc")));
}

#[test]
fn parse_raw_does_not_override_known_project_or_fabricate() {
    let data = br#"{
      "format_version": "1.2",
      "resource_changes": [
        {"address":"a","type":"google_project_iam_member","name":"a",
         "change":{"actions":["create"],"before":null,
           "after":{"project":"real-proj","secret_id":"projects/other-proj/secrets/x","role":"roles/viewer"},
           "after_unknown":{},"before_sensitive":false,"after_sensitive":{}}},
        {"address":"b","type":"google_storage_bucket_iam_member","name":"b",
         "change":{"actions":["create"],"before":null,
           "after":{"bucket":"my-bucket","role":"roles/viewer"},
           "after_unknown":{"project":true},"before_sensitive":false,"after_sensitive":{}}}
      ]
    }"#;
    let rs = parse("s", data).unwrap();
    assert_eq!(rs.changes[0].raw.get("project"), Some(&s("real-proj")));
    assert!(!rs.changes[1].raw.contains_key("project"));
}

#[test]
fn parse_provider_name() {
    let data = br#"{
      "format_version": "1.2",
      "resource_changes": [
        {"address": "a.b", "type": "t", "name": "b",
         "provider_name": "registry.terraform.io/hashicorp/google",
         "change": {"actions": ["create"], "before": null, "after": {"x": "1"},
           "after_unknown": {}, "before_sensitive": {}, "after_sensitive": {}}}
      ]
    }"#;
    let rs = parse("s", data).unwrap();
    assert_eq!(rs.changes.len(), 1);
    assert_eq!(
        rs.changes[0].provider_name,
        "registry.terraform.io/hashicorp/google"
    );
}

#[test]
fn parse_skips_data_sources() {
    let data = br#"{
      "format_version": "1.2",
      "resource_changes": [
        {"address": "data.aws_caller_identity.current", "mode": "data",
         "type": "aws_caller_identity", "name": "current",
         "provider_name": "registry.terraform.io/hashicorp/aws",
         "change": {"actions": ["noop"]}},
        {"address": "aws_s3_bucket.main", "mode": "managed",
         "type": "aws_s3_bucket", "name": "main",
         "provider_name": "registry.terraform.io/hashicorp/aws",
         "change": {"actions": ["create"]}}
      ]
    }"#;
    let rs = parse("stack", data).unwrap();
    assert_eq!(rs.changes.len(), 1, "data source skipped");
    assert_eq!(rs.changes[0].address, "aws_s3_bucket.main");
}

#[test]
fn parse_sensitivity_only_change() {
    let data = br#"{
      "format_version": "1.2",
      "resource_changes": [
        {"address": "aws_ssm_parameter.foo", "mode": "managed",
         "type": "aws_ssm_parameter", "name": "foo",
         "provider_name": "registry.terraform.io/hashicorp/aws",
         "change": {"actions": ["update"],
           "before": { "value": "my-secret-val" },
           "after": { "value": "my-secret-val" },
           "before_sensitive": { "value": false },
           "after_sensitive": { "value": true }}}
      ]
    }"#;
    let rs = parse("stack", data).unwrap();
    assert_eq!(rs.changes.len(), 1);
    let ch = &rs.changes[0];
    assert!(ch.sensitivity_only);
    assert_eq!(ch.attrs.len(), 1);
    assert_eq!(ch.attrs[0].name, "value");
    assert!(ch.attrs[0].sensitivity_only);
    assert_eq!((rs.counts.sensitivity_only, rs.counts.change), (1, 1));
}

#[test]
fn parse_errors_read_like_go() {
    assert_eq!(
        parse("s", b"{").unwrap_err(),
        r#"stack "s": parse plan json: unexpected end of JSON input"#
    );
    assert_eq!(
        parse("s", b"{}").unwrap_err(),
        r#"stack "s": parse plan json: unexpected plan input, format version is missing"#
    );
    assert_eq!(
        parse("s", br#"{"format_version":"2.0"}"#).unwrap_err(),
        r#"stack "s": parse plan json: unsupported plan format version: "2.0.0" does not satisfy ">= 0.1, < 2.0""#
    );
}

// moveset_apply_test.go

#[test]
fn apply_state_moves_reclassifies_module_children_as_moves() {
    let mut rs = RawStack {
        counts: Counts {
            add: 3,
            ..Counts::default()
        },
        changes: vec![
            change(
                "module.content_library.google_project_iam_member.x",
                Action::Add,
            ),
            change(
                "module.content_library.google_cloudbuild_trigger.t",
                Action::Add,
            ),
            change("module.other.google_project_iam_member.y", Action::Add),
        ],
        ..RawStack::default()
    };
    let n = rs.apply_state_moves(&Set::new(["module.content_library"]));
    assert_eq!(n, 2);
    assert_eq!((rs.counts.add, rs.counts.moved), (1, 2));
    for c in &rs.changes[..2] {
        assert!(c.action == Action::Noop && c.moved, "{}", c.address);
    }
    assert!(rs.changes[2].action == Action::Add && !rs.changes[2].moved);
}

#[test]
fn apply_state_moves_empty_targets_is_noop() {
    let mut rs = RawStack {
        counts: Counts {
            add: 1,
            ..Counts::default()
        },
        changes: vec![change("module.a.r", Action::Add)],
        ..RawStack::default()
    };
    assert_eq!(rs.apply_state_moves(&Set::default()), 0);
    assert_eq!((rs.counts.add, rs.counts.moved), (1, 0));
}

#[test]
fn apply_state_moves_leaves_in_stack_moves_alone() {
    let mut rs = RawStack {
        counts: Counts {
            moved: 1,
            ..Counts::default()
        },
        changes: vec![moved(
            "module.content_library.r",
            Action::Noop,
            "module.old.r",
        )],
        ..RawStack::default()
    };
    assert_eq!(
        rs.apply_state_moves(&Set::new(["module.content_library"])),
        0
    );
    assert_eq!(rs.counts.moved, 1);
}

#[test]
fn apply_state_moves_reclassifies_destroy_with_previous_address() {
    let mut rs = RawStack {
        counts: Counts {
            destroy: 2,
            moved: 1,
            ..Counts::default()
        },
        changes: vec![
            moved("module.agent[0].r", Action::Noop, "module.agent.r"),
            moved(
                "module.agent[0].google_project_iam_member.x",
                Action::Destroy,
                "module.agent.google_project_iam_member.x",
            ),
            moved(
                "module.agent[0].google_service_account.main",
                Action::Destroy,
                "module.agent.google_service_account.main",
            ),
        ],
        ..RawStack::default()
    };
    let targets = Set::new([
        "module.agent[0].google_project_iam_member.x",
        "module.agent[0].google_service_account.main",
    ]);
    assert_eq!(rs.apply_state_moves(&targets), 2);
    assert_eq!((rs.counts.destroy, rs.counts.moved), (0, 3));
    let c = &rs.changes[0];
    assert!(c.action == Action::Noop && c.previous_address == "module.agent.r");
    for c in &rs.changes[1..] {
        assert!(
            c.action == Action::Noop && c.moved && c.attrs.is_empty(),
            "{}",
            c.address
        );
    }
}

#[test]
fn apply_state_moves_reclassifies_by_previous_address() {
    let mut rs = RawStack {
        counts: Counts {
            destroy: 1,
            ..Counts::default()
        },
        changes: vec![moved(
            "module.agent[0].r",
            Action::Destroy,
            "module.agent.r",
        )],
        ..RawStack::default()
    };
    assert_eq!(rs.apply_state_moves(&Set::new(["module.agent.r"])), 1);
    assert_eq!((rs.counts.destroy, rs.counts.moved), (0, 1));
    assert!(rs.changes[0].action == Action::Noop && rs.changes[0].moved);
}
