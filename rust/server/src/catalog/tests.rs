// Ported from internal/catalog/catalog_test.go and internal/runner/rungraph_test.go.
use super::*;

#[test]
fn parse_watches_reads_the_stack_watch_list() {
    let dir = tempfile::Builder::new().tempdir().unwrap();
    let fp = dir.path().join("stack.tm.hcl");
    std::fs::write(
        &fp,
        "\n\tstack {\n\t\tname  = \"test\"\n\t\tid    = \"test-id\"\n\t\twatch = [\n\t\t\t\"../../components/am\",\n\t\t\t\"../../components/apim\"\n\t\t]\n\t}\n\t",
    )
    .unwrap();
    assert_eq!(
        parse_watches(&fp).unwrap(),
        Some(vec![
            "../../components/am".to_string(),
            "../../components/apim".to_string()
        ])
    );
}

#[test]
fn component_grouping() {
    assert_eq!(component_of("stacks/nonprod/am/fh-dev-svc"), "nonprod/am");
    assert_eq!(component_of("infra/vpc/x"), "infra/vpc");
    assert_eq!(component_of("infra/vpc"), "infra/vpc");
}

#[test]
fn run_graph_edges_by_label() {
    let dot =
        "digraph {\n n1[label=\"/stacks/a\"]\n n2 [label=\"/stacks/b\"]\n n1 -> n2\n n2->n9\n}";
    assert_eq!(
        parse_run_graph(dot),
        vec![("stacks/a".to_string(), "stacks/b".to_string())]
    );
}
