//! A small `gohcl`-style decoder over `hcl-rs`: parse a body, evaluate an
//! attribute with no variables in scope, and reject arguments and blocks a
//! schema does not name.
//!
//! Go's HCL diagnostics carry a file position (`file.hcl:3,5-9: …`); these carry
//! the same summary and detail without it, so a malformed file is rejected in the
//! same cases with a shorter message.

use hcl::eval::{Context, Evaluate};
use hcl::{Block, Body, Expression, Value};

/// Parses HCL source. The error is `hcl-rs`'s message.
pub fn parse(src: &str) -> Result<Body, String> {
    hcl::parse(src).map_err(|e| e.to_string())
}

/// Evaluates an expression with no variables or functions in scope (Go's
/// `expr.Value(nil)`).
pub fn eval(expr: &Expression) -> Result<Value, String> {
    expr.evaluate(&Context::new()).map_err(|e| e.to_string())
}

/// A block's labels as strings.
pub fn labels(block: &Block) -> Vec<String> {
    block
        .labels
        .iter()
        .map(|l| l.as_str().to_string())
        .collect()
}

/// Converts a value to a string the way cty converts primitives to `string`.
pub fn value_to_string(v: &Value) -> Option<String> {
    match v {
        Value::String(s) => Some(s.clone()),
        Value::Number(n) => Some(n.to_string()),
        Value::Bool(b) => Some(b.to_string()),
        _ => None,
    }
}

/// A map of string to list of strings, as sorted `(key, list)` pairs.
pub type StringsMap = Vec<(String, Vec<String>)>;

/// Decodes one body against an explicit schema, `gohcl.DecodeBody` style.
pub struct Decoder<'a> {
    body: &'a Body,
    known_attrs: Vec<&'static str>,
    known_blocks: Vec<&'static str>,
}

fn type_err(name: &str, want: &str) -> String {
    format!(
        "Incorrect attribute value type; Inappropriate value for attribute {name:?}: {want} required."
    )
}

impl<'a> Decoder<'a> {
    /// Starts decoding `body`.
    pub fn new(body: &'a Body) -> Self {
        Self {
            body,
            known_attrs: Vec::new(),
            known_blocks: Vec::new(),
        }
    }

    fn attr(&mut self, name: &'static str) -> Option<&'a Expression> {
        self.known_attrs.push(name);
        self.body
            .attributes()
            .find(|a| a.key.as_str() == name)
            .map(|a| &a.expr)
    }

    fn required<T>(name: &str, v: Option<T>) -> Result<T, String> {
        v.ok_or_else(|| {
            format!(
                "Missing required argument; The argument {name:?} is required, but no definition was found."
            )
        })
    }

    /// An optional string attribute.
    pub fn opt_str(&mut self, name: &'static str) -> Result<Option<String>, String> {
        let Some(expr) = self.attr(name) else {
            return Ok(None);
        };
        let v = eval(expr)?;
        if v.is_null() {
            return Ok(None);
        }
        value_to_string(&v)
            .map(Some)
            .ok_or_else(|| type_err(name, "string"))
    }

    /// A required string attribute.
    pub fn req_str(&mut self, name: &'static str) -> Result<String, String> {
        let v = self.opt_str(name)?;
        Self::required(name, v)
    }

    /// An optional bool attribute.
    pub fn opt_bool(&mut self, name: &'static str) -> Result<Option<bool>, String> {
        let Some(expr) = self.attr(name) else {
            return Ok(None);
        };
        match eval(expr)? {
            Value::Null => Ok(None),
            Value::Bool(b) => Ok(Some(b)),
            Value::String(s) if s == "true" || s == "false" => Ok(Some(s == "true")),
            _ => Err(type_err(name, "bool")),
        }
    }

    /// An optional number attribute.
    pub fn opt_f64(&mut self, name: &'static str) -> Result<Option<f64>, String> {
        let Some(expr) = self.attr(name) else {
            return Ok(None);
        };
        match eval(expr)? {
            Value::Null => Ok(None),
            Value::Number(n) => n.as_f64().map(Some).ok_or_else(|| type_err(name, "number")),
            Value::String(s) => s.parse().map(Some).or(Err(type_err(name, "number"))),
            _ => Err(type_err(name, "number")),
        }
    }

    /// An optional list of strings.
    pub fn opt_strings(&mut self, name: &'static str) -> Result<Option<Vec<String>>, String> {
        let Some(expr) = self.attr(name) else {
            return Ok(None);
        };
        match eval(expr)? {
            Value::Null => Ok(None),
            Value::Array(items) => items
                .iter()
                .map(|v| value_to_string(v).ok_or_else(|| type_err(name, "list of string")))
                .collect::<Result<Vec<_>, _>>()
                .map(Some),
            _ => Err(type_err(name, "list of string")),
        }
    }

    /// A required list of strings.
    pub fn req_strings(&mut self, name: &'static str) -> Result<Vec<String>, String> {
        let v = self.opt_strings(name)?;
        Self::required(name, v)
    }

    /// An optional map of string to string, sorted by key.
    pub fn opt_string_map(
        &mut self,
        name: &'static str,
    ) -> Result<Option<Vec<(String, String)>>, String> {
        let Some(expr) = self.attr(name) else {
            return Ok(None);
        };
        match eval(expr)? {
            Value::Null => Ok(None),
            Value::Object(m) => {
                let mut out = m
                    .iter()
                    .map(|(k, v)| {
                        value_to_string(v)
                            .map(|s| (k.clone(), s))
                            .ok_or_else(|| type_err(name, "map of string"))
                    })
                    .collect::<Result<Vec<_>, _>>()?;
                out.sort();
                Ok(Some(out))
            }
            _ => Err(type_err(name, "map of string")),
        }
    }

    /// An optional map of string to list of strings, sorted by key.
    pub fn opt_strings_map(&mut self, name: &'static str) -> Result<Option<StringsMap>, String> {
        let Some(expr) = self.attr(name) else {
            return Ok(None);
        };
        let want = "map of list of string";
        match eval(expr)? {
            Value::Null => Ok(None),
            Value::Object(m) => {
                let mut out = Vec::new();
                for (k, v) in m.iter() {
                    let Value::Array(items) = v else {
                        return Err(type_err(name, want));
                    };
                    let list = items
                        .iter()
                        .map(|i| value_to_string(i).ok_or_else(|| type_err(name, want)))
                        .collect::<Result<Vec<_>, _>>()?;
                    out.push((k.clone(), list));
                }
                out.sort();
                Ok(Some(out))
            }
            _ => Err(type_err(name, want)),
        }
    }

    /// Every block of this type, in order.
    pub fn blocks(&mut self, name: &'static str) -> Vec<&'a Block> {
        self.known_blocks.push(name);
        self.body
            .blocks()
            .filter(|b| b.identifier.as_str() == name)
            .collect()
    }

    /// Fails on any argument or block the schema did not ask for.
    pub fn finish(self) -> Result<(), String> {
        for a in self.body.attributes() {
            if !self.known_attrs.contains(&a.key.as_str()) {
                return Err(format!(
                    "Unsupported argument; An argument named {:?} is not expected here.",
                    a.key.as_str()
                ));
            }
        }
        for b in self.body.blocks() {
            if !self.known_blocks.contains(&b.identifier.as_str()) {
                return Err(format!(
                    "Unsupported block type; Blocks of type {:?} are not expected here.",
                    b.identifier.as_str()
                ));
            }
        }
        Ok(())
    }
}
