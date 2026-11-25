use super::*;

const AWS: &str = "registry.terraform.io/hashicorp/aws";
const GOOGLE: &str = "registry.terraform.io/hashicorp/google";

fn set(pairs: &[(&str, &str)]) -> AddressSet {
    pairs
        .iter()
        .map(|(a, p)| (a.to_string(), p.to_string()))
        .collect()
}

fn providers(names: &[&str]) -> DestProviders {
    names.iter().map(|s| s.to_string()).collect()
}

fn xm(pairs: &[(&str, &str)]) -> XMove {
    XMove {
        source_stack: String::new(),
        pairs: pairs.iter().map(|(f, t)| Move::new(f, t)).collect(),
    }
}

fn codes(d: &[Diagnostic]) -> Vec<&str> {
    d.iter().map(|d| d.code.as_str()).collect()
}

// Go: TestDecide.
#[test]
fn decide_table() {
    let x = set(&[("aws_s3_bucket.x", AWS)]);
    assert_eq!(
        decide(&x, &AddressSet::new(), "aws_s3_bucket.x", "aws_s3_bucket.x"),
        Ok(Decision::Move)
    );
    let ab = set(&[("a.b", AWS)]);
    assert_eq!(
        decide(&AddressSet::new(), &ab, "a.b", "a.b"),
        Ok(Decision::Skip)
    );
    assert!(decide(&ab, &ab, "a.b", "a.b").is_err());
    assert!(decide(&AddressSet::new(), &AddressSet::new(), "a.b", "a.b").is_err());
}

// Go: TestExpandPairs.
#[test]
fn expand_pairs_cases() {
    let src = set(&[
        ("module.a[0].res.x", AWS),
        ("module.a[0].res.y[\"k\"]", AWS),
        ("module.a[0].module.sub.res.z", AWS),
    ]);
    assert_eq!(
        expand_pairs(
            &src,
            &AddressSet::new(),
            &[Move::new("module.a[0]", "module.b")]
        ),
        vec![
            Move::new("module.a[0].module.sub.res.z", "module.b.module.sub.res.z"),
            Move::new("module.a[0].res.x", "module.b.res.x"),
            Move::new("module.a[0].res.y[\"k\"]", "module.b.res.y[\"k\"]"),
        ]
    );

    let dst = set(&[("module.b.res.x", AWS)]);
    assert_eq!(
        expand_pairs(
            &AddressSet::new(),
            &dst,
            &[Move::new("module.a[0]", "module.b")]
        ),
        vec![Move::new("module.a[0].res.x", "module.b.res.x")]
    );

    assert_eq!(
        expand_pairs(
            &set(&[("aws_s3_bucket.x", AWS)]),
            &AddressSet::new(),
            &[Move::new("aws_s3_bucket.x", "aws_s3_bucket.x")]
        ),
        vec![Move::new("aws_s3_bucket.x", "aws_s3_bucket.x")]
    );

    assert_eq!(
        expand_pairs(
            &AddressSet::new(),
            &AddressSet::new(),
            &[Move::new("module.a[0]", "module.b")]
        ),
        vec![Move::new("module.a[0]", "module.b")]
    );

    let src_longest = set(&[("module.a[0].res.x", AWS), ("module.a[0].res.y", AWS)]);
    let mut got = expand_pairs(
        &src_longest,
        &AddressSet::new(),
        &[
            Move::new("module.a[0].res.y", "module.b.res.z"),
            Move::new("module.a[0]", "module.b"),
        ],
    );
    got.sort();
    assert_eq!(
        got,
        vec![
            Move::new("module.a[0].res.x", "module.b.res.x"),
            Move::new("module.a[0].res.y", "module.b.res.z"),
        ]
    );
}

// Go: TestValidateMovePlan.
#[test]
fn validate_move_plan_cases() {
    let src = set(&[("google_artifact_registry_repository.main", GOOGLE)]);
    let none = AddressSet::new();
    let google = providers(&["google"]);
    let m = xm(&[(
        "google_artifact_registry_repository.main",
        "google_artifact_registry_repository.main",
    )]);
    assert!(validate_move_plan(&src, &none, &none, &google, &m, true).is_empty());
    assert_eq!(
        codes(&validate_move_plan(
            &src,
            &none,
            &none,
            &providers(&[]),
            &m,
            true
        )),
        vec!["xmove/provider-missing"]
    );
    let wrong = xm(&[(
        "google_artifact_registry_repository.main[0]",
        "google_artifact_registry_repository.main",
    )]);
    assert_eq!(
        codes(&validate_move_plan(
            &src, &none, &none, &google, &wrong, true
        )),
        vec!["xmove/source-missing"]
    );
    let occupied = set(&[("google_artifact_registry_repository.main", GOOGLE)]);
    assert_eq!(
        codes(&validate_move_plan(
            &src, &occupied, &none, &google, &m, true
        )),
        vec!["xmove/dest-occupied"]
    );
    assert!(validate_move_plan(&src, &occupied, &none, &google, &m, false).is_empty());
    assert_eq!(
        codes(&validate_move_plan(
            &none, &occupied, &none, &google, &m, false
        )),
        vec!["xmove/source-missing"]
    );
}

// Go: TestValidateMovePlan_SpentEntry.
#[test]
fn validate_move_plan_spent_entry() {
    let google = providers(&["google"]);
    let none = AddressSet::new();
    let m = xm(&[(
        "google_storage_bucket.a",
        "module.dst.google_storage_bucket.a",
    )]);
    let dst_changes = set(&[("google_storage_bucket.other", GOOGLE)]);
    let dst_prior = set(&[("module.dst.google_storage_bucket.a", GOOGLE)]);
    let d = validate_move_plan(&none, &dst_changes, &dst_prior, &google, &m, false);
    assert_eq!(codes(&d), vec!["xmove/spent"]);
    assert_eq!(d[0].severity, Severity::Warning);

    let m_mod = xm(&[("module.a", "module.b")]);
    let prior_mod = set(&[("module.b.google_storage_bucket.x", GOOGLE)]);
    assert_eq!(
        codes(&validate_move_plan(
            &none, &none, &prior_mod, &google, &m_mod, false
        )),
        vec!["xmove/spent"]
    );

    let unrelated = set(&[("module.other.google_storage_bucket.y", GOOGLE)]);
    let d = validate_move_plan(&none, &none, &unrelated, &google, &m, false);
    assert_eq!(codes(&d), vec!["xmove/source-missing"]);
    assert_eq!(d[0].severity, Severity::Error);

    let mixed = xm(&[
        (
            "google_storage_bucket.pending",
            "module.dst.google_storage_bucket.pending",
        ),
        (
            "google_storage_bucket.a",
            "module.dst.google_storage_bucket.a",
        ),
    ]);
    let src = set(&[("google_storage_bucket.pending", GOOGLE)]);
    let dst_pending = set(&[("module.dst.google_storage_bucket.pending", GOOGLE)]);
    assert_eq!(
        codes(&validate_move_plan(
            &src,
            &dst_pending,
            &dst_prior,
            &google,
            &mixed,
            false
        )),
        vec!["xmove/spent"]
    );
}

// Go: TestValidateMovePlan_BuiltinTerraformProvider.
#[test]
fn validate_move_plan_builtin_terraform_provider() {
    let a = "module.analytics.terraform_data.loinc_csv_file";
    let src = set(&[(a, "terraform.io/builtin/terraform")]);
    let m = xm(&[(a, a)]);
    let none = AddressSet::new();
    assert!(validate_move_plan(&src, &none, &none, &providers(&[]), &m, true).is_empty());
    let dst = set(&[(a, "terraform.io/builtin/terraform")]);
    assert!(validate_move_plan(&src, &dst, &none, &providers(&[]), &m, false).is_empty());
}

// Go: TestValidateMovePlan_ProviderMismatch.
#[test]
fn validate_move_plan_provider_mismatch() {
    let src = set(&[("module.a.google_project_iam_member.x", GOOGLE)]);
    let dst = set(&[("module.b.aws_iam_role.x", AWS)]);
    let d = validate_move_plan(
        &src,
        &dst,
        &AddressSet::new(),
        &providers(&["aws"]),
        &xm(&[("module.a", "module.b")]),
        false,
    );
    assert!(codes(&d).contains(&"xmove/provider-mismatch"), "{d:?}");
}

// Go: TestDiscoverDestProviders (over file contents; the directory read is the binary's).
#[test]
fn dest_providers_from_tf_contents() {
    let content =
        "\nprovider \"google\" {\n  project = \"my-project\"\n}\nprovider \"postgresql\" {}\n";
    assert_eq!(
        dest_providers([content]),
        providers(&["google", "postgresql"])
    );
}

// Go: TestIsSpent.
#[test]
fn is_spent_cases() {
    let pairs = [Move::new("module.x", "module.y")];
    let dst_has = set(&[
        ("module.y.google_project.p", GOOGLE),
        ("module.y.aws_s3_bucket.b", AWS),
    ]);
    assert!(is_spent(&pairs, &dst_has));
    assert!(!is_spent(&pairs, &AddressSet::new()));
    assert!(!is_spent(
        &pairs,
        &set(&[("module.z.google_project.p", "p")])
    ));
    assert!(is_spent(&[Move::new("a.x", "a.x")], &set(&[("a.x", "p")])));
    let multi = [
        Move::new("module.a", "module.b"),
        Move::new("module.c", "module.d"),
    ];
    assert!(!is_spent(&multi, &set(&[("module.b.r.x", "p")])));
    assert!(is_spent(
        &multi,
        &set(&[("module.b.r.x", "p"), ("module.d.r.y", "p")])
    ));
}

// Go: TestDataSourceOrphans.
#[test]
fn data_source_orphans_cases() {
    let pairs = [Move::new("module.x", "module.y")];
    let ds: Vec<String> = [
        "module.x.data.google_secret_manager_secret_version.v",
        "module.z.data.something.else",
        "data.other.ds",
    ]
    .iter()
    .map(|s| s.to_string())
    .collect();
    assert_eq!(
        data_source_orphans(&pairs, &ds),
        vec!["module.x.data.google_secret_manager_secret_version.v"]
    );
    assert!(data_source_orphans(&pairs, &["data.other.ds".to_string()]).is_empty());
    let multi = [
        Move::new("module.a", "module.b"),
        Move::new("module.c", "module.d"),
    ];
    let ds2: Vec<String> = [
        "module.a.data.ds.x",
        "module.c.data.ds.y",
        "module.z.data.ds.z",
    ]
    .iter()
    .map(|s| s.to_string())
    .collect();
    assert_eq!(data_source_orphans(&multi, &ds2).len(), 2);
}

#[test]
fn module_prefix_cases() {
    assert_eq!(module_prefix("aws_s3_bucket.x"), "");
    assert_eq!(module_prefix("module.a.aws_s3_bucket.x"), "module.a");
    assert_eq!(
        module_prefix("module.a.module.b.res.x"),
        "module.a.module.b"
    );
    assert_eq!(module_prefix("module.a"), "module.a");
}
