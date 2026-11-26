//! `yaml.Unmarshal(data, &map[string]any{})` as Go's `yaml.v3` does it.
//!
//! The values, merge keys included, are decoded by `tfstackplan_render::yaml`, the
//! render port's yaml.v3 decoder. This module walks the document once more with
//! line numbers, only to word yaml.v3's `unmarshal errors` the way Go prints them
//! (a duplicate key, a non-scalar key, a root that is not a mapping). Syntax errors
//! carry `yaml-rust2`'s wording, not yaml.v3's.

use std::collections::BTreeMap;
use yaml_rust2::parser::{Event, MarkedEventReceiver, Parser};
use yaml_rust2::scanner::Marker;

use tfstackplan_render::goval::Value as GoValue;

use super::value::{Timestamp, Value};

#[derive(Clone, Debug)]
enum Node {
    Scalar { value: String, line: usize },
    Seq(Vec<Node>, usize),
    Map(Vec<(Node, Node)>, usize),
}

enum Frame {
    Seq(Vec<Node>, usize, usize),
    Map(Vec<Node>, usize, usize),
}

#[derive(Default)]
struct Builder {
    stack: Vec<Frame>,
    anchors: BTreeMap<usize, Node>,
    root: Option<Node>,
}

impl Builder {
    fn push(&mut self, node: Node, anchor: usize) {
        if anchor > 0 {
            self.anchors.insert(anchor, node.clone());
        }
        match self.stack.last_mut() {
            Some(Frame::Seq(items, _, _)) | Some(Frame::Map(items, _, _)) => items.push(node),
            None => {
                if self.root.is_none() {
                    self.root = Some(node);
                }
            }
        }
    }
}

impl MarkedEventReceiver for Builder {
    fn on_event(&mut self, ev: Event, mark: Marker) {
        match ev {
            Event::Scalar(value, _, anchor, _) => self.push(
                Node::Scalar {
                    value,
                    line: mark.line(),
                },
                anchor,
            ),
            Event::SequenceStart(anchor, _) => {
                self.stack.push(Frame::Seq(Vec::new(), anchor, mark.line()));
            }
            Event::MappingStart(anchor, _) => {
                self.stack.push(Frame::Map(Vec::new(), anchor, mark.line()));
            }
            Event::SequenceEnd | Event::MappingEnd => {
                let (node, anchor) = match self.stack.pop() {
                    Some(Frame::Seq(items, a, line)) => (Node::Seq(items, line), a),
                    Some(Frame::Map(items, a, line)) => {
                        let mut pairs = Vec::new();
                        let mut it = items.into_iter();
                        while let (Some(k), Some(v)) = (it.next(), it.next()) {
                            pairs.push((k, v));
                        }
                        (Node::Map(pairs, line), a)
                    }
                    None => return,
                };
                self.push(node, anchor);
            }
            Event::Alias(id) => {
                let node = self.anchors.get(&id).cloned().unwrap_or(Node::Scalar {
                    value: String::new(),
                    line: mark.line(),
                });
                self.push(node, 0);
            }
            _ => {}
        }
    }
}

/// Converts a value decoded by the render port's yaml.v3 decoder.
fn from_go(v: GoValue) -> Value {
    match v {
        GoValue::Null => Value::Nil,
        GoValue::Bool(b) => Value::Bool(b),
        GoValue::Int(i) => Value::Int(i),
        GoValue::Uint(u) => Value::Uint(u),
        GoValue::Float(f) => Value::Float(f),
        GoValue::Str(s) => Value::Str(s),
        GoValue::Time(t) => {
            let (year, month, day, hour, minute, second, nanos, off) = t.fields();
            Value::Time(Timestamp {
                year,
                month,
                day,
                hour,
                minute,
                second,
                nanos,
                offset: (off != 0).then_some(off),
            })
        }
        GoValue::Seq(items) => Value::Seq(items.into_iter().map(from_go).collect()),
        GoValue::Map(m) => Value::Map(m.into_iter().map(|(k, v)| (k, from_go(v))).collect()),
        GoValue::AnyMap(pairs) => Value::AnyMap(
            pairs
                .into_iter()
                .map(|(k, v)| (from_go(k), from_go(v)))
                .collect(),
        ),
    }
}

/// Fails with yaml.v3's text on a duplicate key anywhere in the tree.
fn check_tree(n: &Node) -> Result<(), String> {
    match n {
        Node::Scalar { .. } => Ok(()),
        Node::Seq(items, _) => items.iter().try_for_each(check_tree),
        Node::Map(pairs, _) => {
            check_unique_keys(pairs)?;
            pairs
                .iter()
                .try_for_each(|(k, v)| check_tree(k).and_then(|()| check_tree(v)))
        }
    }
}

fn node_line(n: &Node) -> usize {
    match n {
        Node::Scalar { line, .. } | Node::Seq(_, line) | Node::Map(_, line) => *line,
    }
}

fn check_unique_keys(pairs: &[(Node, Node)]) -> Result<(), String> {
    for (i, (k, _)) in pairs.iter().enumerate() {
        let Node::Scalar { value, .. } = k else {
            continue;
        };
        for (prev, _) in &pairs[..i] {
            if let Node::Scalar { value: pv, .. } = prev
                && pv == value
            {
                return Err(format!(
                    "yaml: unmarshal errors:\n  line {}: mapping key {} already defined at line {}",
                    node_line(k),
                    crate::gofmt::quote(value),
                    node_line(prev)
                ));
            }
        }
    }
    Ok(())
}

fn short_tag(v: &Value) -> &'static str {
    match v {
        Value::Nil => "!!null",
        Value::Bool(_) => "!!bool",
        Value::Int(_) | Value::Uint(_) => "!!int",
        Value::Float(_) => "!!float",
        Value::Time(_) => "!!timestamp",
        _ => "!!str",
    }
}

/// `yaml.Unmarshal` into a `map[string]any`. `Ok(None)` is an empty or null
/// document (Go leaves the map nil).
pub fn unmarshal_map(src: &str) -> Result<Option<Vec<(String, Value)>>, String> {
    let mut b = Builder::default();
    let mut parser = Parser::new_from_str(src);
    parser
        .load(&mut b, false)
        .map_err(|e| format!("yaml: {e}"))?;
    let Some(root) = b.root else {
        return Ok(None);
    };
    match &root {
        Node::Map(pairs, _) => {
            check_tree(&root)?;
            for (k, _) in pairs {
                if let Node::Seq(..) | Node::Map(..) = k {
                    return Err(format!(
                        "yaml: unmarshal errors:\n  line {}: cannot unmarshal {} into string",
                        node_line(k),
                        if matches!(k, Node::Seq(..)) {
                            "!!seq"
                        } else {
                            "!!map"
                        }
                    ));
                }
            }
            let m = tfstackplan_render::yaml::load_string_map(src)
                .map_err(|e| format!("yaml: unmarshal errors:\n  {e}"))?
                .unwrap_or_default();
            Ok(Some(m.into_iter().map(|(k, v)| (k, from_go(v))).collect()))
        }
        Node::Seq(_, line) => Err(format!(
            "yaml: unmarshal errors:\n  line {line}: cannot unmarshal !!seq into map[string]interface {{}}"
        )),
        Node::Scalar { value, line, .. } => {
            let v = tfstackplan_render::yaml::load(src)
                .map(from_go)
                .map_err(|e| format!("yaml: {e}"))?;
            if v == Value::Nil {
                return Ok(None);
            }
            Err(format!(
                "yaml: unmarshal errors:\n  line {line}: cannot unmarshal {} `{value}` into map[string]interface {{}}",
                short_tag(&v)
            ))
        }
    }
}

#[cfg(test)]
mod tests;
