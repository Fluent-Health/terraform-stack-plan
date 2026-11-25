//! Parses a `terraform show -json` plan into a [`RawStack`]: reduced action counts plus the
//! affected attributes of each change (`internal/plan`, including `moveset_apply.go`).

use std::collections::{BTreeMap, BTreeSet};
use std::sync::OnceLock;

use crate::gojson;
use crate::goval::Value;
use crate::model::{Action, Counts};
use crate::moveset::Set;

/// One changed attribute, before rendering.
#[derive(Debug, Clone, PartialEq)]
pub struct RawAttr {
    /// Attribute name.
    pub name: String,
    /// Value before.
    pub before: Value,
    /// Value after.
    pub after: Value,
    /// The whole attribute is sensitive (its marker is a bare `true`).
    pub sensitive: bool,
    /// Known after apply.
    pub unknown: bool,
    /// Terraform's sensitivity tree for the value before.
    pub before_sensitive: Value,
    /// Terraform's sensitivity tree for the value after.
    pub after_sensitive: Value,
    /// Only the sensitivity changed.
    pub sensitivity_only: bool,
}

impl RawAttr {
    fn named(name: &str) -> Self {
        RawAttr {
            name: name.to_string(),
            before: Value::Null,
            after: Value::Null,
            sensitive: false,
            unknown: false,
            before_sensitive: Value::Null,
            after_sensitive: Value::Null,
            sensitivity_only: false,
        }
    }
}

/// One resource change with its raw Terraform actions alongside the reduced bucket.
#[derive(Debug, Clone, PartialEq)]
pub struct RawChange {
    /// Address.
    pub address: String,
    /// Resource type.
    pub type_: String,
    /// Raw Terraform actions, e.g. `["delete", "create"]`.
    pub actions: Vec<String>,
    /// Reduced bucket.
    pub action: Action,
    /// Changed attributes.
    pub attrs: Vec<RawAttr>,
    /// Every changed attribute only changed sensitivity.
    pub sensitivity_only: bool,
    /// Resource name.
    pub name: String,
    /// Module address.
    pub module_address: String,
    /// Provider name.
    pub provider_name: String,
    /// Top-level scalar attributes (after over before), sensitive and unknown ones skipped.
    pub raw: BTreeMap<String, Value>,
    /// Moved.
    pub moved: bool,
    /// "in", "out", "in-update" or empty.
    pub move_direction: String,
    /// Old address when moved.
    pub previous_address: String,
    /// Imported.
    pub imported: bool,
    /// Import id.
    pub import_id: String,
}

/// A parsed plan for one stack, no-ops excluded.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct RawStack {
    /// Stack name.
    pub name: String,
    /// Tallies.
    pub counts: Counts,
    /// Changes.
    pub changes: Vec<RawChange>,
}

type Object = serde_json::Map<String, serde_json::Value>;

/// Reads a field the way `encoding/json` fills a Go struct field: absent or `null` leaves the
/// zero value, a wrong JSON type is an `UnmarshalTypeError`.
struct Fields<'a> {
    obj: &'a Object,
    strukt: &'static str,
    path: String,
}

impl<'a> Fields<'a> {
    fn type_error(&self, key: &str, v: &serde_json::Value, go_type: &str) -> String {
        format!(
            "json: cannot unmarshal {} into Go struct field {}.{}{key} of type {go_type}",
            gojson::type_name(v),
            self.strukt,
            self.path
        )
    }

    fn get(&self, key: &str) -> Option<&'a serde_json::Value> {
        self.obj.get(key).filter(|v| !v.is_null())
    }

    fn string(&self, key: &str) -> Result<String, String> {
        match self.get(key) {
            None => Ok(String::new()),
            Some(serde_json::Value::String(s)) => Ok(s.clone()),
            Some(v) => Err(self.type_error(key, v, "string")),
        }
    }

    fn object(&self, key: &str, strukt: &'static str) -> Result<Option<Fields<'a>>, String> {
        match self.get(key) {
            None => Ok(None),
            Some(serde_json::Value::Object(o)) => Ok(Some(Fields {
                obj: o,
                strukt,
                path: format!("{}{key}.", self.path),
            })),
            Some(v) => Err(self.type_error(key, v, &format!("tfjson.{strukt}"))),
        }
    }

    fn any(&self, key: &str) -> Value {
        self.get(key).cloned().map_or(Value::Null, Value::from_json)
    }
}

struct ParsedChange {
    actions: Vec<String>,
    before: Value,
    after: Value,
    after_unknown: Value,
    before_sensitive: Value,
    after_sensitive: Value,
    importing: Option<String>,
}

fn has(a: &[String], x: &[&str]) -> bool {
    a.len() == x.len() && a.iter().zip(x).all(|(l, r)| l == r)
}

/// Checks `format_version` against tfjson's `>= 0.1, < 2.0` (hashicorp/go-version rules).
fn validate_format_version(v: &str) -> Result<(), String> {
    if v.is_empty() {
        return Err("unexpected plan input, format version is missing".to_string());
    }
    static RE: OnceLock<regex::Regex> = OnceLock::new();
    let re = RE.get_or_init(|| {
        regex::Regex::new(concat!(
            r"^v?([0-9]+(\.[0-9]+)*?)",
            r"(-([0-9]+[0-9A-Za-z\-~]*(\.[0-9A-Za-z\-~]+)*)|(-?([A-Za-z\-~]+[0-9A-Za-z\-~]*(\.[0-9A-Za-z\-~]+)*)))?",
            r"(\+([0-9A-Za-z\-~]+(\.[0-9A-Za-z\-~]+)*))?",
            r"?$"
        ))
        .unwrap_or_else(|_| unreachable!())
    });
    let Some(m) = re.captures(v) else {
        return Err(format!("invalid format version {}: Malformed version: {v}", crate::gofmt::quote(v)));
    };
    let mut segments = Vec::new();
    for s in m.get(1).map_or("", |g| g.as_str()).split('.') {
        match s.parse::<i64>() {
            Ok(n) => segments.push(n),
            Err(e) => {
                return Err(format!(
                    "invalid format version {}: Error parsing version: strconv.ParseInt: parsing {}: {}",
                    crate::gofmt::quote(v),
                    crate::gofmt::quote(s),
                    if matches!(e.kind(), std::num::IntErrorKind::PosOverflow) {
                        "value out of range"
                    } else {
                        "invalid syntax"
                    }
                ));
            }
        }
    }
    while segments.len() < 3 {
        segments.push(0);
    }
    let pre = m.get(7).or_else(|| m.get(4)).map_or("", |g| g.as_str());
    let meta = m.get(10).map_or("", |g| g.as_str());
    let cmp = |other: [i64; 3]| -> std::cmp::Ordering {
        let mut it = segments.iter().copied().chain(std::iter::repeat(0));
        for o in other {
            let s = it.next().unwrap_or(0);
            if s != o {
                return s.cmp(&o);
            }
        }
        if segments.iter().skip(3).any(|&s| s != 0) {
            return std::cmp::Ordering::Greater;
        }
        std::cmp::Ordering::Equal
    };
    // A pre-release never satisfies a constraint without one.
    let ok = pre.is_empty() && cmp([0, 1, 0]).is_ge() && cmp([2, 0, 0]).is_lt();
    if ok {
        return Ok(());
    }
    let mut shown = segments.iter().map(i64::to_string).collect::<Vec<_>>().join(".");
    if !pre.is_empty() {
        shown.push('-');
        shown.push_str(pre);
    }
    if !meta.is_empty() {
        shown.push('+');
        shown.push_str(meta);
    }
    Err(format!(
        "unsupported plan format version: {} does not satisfy \">= 0.1, < 2.0\"",
        crate::gofmt::quote(&shown)
    ))
}

fn parse_change(rc: &Fields<'_>) -> Result<Option<ParsedChange>, String> {
    let Some(c) = rc.object("change", "Change")? else {
        return Ok(None);
    };
    let actions = match c.get("actions") {
        None => Vec::new(),
        Some(serde_json::Value::Array(a)) => {
            let mut out = Vec::new();
            for x in a {
                match x {
                    serde_json::Value::String(s) => out.push(s.clone()),
                    serde_json::Value::Null => out.push(String::new()),
                    other => return Err(c.type_error("actions", other, "tfjson.Action")),
                }
            }
            out
        }
        Some(v) => return Err(c.type_error("actions", v, "tfjson.Actions")),
    };
    let importing = match c.object("importing", "Importing")? {
        None => None,
        Some(i) => Some(i.string("id")?),
    };
    Ok(Some(ParsedChange {
        actions,
        before: c.any("before"),
        after: c.any("after"),
        after_unknown: c.any("after_unknown"),
        before_sensitive: c.any("before_sensitive"),
        after_sensitive: c.any("after_sensitive"),
        importing,
    }))
}

/// Parses a plan document. Errors read like Go's (`stack "x": parse plan json: ...`).
pub fn parse(name: &str, data: &[u8]) -> Result<RawStack, String> {
    let wrap = |e: String| format!("stack {}: parse plan json: {e}", crate::gofmt::quote(name));
    gojson::check_valid(data).map_err(wrap)?;
    let text = String::from_utf8_lossy(data);
    let doc: serde_json::Value = serde_json::from_str(&text).map_err(|e| wrap(e.to_string()))?;
    let serde_json::Value::Object(obj) = &doc else {
        return Err(wrap(format!(
            "json: cannot unmarshal {} into Go value of type tfjson.rawPlan",
            gojson::type_name(&doc)
        )));
    };
    let top = Fields {
        obj,
        strukt: "rawPlan",
        path: String::new(),
    };
    let format_version = top.string("format_version").map_err(wrap)?;
    let rcs: Vec<&Object> = match top.get("resource_changes") {
        None => Vec::new(),
        Some(serde_json::Value::Array(a)) => {
            let mut v = Vec::new();
            for x in a {
                match x {
                    serde_json::Value::Object(o) => v.push(o),
                    serde_json::Value::Null => {}
                    other => {
                        return Err(wrap(top.type_error("resource_changes", other, "tfjson.ResourceChange")));
                    }
                }
            }
            v
        }
        Some(other) => {
            return Err(wrap(top.type_error("resource_changes", other, "[]*tfjson.ResourceChange")));
        }
    };
    // Decode every change first: Go reports a type error before validating the version.
    let mut decoded = Vec::new();
    for o in rcs {
        let f = Fields {
            obj: o,
            strukt: "ResourceChange",
            path: "resource_changes.".to_string(),
        };
        let rc = (
            f.string("address").map_err(wrap)?,
            f.string("previous_address").map_err(wrap)?,
            f.string("module_address").map_err(wrap)?,
            f.string("mode").map_err(wrap)?,
            f.string("type").map_err(wrap)?,
            f.string("name").map_err(wrap)?,
            f.string("provider_name").map_err(wrap)?,
            parse_change(&f).map_err(wrap)?,
        );
        decoded.push(rc);
    }
    validate_format_version(&format_version).map_err(wrap)?;

    let mut rs = RawStack {
        name: name.to_string(),
        ..RawStack::default()
    };
    for (address, previous_address, module_address, mode, type_, rname, provider_name, change) in decoded {
        let Some(c) = change else { continue };
        if mode == "data" {
            continue;
        }
        let act = &c.actions;
        if has(act, &["read"]) {
            continue;
        }
        let moved = !previous_address.is_empty() && previous_address != address;
        let imported = c.importing.is_some();
        let noop = has(act, &["no-op"]);
        if noop && !moved && !imported {
            continue;
        }
        let bucket = if has(act, &["forget"]) {
            Action::Forget
        } else if noop {
            Action::Noop
        } else if has(act, &["delete", "create"]) || has(act, &["create", "delete"]) {
            Action::Replace
        } else if has(act, &["create"]) {
            Action::Add
        } else if has(act, &["delete"]) {
            Action::Destroy
        } else {
            Action::Change
        };
        let attrs = match bucket {
            Action::Change | Action::Replace => changed_attrs(&c),
            Action::Add => side_attrs(&c, true),
            Action::Destroy | Action::Forget => side_attrs(&c, false),
            Action::Noop => Vec::new(),
        };
        let sens_only = !attrs.is_empty() && attrs.iter().all(|a| a.sensitivity_only);
        match bucket {
            Action::Add => rs.counts.add += 1,
            Action::Change => {
                rs.counts.change += 1;
                if sens_only {
                    rs.counts.sensitivity_only += 1;
                }
            }
            Action::Destroy => rs.counts.destroy += 1,
            Action::Replace => rs.counts.replace += 1,
            Action::Forget => rs.counts.forget += 1,
            Action::Noop => {}
        }
        if moved {
            rs.counts.moved += 1;
        }
        if imported {
            rs.counts.import += 1;
        }
        let raw = raw_scalars(&c);
        rs.changes.push(RawChange {
            address,
            type_,
            actions: c.actions.clone(),
            action: bucket,
            attrs,
            sensitivity_only: sens_only,
            name: rname,
            module_address,
            provider_name,
            raw,
            moved,
            move_direction: String::new(),
            previous_address: if moved { previous_address } else { String::new() },
            imported,
            import_id: c.importing.clone().unwrap_or_default(),
        });
    }
    Ok(rs)
}

fn as_map(v: &Value) -> Option<&BTreeMap<String, Value>> {
    v.as_map()
}

fn lookup<'a>(m: Option<&'a BTreeMap<String, Value>>, k: &str) -> &'a Value {
    static NULL: Value = Value::Null;
    m.and_then(|m| m.get(k)).unwrap_or(&NULL)
}

/// A sensitive or unknown marker is set: `true` or a nested object (anything but nil/false).
fn truthy(v: &Value) -> bool {
    match v {
        Value::Null => false,
        Value::Bool(b) => *b,
        _ => true,
    }
}

/// The marker means the whole attribute is sensitive: a bare `true`.
fn is_true(v: &Value) -> bool {
    matches!(v, Value::Bool(true))
}

/// Two sensitivity markings are equivalent, treating nil and false as equal.
fn sens_equal(a: &Value, b: &Value) -> bool {
    let off = |v: &Value| matches!(v, Value::Null | Value::Bool(false));
    (off(a) && off(b)) || a == b
}

fn changed_attrs(c: &ParsedChange) -> Vec<RawAttr> {
    let before = as_map(&c.before);
    let after = as_map(&c.after);
    let unknown = as_map(&c.after_unknown);
    let before_sens = as_map(&c.before_sensitive);
    let after_sens = as_map(&c.after_sensitive);

    let mut keys = BTreeSet::new();
    for m in [before, after, unknown].into_iter().flatten() {
        keys.extend(m.keys());
    }
    let mut attrs = Vec::new();
    for k in keys {
        let (b, a) = (lookup(before, k), lookup(after, k));
        let is_unknown = truthy(lookup(unknown, k));
        let (bs, as_) = (lookup(before_sens, k), lookup(after_sens, k));
        let sens_changed = !sens_equal(bs, as_);
        if !is_unknown && b == a && !sens_changed {
            continue;
        }
        attrs.push(RawAttr {
            name: k.clone(),
            before: b.clone(),
            after: a.clone(),
            sensitive: is_true(bs) || is_true(as_),
            unknown: is_unknown,
            before_sensitive: bs.clone(),
            after_sensitive: as_.clone(),
            sensitivity_only: !is_unknown && b == a && sens_changed,
        });
    }
    attrs
}

fn side_attrs(c: &ParsedChange, after: bool) -> Vec<RawAttr> {
    let (src, sens, unknown) = if after {
        (as_map(&c.after), as_map(&c.after_sensitive), as_map(&c.after_unknown))
    } else {
        (as_map(&c.before), as_map(&c.before_sensitive), None)
    };
    let mut keys = BTreeSet::new();
    for m in [src, unknown].into_iter().flatten() {
        keys.extend(m.keys());
    }
    keys.into_iter()
        .map(|k| {
            let mut ra = RawAttr::named(k);
            ra.sensitive = is_true(lookup(sens, k));
            ra.unknown = truthy(lookup(unknown, k));
            if after {
                ra.after = lookup(src, k).clone();
                ra.after_sensitive = lookup(sens, k).clone();
            } else {
                ra.before = lookup(src, k).clone();
                ra.before_sensitive = lookup(sens, k).clone();
            }
            ra
        })
        .collect()
}

fn raw_scalars(c: &ParsedChange) -> BTreeMap<String, Value> {
    let after = as_map(&c.after);
    let before = as_map(&c.before);
    let after_sens = as_map(&c.after_sensitive);
    let before_sens = as_map(&c.before_sensitive);
    let unknown = as_map(&c.after_unknown);
    let mut out = BTreeMap::new();
    for src in [after, before].into_iter().flatten() {
        for (k, v) in src {
            if out.contains_key(k) {
                continue;
            }
            if truthy(lookup(after_sens, k)) || truthy(lookup(before_sens, k)) || truthy(lookup(unknown, k)) {
                continue;
            }
            if matches!(v, Value::Str(_) | Value::Bool(_) | Value::Float(_)) {
                out.insert(k.clone(), v.clone());
            }
        }
    }
    derive_project(&mut out);
    out
}

/// Fills `project` from the first scalar (in key order) shaped `projects/<project>/...`, when
/// `project` itself is absent.
fn derive_project(out: &mut BTreeMap<String, Value>) {
    if out.contains_key("project") {
        return;
    }
    static RE: OnceLock<regex::Regex> = OnceLock::new();
    let re = RE.get_or_init(|| regex::Regex::new(r"^projects/([^/]+)/").unwrap_or_else(|_| unreachable!()));
    let found = out
        .values()
        .filter_map(Value::as_str)
        .find_map(|s| re.captures(s).and_then(|m| m.get(1)).map(|m| m.as_str().to_string()));
    if let Some(p) = found {
        out.insert("project".to_string(), Value::Str(p));
    }
}

impl RawStack {
    /// Reclassifies every change whose address is a pending cross-state move target as a
    /// move, and returns how many it reclassified.
    pub fn apply_state_moves(&mut self, targets: &Set) -> usize {
        if targets.is_empty() {
            return 0;
        }
        let mut n = 0;
        for c in &mut self.changes {
            if c.action == Action::Noop
                || c.imported
                || !(targets.covers(&c.address)
                    || (!c.previous_address.is_empty() && targets.covers(&c.previous_address)))
            {
                continue;
            }
            match c.action {
                Action::Add => {
                    self.counts.add -= 1;
                    c.move_direction = "in".to_string();
                    c.attrs.clear();
                }
                Action::Change => {
                    self.counts.change -= 1;
                    c.move_direction = "in-update".to_string();
                }
                Action::Destroy => {
                    self.counts.destroy -= 1;
                    c.move_direction = "out".to_string();
                    c.attrs.clear();
                }
                Action::Replace => {
                    self.counts.replace -= 1;
                    c.move_direction = "in-update".to_string();
                }
                Action::Forget | Action::Noop => {}
            }
            c.action = Action::Noop;
            c.moved = true;
            self.counts.moved += 1;
            n += 1;
        }
        n
    }
}

#[cfg(test)]
mod tests;
