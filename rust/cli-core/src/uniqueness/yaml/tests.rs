use super::*;

fn doc(src: &str) -> Vec<(String, Value)> {
    unmarshal_map(src).unwrap().unwrap()
}

fn get<'a>(d: &'a [(String, Value)], k: &str) -> &'a Value {
    &d.iter().find(|(key, _)| key == k).unwrap().1
}

#[test]
fn plain_scalars_resolve_like_yaml_v3() {
    let d = doc(
        "a: 1\nb: 0x1F\nc: 017\nd: 1_000\ne: 1.5\nf: .5\ng: true\nh: yes\ni: ~\nj: null\nk: \"1\"\nl: 'true'\nm: 1e3\nn: 18446744073709551615\no: 99999999999999999999\np: 0b101\nq: 0o17\nr: -.inf\ns: abc\nt:\n",
    );
    assert_eq!(get(&d, "a"), &Value::Int(1));
    assert_eq!(get(&d, "b"), &Value::Int(31));
    assert_eq!(get(&d, "c"), &Value::Int(15));
    assert_eq!(get(&d, "d"), &Value::Int(1000));
    assert_eq!(get(&d, "e"), &Value::Float(1.5));
    assert_eq!(get(&d, "f"), &Value::Float(0.5));
    assert_eq!(get(&d, "g"), &Value::Bool(true));
    assert_eq!(get(&d, "h"), &Value::str("yes"));
    assert_eq!(get(&d, "i"), &Value::Nil);
    assert_eq!(get(&d, "j"), &Value::Nil);
    assert_eq!(get(&d, "k"), &Value::str("1"));
    assert_eq!(get(&d, "l"), &Value::str("true"));
    assert_eq!(get(&d, "m"), &Value::Float(1000.0));
    assert_eq!(get(&d, "n"), &Value::Uint(u64::MAX));
    assert_eq!(get(&d, "o"), &Value::Float(1e20));
    assert_eq!(get(&d, "p"), &Value::Int(5));
    assert_eq!(get(&d, "q"), &Value::Int(15));
    assert_eq!(get(&d, "r"), &Value::Float(f64::NEG_INFINITY));
    assert_eq!(get(&d, "s"), &Value::str("abc"));
    assert_eq!(get(&d, "t"), &Value::Nil);
}

#[test]
fn timestamps_decode_to_time() {
    let d = doc("a: 2024-01-15\nb: 2024-1-5T10:00:00.5+05:30\nc: '2024-01-15'\nd: 2024-13-01\n");
    assert_eq!(get(&d, "a").sprint(), "2024-01-15 00:00:00 +0000 UTC");
    assert_eq!(get(&d, "b").sprint(), "2024-01-05 10:00:00.5 +0530 +0530");
    assert_eq!(get(&d, "c"), &Value::str("2024-01-15"));
    assert_eq!(get(&d, "d"), &Value::str("2024-13-01"));
}

#[test]
fn nested_maps_lists_and_anchors() {
    let d = doc("base: &b\n  x: 1\nuse: *b\nlist: [a, 2]\nweird:\n  1: one\n");
    assert_eq!(
        get(&d, "use"),
        &Value::Map(vec![("x".into(), Value::Int(1))])
    );
    assert_eq!(
        get(&d, "list"),
        &Value::Seq(vec![Value::str("a"), Value::Int(2)])
    );
    assert_eq!(
        get(&d, "weird"),
        &Value::AnyMap(vec![(Value::Int(1), Value::str("one"))])
    );
}

#[test]
fn top_level_shapes() {
    assert_eq!(unmarshal_map("").unwrap(), None);
    assert_eq!(unmarshal_map("~\n").unwrap(), None);
    assert_eq!(
        unmarshal_map("foo\n").unwrap_err(),
        "yaml: unmarshal errors:\n  line 1: cannot unmarshal !!str `foo` into map[string]interface {}"
    );
    assert_eq!(
        unmarshal_map("- a\n").unwrap_err(),
        "yaml: unmarshal errors:\n  line 1: cannot unmarshal !!seq into map[string]interface {}"
    );
    assert_eq!(
        unmarshal_map("a: 1\na: 2\n").unwrap_err(),
        "yaml: unmarshal errors:\n  line 2: mapping key \"a\" already defined at line 1"
    );
    // Top-level keys decode into strings from their source text.
    assert_eq!(doc("1: x\n"), vec![("1".to_string(), Value::str("x"))]);
}
