//! The Go types the handlers decode into, as data, so a request body can be checked
//! and normalised the way Go's decoder treats it before serde reads it.

use serde_json::{Map, Number, Value};
use tfstackplan_gate::events::Status;

/// A Go type: its name as `reflect.Type.String()` prints it, and its kind.
#[derive(Debug)]
pub struct Shape {
    pub go: &'static str,
    pub kind: Kind,
}

/// What a Go type accepts.
#[derive(Debug)]
pub enum Kind {
    Str,
    Int,
    Bool,
    /// `events.Status`, whose `UnmarshalJSON` rejects unknown values.
    Status,
    Struct(&'static str, &'static [(&'static str, &'static Shape)]),
    Slice(&'static Shape),
    /// `map[string]T`.
    Map(&'static Shape),
    /// `map[string]string` etc. carried verbatim (`json.RawMessage`, `any`).
    Any,
}

/// Rewrites `v` into the canonical JSON serde expects for `shape`: object keys
/// matched case-insensitively to the declared field names, unknown keys and nulls
/// dropped. `Err` is Go's decode error: the first type mismatch, or an
/// `UnmarshalJSON` failure.
pub fn canonicalize(v: &Value, shape: &Shape) -> Result<Value, String> {
    let mut first = None;
    let out = walk(v, shape, None, &mut Vec::new(), &mut first)?;
    match first {
        Some(e) => Err(e),
        None => Ok(out.unwrap_or(Value::Null)),
    }
}

fn json_kind(v: &Value) -> String {
    match v {
        Value::Null => "null".into(),
        Value::Bool(_) => "bool".into(),
        Value::Number(_) => "number".into(),
        Value::String(_) => "string".into(),
        Value::Array(_) => "array".into(),
        Value::Object(_) => "object".into(),
    }
}

fn type_error(value: String, shape: &Shape, ctx: Option<&str>, fields: &[String]) -> String {
    match ctx {
        Some(st) if !fields.is_empty() => format!(
            "json: cannot unmarshal {value} into Go struct field {st}.{} of type {}",
            fields.join("."),
            shape.go
        ),
        _ => format!(
            "json: cannot unmarshal {value} into Go value of type {}",
            shape.go
        ),
    }
}

fn zero(shape: &Shape) -> Option<Value> {
    Some(match shape.kind {
        Kind::Str | Kind::Status => Value::String(String::new()),
        Kind::Int => Value::Number(Number::from(0)),
        Kind::Bool => Value::Bool(false),
        Kind::Struct(..) | Kind::Map(_) => Value::Object(Map::new()),
        Kind::Slice(_) => Value::Array(Vec::new()),
        Kind::Any => return None,
    })
}

/// `None` means "leave the field at its zero value" (Go skips nulls and mismatches).
fn walk(
    v: &Value,
    shape: &Shape,
    ctx: Option<&'static str>,
    fields: &mut Vec<String>,
    first: &mut Option<String>,
) -> Result<Option<Value>, String> {
    let mismatch = |value: String, first: &mut Option<String>| {
        if first.is_none() {
            *first = Some(type_error(value, shape, ctx, fields));
        }
        Ok(None)
    };
    if v.is_null() && !matches!(shape.kind, Kind::Status | Kind::Any) {
        return Ok(None);
    }
    match &shape.kind {
        Kind::Any => Ok(Some(v.clone())),
        Kind::Str => match v {
            Value::String(_) => Ok(Some(v.clone())),
            other => mismatch(json_kind(other), first),
        },
        Kind::Bool => match v {
            Value::Bool(_) => Ok(Some(v.clone())),
            other => mismatch(json_kind(other), first),
        },
        Kind::Int => match v {
            Value::Number(n) if n.is_i64() => Ok(Some(v.clone())),
            Value::Number(n) => mismatch(format!("number {n}"), first),
            other => mismatch(json_kind(other), first),
        },
        Kind::Status => match v {
            Value::Null => Ok(None),
            Value::String(s) => match Status::parse(s) {
                Ok(_) => Ok(Some(v.clone())),
                Err(e) => Err(e.to_string()),
            },
            other => Err(format!(
                "json: cannot unmarshal {} into Go value of type string",
                json_kind(other)
            )),
        },
        Kind::Slice(elem) => match v {
            Value::Array(items) => {
                let mut out = Vec::with_capacity(items.len());
                for item in items {
                    let got = walk(item, elem, ctx, fields, first)?;
                    out.push(got.or_else(|| zero(elem)).unwrap_or(Value::Null));
                }
                Ok(Some(Value::Array(out)))
            }
            other => mismatch(json_kind(other), first),
        },
        Kind::Map(elem) => match v {
            Value::Object(m) => {
                let mut out = Map::new();
                for (k, item) in m {
                    fields.push(k.clone());
                    let got = walk(item, elem, ctx, fields, first);
                    fields.pop();
                    if let Some(got) = got?.or_else(|| zero(elem)) {
                        out.insert(k.clone(), got);
                    }
                }
                Ok(Some(Value::Object(out)))
            }
            other => mismatch(json_kind(other), first),
        },
        Kind::Struct(name, decl) => match v {
            Value::Object(m) => {
                let mut out = Map::new();
                for (k, item) in m {
                    let field = decl
                        .iter()
                        .find(|(f, _)| *f == k)
                        .or_else(|| decl.iter().find(|(f, _)| f.eq_ignore_ascii_case(k)));
                    let Some((fname, fshape)) = field else {
                        continue;
                    };
                    fields.push((*fname).to_string());
                    let got = walk(item, fshape, Some(name), fields, first);
                    fields.pop();
                    match got? {
                        Some(got) => {
                            out.insert((*fname).to_string(), got);
                        }
                        None => {
                            out.remove(*fname);
                        }
                    }
                }
                Ok(Some(Value::Object(out)))
            }
            other => mismatch(json_kind(other), first),
        },
    }
}

macro_rules! shape {
    ($name:ident, $go:expr, $kind:expr) => {
        #[doc = concat!("Go's `", $go, "`.")]
        pub static $name: Shape = Shape {
            go: $go,
            kind: $kind,
        };
    };
}

shape!(STRING, "string", Kind::Str);
shape!(INT, "int", Kind::Int);
shape!(PTR_INT, "*int", Kind::Int);
shape!(BOOL, "bool", Kind::Bool);
shape!(STATUS, "events.Status", Kind::Status);
shape!(PHASE, "events.Phase", Kind::Str);
shape!(STRINGS, "[]string", Kind::Slice(&STRING));
shape!(STRING_MAP, "map[string]string", Kind::Map(&STRING));
shape!(ANY, "interface {}", Kind::Any);
shape!(STRING_LIST_MAP, "map[string][]string", Kind::Map(&STRINGS));
shape!(
    CATEGORY,
    "domain.Category",
    Kind::Struct(
        "Category",
        &[
            ("name", &STRING),
            ("icon", &STRING),
            ("attributes", &STRING_LIST_MAP)
        ]
    )
);
shape!(CATEGORIES, "[]domain.Category", Kind::Slice(&CATEGORY));
const COUNTS_FIELDS: &[(&str, &Shape)] = &[
    ("add", &INT),
    ("change", &INT),
    ("destroy", &INT),
    ("replace", &INT),
    ("move", &INT),
    ("import", &INT),
    ("forget", &INT),
    ("sensitivity_only", &INT),
];
shape!(
    COUNTS,
    "domain.Counts",
    Kind::Struct("Counts", COUNTS_FIELDS)
);
shape!(
    PTR_COUNTS,
    "*domain.Counts",
    Kind::Struct("Counts", COUNTS_FIELDS)
);
shape!(
    STACK_STATE,
    "events.StackState",
    Kind::Struct(
        "StackState",
        &[
            ("path", &STRING),
            ("project", &STRING),
            ("status", &STATUS),
            ("detail", &STRING),
            ("categories", &CATEGORIES),
            ("counts", &PTR_COUNTS),
        ]
    )
);
shape!(
    STACK_STATES,
    "[]events.StackState",
    Kind::Slice(&STACK_STATE)
);
shape!(
    EDGE,
    "events.Edge",
    Kind::Struct("Edge", &[("from", &STRING), ("to", &STRING)])
);
shape!(EDGES, "[]events.Edge", Kind::Slice(&EDGE));
shape!(
    INIT,
    "events.Init",
    Kind::Struct(
        "Init",
        &[
            ("id", &STRING),
            ("repo", &STRING),
            ("sha", &STRING),
            ("pr", &INT),
            ("environment", &STRING),
            ("log_url", &STRING),
            ("context", &STRING),
            ("stacks", &STACK_STATES),
            ("edges", &EDGES),
        ]
    )
);
shape!(
    PHASE_EVENT,
    "events.PhaseEvent",
    Kind::Struct(
        "PhaseEvent",
        &[
            ("id", &STRING),
            ("repo", &STRING),
            ("sha", &STRING),
            ("pr", &INT),
            ("environment", &STRING),
            ("log_url", &STRING),
            ("context", &STRING),
            ("phase", &PHASE),
            ("label", &STRING),
            ("progress_pct", &PTR_INT),
        ]
    )
);
shape!(
    LOG_CHUNK,
    "events.LogChunk",
    Kind::Struct(
        "LogChunk",
        &[("id", &STRING), ("stack", &STRING), ("data", &STRING)]
    )
);
shape!(
    UPDATE,
    "events.Update",
    Kind::Struct(
        "Update",
        &[
            ("id", &STRING),
            ("stack", &STRING),
            ("status", &STATUS),
            ("detail", &STRING)
        ]
    )
);
shape!(
    GATE_TARGET,
    "events.GateTarget",
    Kind::Struct("GateTarget", &[("class", &STRING), ("target", &STRING)])
);
shape!(
    GATE_TARGETS,
    "[]events.GateTarget",
    Kind::Slice(&GATE_TARGET)
);
shape!(
    CHANGE_REASON,
    "events.ChangeReason",
    Kind::Struct(
        "ChangeReason",
        &[("stack", &STRING), ("kind", &STRING), ("via", &STRINGS)]
    )
);
shape!(
    CHANGE_REASONS,
    "[]events.ChangeReason",
    Kind::Slice(&CHANGE_REASON)
);
shape!(
    CATEGORY_MAP,
    "map[string][]domain.Category",
    Kind::Map(&CATEGORIES)
);
shape!(COUNTS_MAP, "map[string]domain.Counts", Kind::Map(&COUNTS));
shape!(
    FINALIZE,
    "events.Finalize",
    Kind::Struct(
        "Finalize",
        &[
            ("id", &STRING),
            ("report_markdown", &STRING),
            ("projects", &STRING_MAP),
            ("stack_reports", &STRING_MAP),
            ("gates", &GATE_TARGETS),
            ("moving", &STRINGS),
            ("failed", &BOOL),
            ("categories", &CATEGORY_MAP),
            ("counts", &COUNTS_MAP),
            ("change_reasons", &CHANGE_REASONS),
        ]
    )
);
shape!(
    GATE_CHECK,
    "events.GateCheck",
    Kind::Struct("GateCheck", &[("pr", &INT), ("environment", &STRING)])
);
shape!(
    GATE_REVOKE,
    "events.GateRevoke",
    Kind::Struct("GateRevoke", &[("pr", &INT), ("environment", &STRING)])
);
shape!(
    CLAIMS_LIST,
    "struct { Environment string \"json:\\\"environment\\\"\" }",
    Kind::Struct("", &[("environment", &STRING)])
);
shape!(
    CLAIMS_RELEASE,
    "struct { Environment string \"json:\\\"environment\\\"\"; PR int \"json:\\\"pr\\\"\"; Stack string \"json:\\\"stack\\\"\" }",
    Kind::Struct(
        "",
        &[("environment", &STRING), ("pr", &INT), ("stack", &STRING)]
    )
);
shape!(
    ADMIN_GRANT,
    "api.AdminGrantsReleaseRequest",
    Kind::Struct(
        "AdminGrantsReleaseRequest",
        &[
            ("class", &STRING),
            ("environment", &STRING),
            ("pr", &INT),
            ("reason", &STRING),
            ("target", &STRING)
        ]
    )
);
shape!(
    ADMIN_SATISFY,
    "api.AdminGatesSatisfyRequest",
    Kind::Struct(
        "AdminGatesSatisfyRequest",
        &[
            ("class", &STRING),
            ("environment", &STRING),
            ("pr", &INT),
            ("reason", &STRING),
            ("target", &STRING)
        ]
    )
);
shape!(
    ADMIN_CANCEL,
    "api.AdminExecutionsCancelRequest",
    Kind::Struct(
        "AdminExecutionsCancelRequest",
        &[("id", &STRING), ("reason", &STRING)]
    )
);
shape!(
    ADMIN_OVERRIDE,
    "api.AdminChecksOverrideRequest",
    Kind::Struct(
        "AdminChecksOverrideRequest",
        &[
            ("check", &STRING),
            ("conclusion", &STRING),
            ("environment", &STRING),
            ("pr", &INT),
            ("reason", &STRING)
        ]
    )
);
shape!(
    PUSH_MESSAGE,
    "struct { Data string \"json:\\\"data\\\"\"; Attributes map[string]string \"json:\\\"attributes\\\"\" }",
    Kind::Struct("", &[("data", &STRING), ("attributes", &STRING_MAP)])
);
shape!(
    PUSH_ENVELOPE,
    "server.pushEnvelope",
    Kind::Struct(
        "pushEnvelope",
        &[("message", &PUSH_MESSAGE), ("subscription", &STRING)]
    )
);
