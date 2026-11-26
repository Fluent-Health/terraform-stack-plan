//! How `tfstackplan uniqueness` prints a [`Report`]: text, or Go's JSON.

use super::{AllowRule, Report, Violation};
use crate::gojson::{self, Value as J};

fn envs(e: &[String]) -> String {
    format!("[{}]", e.join(" "))
}

/// The text report.
pub fn render_text(rep: &Report) -> String {
    let mut out = String::new();
    if !rep.report_only.is_empty() {
        out.push_str("Report-only findings (not blocking):\n");
        for v in &rep.report_only {
            out.push_str(&format!(
                "  {}: {} duplicated across {} ({})\n",
                v.unit,
                v.key,
                envs(&v.envs),
                v.kind.as_str()
            ));
        }
    }
    if !rep.unjustified.is_empty() {
        out.push_str("Unjustified violations:\n");
        for v in &rep.unjustified {
            out.push_str(&format!(
                "  {}: {} across {} ({})\n",
                v.unit,
                v.key,
                envs(&v.envs),
                v.kind.as_str()
            ));
        }
    }
    if !rep.stale.is_empty() {
        out.push_str("Stale allow rules (no longer match anything):\n");
        for a in &rep.stale {
            out.push_str(&format!("  {}: {}\n", a.unit, a.key));
        }
    }
    if rep.unjustified.is_empty() && rep.stale.is_empty() {
        out.push_str("tfstackplan uniqueness: ok — no unjustified violations or stale allows\n");
    }
    out
}

fn violation(v: &Violation) -> Result<J, String> {
    Ok(J::Object(vec![
        ("Unit".into(), J::str(&v.unit)),
        ("Key".into(), J::str(&v.key)),
        ("Value".into(), v.value.to_json()?),
        ("Envs".into(), J::strings(&v.envs)),
        ("Kind".into(), J::str(v.kind.as_str())),
        ("Severity".into(), J::str(v.severity.as_str())),
    ]))
}

fn allow(a: &AllowRule) -> J {
    J::Object(vec![
        ("Unit".into(), J::str(&a.unit)),
        ("Key".into(), J::str(&a.key)),
        ("Envs".into(), J::strings(&a.envs)),
        ("Reason".into(), J::str(&a.reason)),
        ("Expires".into(), J::str(&a.expires)),
    ])
}

fn violations(vs: &[Violation]) -> Result<J, String> {
    if vs.is_empty() {
        return Ok(J::Null);
    }
    Ok(J::Array(
        vs.iter().map(violation).collect::<Result<_, _>>()?,
    ))
}

/// `json.NewEncoder(os.Stdout)` with `SetIndent("", "  ")`: the report plus a
/// trailing newline. An error means nothing is written (Go's encoder buffers).
pub fn render_json(rep: &Report) -> Result<String, String> {
    let stale = if rep.stale.is_empty() {
        J::Null
    } else {
        J::Array(rep.stale.iter().map(allow).collect())
    };
    let doc = J::Object(vec![
        ("Unjustified".into(), violations(&rep.unjustified)?),
        ("Stale".into(), stale),
        ("ReportOnly".into(), violations(&rep.report_only)?),
    ]);
    let mut s = gojson::marshal_indent(&doc, "  ")?;
    s.push('\n');
    Ok(s)
}

#[cfg(test)]
mod tests;
