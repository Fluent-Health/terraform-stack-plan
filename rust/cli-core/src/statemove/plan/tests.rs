use super::*;

#[test]
fn plan_parsing_checks_format_versions_like_tfjson() {
    assert_eq!(
        parse_plan(br#"{"resource_changes":[]}"#).unwrap_err(),
        "unexpected plan input, format version is missing"
    );
    assert_eq!(
        parse_plan(br#"{"format_version":"2.0"}"#).unwrap_err(),
        "unsupported plan format version: \"2.0.0\" does not satisfy \">= 0.1, < 2.0\""
    );
    assert_eq!(
        parse_plan(br#"{"format_version":"1.2","prior_state":{"values":{}}}"#).unwrap_err(),
        "unexpected state input, format version is missing"
    );
    let p = parse_plan(
        br#"{"format_version":"1.2","resource_changes":[{"address":"a.b","type":"a","change":{"actions":["delete"],"before":{"id":"x"}}}],"prior_state":null}"#,
    )
    .unwrap();
    assert_eq!(p.resource_changes.len(), 1);
    assert!(p.resource_changes[0].change.as_ref().unwrap().is_delete());
    assert!(p.prior_state.is_none());
}

#[test]
fn nulls_read_as_empty() {
    let p = parse_plan(br#"{"format_version":"1.0","resource_changes":null}"#).unwrap();
    assert!(p.resource_changes.is_empty());
}

#[test]
fn change_addresses_skip_data_reads_and_pure_noops() {
    let p = parse_plan(
        br#"{"format_version":"1.2","resource_changes":[
          {"address":"a.created","provider_name":"p/a","change":{"actions":["create"]}},
          {"address":"data.a.d","mode":"data","provider_name":"p/a","change":{"actions":["read"]}},
          {"address":"a.read","provider_name":"p/a","change":{"actions":["read"]}},
          {"address":"a.noop","provider_name":"p/a","change":{"actions":["no-op"]}},
          {"address":"a.moved","previous_address":"a.old","provider_name":"p/a","change":{"actions":["no-op"]}},
          {"address":"a.imported","provider_name":"p/a","change":{"actions":["no-op"],"importing":{"id":"i"}}}
        ]}"#,
    )
    .unwrap();
    let addrs = change_addresses(&p);
    let got: Vec<&str> = addrs.keys().map(String::as_str).collect();
    assert_eq!(got, vec!["a.created", "a.imported", "a.moved", "a.old"]);
}
