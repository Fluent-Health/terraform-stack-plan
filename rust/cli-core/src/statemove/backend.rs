//! The `terraform { backend "gcs" { … } }` block a state lock needs.

use crate::hclx;

fn literal_string(body: &hcl::Body, name: &str) -> String {
    body.attributes()
        .find(|a| a.key.as_str() == name)
        .and_then(|a| hclx::eval(&a.expr).ok())
        .and_then(|v| match v {
            hcl::Value::String(s) => Some(s),
            _ => None,
        })
        .unwrap_or_default()
}

/// Go's `gcsBackend` over a stack's `*.tf` files (name order): the first
/// `backend "gcs"` block with a literal `bucket`, as `(bucket, prefix)`. Files that
/// do not parse are skipped.
pub fn gcs_backend<'a>(tf_files: impl IntoIterator<Item = &'a str>) -> Option<(String, String)> {
    for src in tf_files {
        let Ok(body) = hclx::parse(src) else { continue };
        for blk in body
            .blocks()
            .filter(|b| b.identifier.as_str() == "terraform")
        {
            for inner in blk.body.blocks() {
                let labels = hclx::labels(inner);
                if inner.identifier.as_str() != "backend" || labels.len() != 1 || labels[0] != "gcs"
                {
                    continue;
                }
                let bucket = literal_string(&inner.body, "bucket");
                let prefix = literal_string(&inner.body, "prefix");
                if !bucket.is_empty() {
                    return Some((bucket, prefix));
                }
            }
        }
    }
    None
}

#[cfg(test)]
mod tests;
