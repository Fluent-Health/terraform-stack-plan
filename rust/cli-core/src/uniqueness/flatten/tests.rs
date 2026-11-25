use std::collections::BTreeMap;

use super::*;

fn m(entries: &[(&str, Value)]) -> Vec<(String, Value)> {
    entries
        .iter()
        .map(|(k, v)| (k.to_string(), v.clone()))
        .collect()
}

fn want(entries: &[(&str, Value)]) -> BTreeMap<String, Value> {
    entries
        .iter()
        .map(|(k, v)| (k.to_string(), v.clone()))
        .collect()
}

// Go: TestFlatten.
#[test]
fn flatten_maps_lists_scalars() {
    let got = flatten(&m(&[
        ("a", Value::Map(m(&[("b", Value::Int(1))]))),
        ("c", Value::Seq(vec![Value::str("x"), Value::str("y")])),
        ("d", Value::Bool(true)),
    ]));
    assert_eq!(
        got,
        want(&[
            ("a.b", Value::Int(1)),
            ("c", Value::list(&["x", "y"])),
            ("d", Value::Bool(true)),
        ])
    );
}

// Go: TestFlattenNestedLists.
#[test]
fn flatten_nested_lists() {
    let got = flatten(&m(&[(
        "top",
        Value::Map(m(&[(
            "mid",
            Value::Map(m(&[
                ("leaf", Value::str("value")),
                (
                    "nums",
                    Value::Seq(vec![Value::Int(1), Value::Int(2), Value::Int(3)]),
                ),
                ("empty", Value::Seq(vec![])),
            ])),
        )])),
    )]));
    assert_eq!(
        got,
        want(&[
            ("top.mid.leaf", Value::str("value")),
            ("top.mid.nums", Value::list(&["1", "2", "3"])),
            ("top.mid.empty", Value::list(&[])),
        ])
    );
}

// Go: TestFlattenTopLevelScalar.
#[test]
fn flatten_top_level_scalar() {
    assert_eq!(
        flatten(&m(&[("n", Value::Int(42))])),
        want(&[("n", Value::Int(42))])
    );
}

#[test]
fn list_elements_print_like_fmt_sprint() {
    let got = flatten(&m(&[(
        "l",
        Value::Seq(vec![
            Value::Float(1.5),
            Value::Nil,
            Value::Map(m(&[("k", Value::Bool(false))])),
            Value::Seq(vec![Value::str("a"), Value::Int(2)]),
        ]),
    )]));
    assert_eq!(
        got["l"],
        Value::list(&["1.5", "<nil>", "map[k:false]", "[a 2]"])
    );
}
