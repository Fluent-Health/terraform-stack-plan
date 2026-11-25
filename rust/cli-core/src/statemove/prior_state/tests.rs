use super::*;
use crate::statemove::{StateResource, plan::StateModule};

// Go: TestPriorStateDataSources.
#[test]
fn prior_state_data_sources_lists_data_only() {
    let plan_json = br#"{"prior_state":{"values":{"root_module":{"resources":[
		{"address":"google_project.p","mode":"managed","provider_name":"registry.terraform.io/hashicorp/google","values":{}},
		{"address":"data.google_secret_manager_secret_version.x","mode":"data","provider_name":"registry.terraform.io/hashicorp/google","values":{}}
	],"child_modules":[{"resources":[
		{"address":"module.m.aws_s3_bucket.b","mode":"managed","provider_name":"registry.terraform.io/hashicorp/aws","values":{}},
		{"address":"module.m.data.aws_caller_identity.current","mode":"data","provider_name":"registry.terraform.io/hashicorp/aws","values":{}}
	]}]}}}}"#;
    let ds = prior_state_data_sources(plan_json).unwrap();
    assert_eq!(
        ds,
        vec![
            "data.google_secret_manager_secret_version.x",
            "module.m.data.aws_caller_identity.current"
        ]
    );
    assert!(prior_state_data_sources(br#"{"format_version":"1.2"}"#).is_none());
}

// Go: TestPriorStateAddrs_extractsAddresses.
#[test]
fn prior_state_addrs_extracts_addresses() {
    let p = "registry.terraform.io/hashicorp/google";
    let json = format!(
        r#"{{"prior_state":{{"values":{{"root_module":{{"resources":[
          {{"address":"google_project_iam_member.x","mode":"managed","type":"google_project_iam_member","provider_name":"{p}"}}],
          "child_modules":[{{"address":"module.m","resources":[
          {{"address":"module.m.google_service_account.y","mode":"managed","provider_name":"{p}"}}]}}]}}}}}}}}"#
    );
    let addrs = prior_state_addrs(json.as_bytes()).unwrap();
    assert_eq!(addrs.len(), 2);
    assert_eq!(addrs["google_project_iam_member.x"], p);
    assert_eq!(addrs["module.m.google_service_account.y"], p);
}

// Go: TestPriorStateAddrs_nilWhenNoPriorState.
#[test]
fn prior_state_addrs_none_without_prior_state() {
    assert!(prior_state_addrs(br#"{}"#).is_none());
    assert!(prior_state_addrs(b"not json").is_none());
}

// Go: TestStateAddressesSkipsDataSources.
#[test]
fn state_addresses_skip_data_sources() {
    let mut data = StateResource::managed(
        "data.google_secret_manager_secret_version.x",
        "registry.terraform.io/hashicorp/google",
    );
    data.mode = "data".into();
    let mut child_data = StateResource::managed(
        "module.m.data.aws_caller_identity.current",
        "registry.terraform.io/hashicorp/aws",
    );
    child_data.mode = "data".into();
    let s = State {
        format_version: "1.0".into(),
        values: Some(StateValues {
            root_module: Some(StateModule {
                resources: vec![
                    StateResource::managed(
                        "google_project.p",
                        "registry.terraform.io/hashicorp/google",
                    ),
                    data,
                ],
                child_modules: vec![StateModule {
                    resources: vec![
                        StateResource::managed(
                            "module.m.aws_s3_bucket.b",
                            "registry.terraform.io/hashicorp/aws",
                        ),
                        child_data,
                    ],
                    child_modules: vec![],
                }],
            }),
        }),
    };
    let got: Vec<String> = state_addresses(&s).into_keys().collect();
    assert_eq!(got, vec!["google_project.p", "module.m.aws_s3_bucket.b"]);
}
