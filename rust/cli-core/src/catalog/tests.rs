use super::*;

// Go: TestParseWatches.
#[test]
fn parse_watches_reads_the_stack_block() {
    let src = r#"
	stack {
		name  = "test"
		id    = "test-id"
		watch = [
			"../../components/am",
			"../../components/apim"
		]
	}
	"#;
    assert_eq!(
        parse_watches(src).unwrap(),
        vec!["../../components/am", "../../components/apim"]
    );
}

// Go: TestParseRunGraph.
#[test]
fn parse_run_graph_edges() {
    let dot = "digraph  {\n\tn1[label=\"/stacks/a\"];\n\tn2[label=\"/stacks/b\"];\n\tn3[label=\"/stacks/c\"];\n\tn1->n2;\n\tn1->n3;\n}";
    assert_eq!(
        parse_run_graph(dot),
        vec![
            StackEdge {
                from: "stacks/a".into(),
                to: "stacks/b".into()
            },
            StackEdge {
                from: "stacks/a".into(),
                to: "stacks/c".into()
            },
        ]
    );
}

// Go: TestParseRunGraphEmpty.
#[test]
fn parse_run_graph_empty() {
    assert!(parse_run_graph("digraph {\n}").is_empty());
}

// Go: TestParseRunGraphStripsLeadingSlash.
#[test]
fn parse_run_graph_strips_leading_slash() {
    assert_eq!(
        parse_run_graph(r#"digraph { n1[label="/a"]; n2[label="/b"]; n1->n2; }"#),
        vec![StackEdge {
            from: "a".into(),
            to: "b".into()
        }]
    );
}

#[test]
fn components_group_and_connect() {
    let stacks: Vec<String> = [
        "stacks/nonprod/am/dev",
        "stacks/nonprod/am/test",
        "stacks/nonprod/apim/dev",
        "tools/x",
    ]
    .iter()
    .map(|s| s.to_string())
    .collect();
    let watches = |s: &str| {
        if s == "stacks/nonprod/apim/dev" {
            vec![
                "../../components/am".to_string(),
                "../../components/am".to_string(),
            ]
        } else {
            vec![]
        }
    };
    let graph = vec![
        StackEdge {
            from: "stacks/nonprod/am/dev".into(),
            to: "stacks/nonprod/apim/dev".into(),
        },
        StackEdge {
            from: "stacks/nonprod/am/dev".into(),
            to: "stacks/nonprod/am/test".into(),
        },
    ];
    let c = build(&stacks, &watches, &graph);
    let ids: Vec<&str> = c.components.iter().map(|c| c.id.as_str()).collect();
    assert_eq!(ids, vec!["nonprod/am", "nonprod/apim", "tools/x"]);
    assert_eq!(c.components[1].watches, vec!["../../components/am"]);
    assert_eq!(
        c.edges,
        vec![
            Edge {
                from: "nonprod/am".into(),
                to: "nonprod/apim".into(),
                kind: "watch".into()
            },
            Edge {
                from: "nonprod/am".into(),
                to: "nonprod/apim".into(),
                kind: "dependency".into()
            },
        ]
    );
    let json = crate::gojson::marshal(&to_json(&Catalog::default())).unwrap();
    assert_eq!(json, r#"{"components":null,"edges":null}"#);
}
