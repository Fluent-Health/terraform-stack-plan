use super::*;

fn edge(f: &str, t: &str) -> Edge {
    Edge {
        from: f.into(),
        to: t.into(),
    }
}

fn strings(a: &[&str]) -> Vec<String> {
    a.iter().map(|s| s.to_string()).collect()
}

// Go: TestNormalizeEdges.
#[test]
fn normalize_edges_maps_onto_listed_stacks() {
    let stacks = strings(&[
        "cluster/dev-svc",
        "workloads/agent/dev-svc",
        "projects/dev-svc",
    ]);
    let edges = vec![
        edge(
            "stacks/nonprod/projects/dev-svc",
            "stacks/nonprod/cluster/dev-svc",
        ),
        edge(
            "stacks/nonprod/cluster/dev-svc",
            "stacks/nonprod/workloads/agent/dev-svc",
        ),
        edge(
            "stacks/nonprod/sql/dev-svc",
            "stacks/nonprod/workloads/agent/dev-svc",
        ),
        edge(
            "stacks/nonprod/observability/a/dev-svc",
            "stacks/nonprod/observability/b/dev-svc",
        ),
        edge("projects/dev-svc", "workloads/agent/dev-svc"),
    ];
    assert_eq!(
        normalize_edges(&stacks, &edges),
        vec![
            edge("projects/dev-svc", "cluster/dev-svc"),
            edge("cluster/dev-svc", "workloads/agent/dev-svc"),
            edge("projects/dev-svc", "workloads/agent/dev-svc"),
        ]
    );
}

// Go: TestNormalizeEdgesExactBeforeSuffix.
#[test]
fn normalize_edges_exact_before_suffix() {
    let stacks = strings(&["agent/dev-svc", "workloads/agent/dev-svc"]);
    let edges = vec![edge("agent/dev-svc", "workloads/agent/dev-svc")];
    assert_eq!(normalize_edges(&stacks, &edges), edges);
}

// Go: TestParseWhyChanged.
#[test]
fn parse_why_changed() {
    let r = parse_why_line(
        r#"stacks/nonprod/am/dev-svc - stack changed because "../../../../components/am/am" changed because module "../../../../components/am/am" has unmerged changes"#,
    );
    assert_eq!(
        (r.kind.as_str(), r.via.clone()),
        ("module", strings(&["../../../../components/am/am"]))
    );
    assert_eq!(r.stack, "stacks/nonprod/am/dev-svc");
    let r = parse_why_line(
        r#"stacks/nonprod/db/postgres - stack changed because "../../../../components/db/postgres" changed because watch file "../../../../components/db/postgres/main.tf" changed"#,
    );
    assert_eq!(r.kind, "watch");
    let r = parse_why_line(
        r#"stacks/nonprod/networking - stack changed because "stacks/nonprod/networking" changed because file "stacks/nonprod/networking/main.tf" changed"#,
    );
    assert_eq!(
        (r.kind.as_str(), r.via.clone()),
        ("direct", strings(&["stacks/nonprod/networking"]))
    );
    let r = parse_why_line("stacks/nonprod/unknown");
    assert_eq!(
        (r.stack.as_str(), r.kind.as_str()),
        ("stacks/nonprod/unknown", "unknown")
    );
    assert!(r.via.is_empty());
    let r = parse_why_line("stacks/b - stack has unmerged changes");
    assert_eq!(
        (r.stack.as_str(), r.kind.as_str(), r.via.clone()),
        ("stacks/b", "direct", strings(&["stacks/b"]))
    );
}

// Go: TestGatesFromSidecar.
#[test]
fn gates_and_moving_from_sidecar() {
    let sidecar = r#"{
      "stacks": {
        "stacks/a": {"categories": [{"category":"iam","icon":"🔐","attributes":{"project":["proj-a"]}}]},
        "stacks/c": {"categories": [{"category":"move","icon":"🚚"}]}
      },
      "summary": {"categories": [
        {"category":"iam","icon":"🔐","attributes":{"project":["proj-a","proj-b"]}},
        {"category":"destructive","icon":"💣"}
      ]}
    }"#;
    let v = read_sidecar(sidecar.as_bytes(), &strings(&["iam"])).unwrap();
    assert_eq!(
        v.gates,
        vec![
            GateTarget {
                class: "iam".into(),
                target: "proj-a".into()
            },
            GateTarget {
                class: "iam".into(),
                target: "proj-b".into()
            },
        ]
    );
    assert_eq!(v.moving, strings(&["stacks/c"]));
}

// Go: TestGatesFromSidecarNoGatingClasses, TestGatesFromSidecarEmpty.
#[test]
fn no_gating_classes_or_empty_sidecar() {
    let s = r#"{"stacks":{},"summary":{"categories":[{"category":"iam","attributes":{"project":["p"]}}]}}"#;
    assert!(read_sidecar(s.as_bytes(), &[]).unwrap().gates.is_empty());
    let v = read_sidecar(
        br#"{"stacks":{},"summary":{"categories":[]}}"#,
        &strings(&["iam"]),
    )
    .unwrap();
    assert!(v.gates.is_empty() && v.moving.is_empty());
}

// Go: TestCountsFromSidecar.
#[test]
fn counts_from_sidecar() {
    let s = r#"{"stacks":{"a":{"categories":[],"counts":{"add":6,"change":2}},"b":{"categories":[],"counts":{"destroy":2}}},"summary":{"categories":[]}}"#;
    let v = read_sidecar(s.as_bytes(), &[]).unwrap();
    assert_eq!(
        (
            v.counts["a"].add,
            v.counts["a"].change,
            v.counts["b"].destroy
        ),
        (6, 2, 2)
    );
}

// Go: TestCategoriesFromSidecar.
#[test]
fn categories_from_sidecar() {
    let s = r#"{"stacks":{
        "stacks/a":{"categories":[{"category":"iam","icon":"🔐"},{"category":"destructive","icon":"💣"}]},
        "stacks/b":{"categories":[{"category":"safe","icon":null}]}
    }}"#;
    let v = read_sidecar(s.as_bytes(), &[]).unwrap();
    assert_eq!(v.categories["stacks/a"].len(), 2);
    assert_eq!(v.categories["stacks/a"][0].icon, "🔐");
    assert_eq!(v.categories["stacks/b"][0].icon, "");
}

// Go: TestGateCheckVerdicts and TestGateCheckAuthRejectionNamesTheCause.
#[test]
fn gate_verdicts() {
    let cases = [
        (
            200,
            r#"{"requester":"sa@x"}"#,
            VerdictKind::Satisfied,
            "sa@x",
        ),
        (
            409,
            r#"{"code":"GATE-001","message":"not classified"}"#,
            VerdictKind::NotClassified,
            "",
        ),
        (
            409,
            r#"{"code":"GATE-002","message":"gate not satisfied"}"#,
            VerdictKind::NotSatisfied,
            "",
        ),
        (
            503,
            r#"{"code":"GATE-003","message":"x"}"#,
            VerdictKind::Unconfirmable,
            "",
        ),
        (
            409,
            r#"{"code":"ZZZ-999","message":"x"}"#,
            VerdictKind::NotSatisfied,
            "",
        ),
    ];
    for (status, body, kind, req) in cases {
        let v = gate_verdict(status, body.as_bytes());
        assert_eq!((v.kind, v.requester.as_str()), (kind, req), "{body}");
    }
    let v = gate_verdict(401, b"unauthorized\n");
    assert_eq!(v.kind, VerdictKind::NotSatisfied);
    assert!(v.err.contains("API auth rejected"), "{}", v.err);
    assert!(v.err.contains("TFSTACKPLAN_AUDIENCE"));
}

// Go: TestPrintGateVerdictBuckets.
#[test]
fn gate_guidance_buckets() {
    for (kind, want) in [
        (VerdictKind::NotClassified, "AWAITING_APPROVAL"),
        (VerdictKind::NotSatisfied, "AWAITING_APPROVAL"),
        (VerdictKind::Unconfirmable, "GATE_UNREACHABLE"),
        (VerdictKind::Unreachable, "GATE_UNREACHABLE"),
    ] {
        let v = Verdict {
            kind,
            requester: String::new(),
            err: "x".into(),
        };
        assert!(gate_guidance(&v, "", 42).contains(want), "{kind:?}");
    }
}

#[test]
fn summary_and_atoi() {
    assert_eq!(gate_summary(&[]), "none");
    assert_eq!(
        gate_summary(&[GateTarget {
            class: "iam".into(),
            target: "p".into()
        }]),
        "iam/p"
    );
    assert_eq!(atoi_or_0("42"), 42);
    assert_eq!(atoi_or_0("+7"), 7);
    assert_eq!(atoi_or_0("x"), 0);
    assert_eq!(atoi_or_0(""), 0);
}
