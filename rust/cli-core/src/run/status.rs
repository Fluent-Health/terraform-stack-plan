//! `run status`: decoding `GET /api/execution/<id>` the way Go decodes it into
//! `cliExecution` (case-insensitive field names, unknown fields ignored, missing
//! ones zero) and printing it as text or re-encoded JSON.

use serde_json::{Map, Value};

use crate::gojson::{self, Value as J};

fn field<'a>(obj: &'a Map<String, Value>, name: &str) -> Option<&'a Value> {
    obj.get(name).or_else(|| {
        obj.iter()
            .find(|(k, _)| k.eq_ignore_ascii_case(name))
            .map(|(_, v)| v)
    })
}

fn s(obj: &Map<String, Value>, name: &str) -> Result<String, String> {
    match field(obj, name) {
        None | Some(Value::Null) => Ok(String::new()),
        Some(Value::String(v)) => Ok(v.clone()),
        Some(other) => Err(format!(
            "json: cannot unmarshal {} into Go struct field .{name} of type string",
            kind(other)
        )),
    }
}

fn int(obj: &Map<String, Value>, name: &str) -> Result<i64, String> {
    match field(obj, name) {
        None | Some(Value::Null) => Ok(0),
        Some(Value::Number(n)) => n.as_i64().ok_or_else(|| {
            format!("json: cannot unmarshal number {n} into Go struct field .{name} of type int")
        }),
        Some(other) => Err(format!(
            "json: cannot unmarshal {} into Go struct field .{name} of type int",
            kind(other)
        )),
    }
}

fn kind(v: &Value) -> &'static str {
    match v {
        Value::Null => "null",
        Value::Bool(_) => "bool",
        Value::Number(_) => "number",
        Value::String(_) => "string",
        Value::Array(_) => "array",
        Value::Object(_) => "object",
    }
}

fn obj(v: Option<&Value>) -> Option<&Map<String, Value>> {
    v.and_then(Value::as_object)
}

fn arr(v: Option<&Value>) -> Option<&Vec<Value>> {
    v.and_then(Value::as_array)
}

const STATUSES: &[&str] = &[
    "",
    "pending",
    "initializing",
    "initialized",
    "running",
    "planned",
    "gated",
    "safe",
    "nochange",
    "moving",
    "failed",
    "aborted",
];

/// A category: name, icon and its attributes (sorted by key).
type CategoryView = (String, String, Vec<(String, Vec<String>)>);

/// One stack of the execution graph.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Stack {
    path: String,
    project: String,
    status: String,
    detail: String,
    categories: Vec<CategoryView>,
    counts: Option<[i64; 8]>,
}

/// A gate target as the server stores it.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Gate {
    class: String,
    target: String,
    grant_name: String,
    state: String,
    requester: String,
}

/// The decoded execution.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Execution {
    strings: Vec<(&'static str, String)>,
    pr: i64,
    rev: i64,
    check_run: (i64, bool),
    created_at: String,
    stacks: Option<Vec<Stack>>,
    edges: Option<Vec<(String, String)>>,
    gates: Option<Vec<Gate>>,
}

const COUNT_KEYS: [&str; 8] = [
    "add",
    "change",
    "destroy",
    "replace",
    "move",
    "import",
    "forget",
    "sensitivity_only",
];

const STRING_FIELDS: [&str; 11] = [
    "ID",
    "Repo",
    "SHA",
    "Environment",
    "ReportMarkdown",
    "LogURL",
    "Status",
    "StatusContext",
    "Phase",
    "SupersededBy",
    "",
];

impl Execution {
    fn get(&self, k: &str) -> &str {
        self.strings
            .iter()
            .find(|(n, _)| *n == k)
            .map(|(_, v)| v.as_str())
            .unwrap_or_default()
    }

    /// The execution-level status (`success`, `failure`, `in_progress`, …).
    pub fn status(&self) -> &str {
        self.get("Status")
    }

    /// Whether the execution has concluded.
    pub fn terminal(&self) -> bool {
        matches!(self.status(), "success" | "failure")
    }

    /// The exit code `run status` returns: 1 for `failure`, else 0.
    pub fn exit_code(&self) -> i32 {
        i32::from(self.status() == "failure")
    }
}

/// Go's `time.Time` RFC 3339 decode and RFC3339Nano re-encode.
fn time_field(raw: &str) -> Result<String, String> {
    let re = crate::goregex::compile(
        r"^([0-9]{4}-[0-9]{2}-[0-9]{2})[Tt]([0-9]{2}:[0-9]{2}:[0-9]{2})(\.[0-9]+)?([Zz]|[+-][0-9]{2}:[0-9]{2})$",
    )?;
    let m = re.captures(raw).ok_or_else(|| {
        format!(
            "parsing time {} as \"2006-01-02T15:04:05Z07:00\": cannot parse",
            crate::gofmt::quote(raw)
        )
    })?;
    let frac = m
        .get(3)
        .map(|f| f.as_str().trim_end_matches('0'))
        .filter(|f| *f != ".")
        .unwrap_or_default();
    let zone = match &m[4] {
        "z" | "Z" | "+00:00" | "-00:00" => "Z".to_string(),
        z => z.to_string(),
    };
    Ok(format!("{}T{}{frac}{zone}", &m[1], &m[2]))
}

/// Decodes an execution body.
pub fn decode(body: &[u8]) -> Result<Execution, String> {
    let v: Value = serde_json::from_slice(body).map_err(|e| e.to_string())?;
    let o = v.as_object().cloned().unwrap_or_default();
    let mut e = Execution::default();
    for f in STRING_FIELDS.iter().filter(|f| !f.is_empty()) {
        e.strings.push((f, s(&o, f)?));
    }
    e.pr = int(&o, "PR")?;
    e.rev = int(&o, "Rev")?;
    if let Some(c) = obj(field(&o, "CheckRunID")) {
        e.check_run = (
            int(c, "Int64")?,
            field(c, "Valid").and_then(Value::as_bool).unwrap_or(false),
        );
    }
    e.created_at = match field(&o, "CreatedAt") {
        Some(Value::String(t)) => time_field(t)?,
        _ => "0001-01-01T00:00:00Z".to_string(),
    };
    if let Some(g) = obj(field(&o, "graph")) {
        e.stacks = match arr(field(g, "stacks")) {
            None => None,
            Some(items) => Some(
                items
                    .iter()
                    .map(|st| decode_stack(st.as_object().cloned().unwrap_or_default()))
                    .collect::<Result<_, _>>()?,
            ),
        };
        e.edges = arr(field(g, "edges")).map(|items| {
            items
                .iter()
                .map(|ed| {
                    let ed = ed.as_object().cloned().unwrap_or_default();
                    (
                        s(&ed, "from").unwrap_or_default(),
                        s(&ed, "to").unwrap_or_default(),
                    )
                })
                .collect()
        });
    }
    e.gates = arr(field(&o, "gates")).map(|items| {
        items
            .iter()
            .map(|g| {
                let g = g.as_object().cloned().unwrap_or_default();
                Gate {
                    class: s(&g, "Class").unwrap_or_default(),
                    target: s(&g, "Target").unwrap_or_default(),
                    grant_name: s(&g, "GrantName").unwrap_or_default(),
                    state: s(&g, "State").unwrap_or_default(),
                    requester: s(&g, "Requester").unwrap_or_default(),
                }
            })
            .collect()
    });
    Ok(e)
}

fn decode_stack(o: Map<String, Value>) -> Result<Stack, String> {
    let status = s(&o, "status")?;
    if !STATUSES.contains(&status.as_str()) {
        return Err(format!(
            "[STACK-001] unknown stack status {}",
            crate::gofmt::quote(&status)
        ));
    }
    let categories = arr(field(&o, "categories"))
        .map(|cs| {
            cs.iter()
                .map(|c| {
                    let c = c.as_object().cloned().unwrap_or_default();
                    let mut attrs: Vec<(String, Vec<String>)> = obj(field(&c, "attributes"))
                        .map(|a| {
                            a.iter()
                                .map(|(k, v)| {
                                    let vals = v
                                        .as_array()
                                        .map(|x| {
                                            x.iter()
                                                .filter_map(|y| y.as_str().map(str::to_owned))
                                                .collect()
                                        })
                                        .unwrap_or_default();
                                    (k.clone(), vals)
                                })
                                .collect()
                        })
                        .unwrap_or_default();
                    attrs.sort();
                    (
                        s(&c, "name").unwrap_or_default(),
                        s(&c, "icon").unwrap_or_default(),
                        attrs,
                    )
                })
                .collect()
        })
        .unwrap_or_default();
    let counts = match obj(field(&o, "counts")) {
        None => None,
        Some(c) => {
            let mut out = [0i64; 8];
            for (i, k) in COUNT_KEYS.iter().enumerate() {
                out[i] = int(c, k)?;
            }
            Some(out)
        }
    };
    Ok(Stack {
        path: s(&o, "path")?,
        project: s(&o, "project")?,
        status,
        detail: s(&o, "detail")?,
        categories,
        counts,
    })
}

/// The text view (`printExecution` with `format == "text"`).
pub fn text(e: &Execution) -> String {
    let mut out = format!(
        "Execution ID: {}\nRepo/PR:      {} #{}\nStatus:       {}\nPhase:        {}\nEnvironment:  {}\n\n",
        e.get("ID"),
        e.get("Repo"),
        e.pr,
        e.status().to_uppercase(),
        e.get("Phase"),
        e.get("Environment"),
    );
    out.push_str("Stacks:\n");
    for st in e.stacks.iter().flatten() {
        let change = match st.counts {
            Some(c) => format!("+{}, -{}, ~{}", c[0], c[2], c[1]),
            None => "No Changes".to_string(),
        };
        out.push_str(&format!(
            "  {:<30}: {:<12} | {change}\n",
            st.path,
            st.status.to_uppercase()
        ));
    }
    if let Some(gates) = e.gates.as_ref().filter(|g| !g.is_empty()) {
        out.push_str("\nGates:\n");
        for g in gates {
            out.push_str(&format!("  {} ({}): {}\n", g.class, g.target, g.state));
        }
    }
    out
}

fn sv(v: &str) -> J {
    J::str(v)
}

/// The JSON view: Go's `json.MarshalIndent(cliExecution, "", "  ")` plus a newline.
pub fn json(e: &Execution) -> Result<String, String> {
    let mut fields: Vec<(String, J)> = Vec::new();
    for k in ["ID", "Repo", "SHA"] {
        fields.push((k.into(), sv(e.get(k))));
    }
    fields.push(("PR".into(), J::Int(e.pr)));
    fields.push(("Environment".into(), sv(e.get("Environment"))));
    fields.push((
        "CheckRunID".into(),
        J::Object(vec![
            ("Int64".into(), J::Int(e.check_run.0)),
            ("Valid".into(), J::Bool(e.check_run.1)),
        ]),
    ));
    fields.push(("Rev".into(), J::Int(e.rev)));
    for k in [
        "ReportMarkdown",
        "LogURL",
        "Status",
        "StatusContext",
        "Phase",
    ] {
        fields.push((k.into(), sv(e.get(k))));
    }
    fields.push(("CreatedAt".into(), sv(&e.created_at)));
    fields.push(("SupersededBy".into(), sv(e.get("SupersededBy"))));
    let stacks = match &e.stacks {
        None => J::Null,
        Some(sts) => J::Array(sts.iter().map(stack_json).collect()),
    };
    let edges = match &e.edges {
        None => J::Null,
        Some(eds) => J::Array(
            eds.iter()
                .map(|(f, t)| J::Object(vec![("from".into(), sv(f)), ("to".into(), sv(t))]))
                .collect(),
        ),
    };
    fields.push((
        "graph".into(),
        J::Object(vec![("stacks".into(), stacks), ("edges".into(), edges)]),
    ));
    let gates = match &e.gates {
        None => J::Null,
        Some(gs) => J::Array(
            gs.iter()
                .map(|g| {
                    J::Object(vec![
                        ("Class".into(), sv(&g.class)),
                        ("Target".into(), sv(&g.target)),
                        ("GrantName".into(), sv(&g.grant_name)),
                        ("State".into(), sv(&g.state)),
                        ("Requester".into(), sv(&g.requester)),
                    ])
                })
                .collect(),
        ),
    };
    fields.push(("gates".into(), gates));
    Ok(gojson::marshal_indent(&J::Object(fields), "  ")? + "\n")
}

fn stack_json(st: &Stack) -> J {
    let mut f = vec![("path".to_string(), sv(&st.path))];
    for (k, v) in [
        ("project", &st.project),
        ("status", &st.status),
        ("detail", &st.detail),
    ] {
        if !v.is_empty() {
            f.push((k.into(), sv(v)));
        }
    }
    if !st.categories.is_empty() {
        f.push((
            "categories".into(),
            J::Array(
                st.categories
                    .iter()
                    .map(|(name, icon, attrs)| {
                        let mut c = vec![("name".to_string(), sv(name))];
                        if !icon.is_empty() {
                            c.push(("icon".into(), sv(icon)));
                        }
                        if !attrs.is_empty() {
                            c.push((
                                "attributes".into(),
                                J::Object(
                                    attrs
                                        .iter()
                                        .map(|(k, v)| (k.clone(), J::strings(v)))
                                        .collect(),
                                ),
                            ));
                        }
                        J::Object(c)
                    })
                    .collect(),
            ),
        ));
    }
    if let Some(c) = st.counts {
        let counts: Vec<(String, J)> = COUNT_KEYS
            .iter()
            .zip(c)
            .filter(|(_, n)| *n != 0)
            .map(|(k, n)| (k.to_string(), J::Int(n)))
            .collect();
        f.push(("counts".into(), J::Object(counts)));
    }
    J::Object(f)
}

#[cfg(test)]
mod tests;
