//! GitHub-flavoured markdown to HTML for the `/plan` fragment (Go: `mdrender.go`,
//! `mddiff.go`), with raw HTML passed through for the reports' `<details>` blocks
//! and ```` ```diff ```` fences coloured per line.
//!
//! Go renders with goldmark and this with comrak; both follow CommonMark and GFM,
//! so the HTML agrees for what the render core emits (headings, tables, details
//! blocks, fenced code), but the two are different parsers.

/// Renders `md` to HTML.
pub fn render(md: &str) -> String {
    let mut opts = comrak::Options::default();
    opts.extension.table = true;
    opts.extension.strikethrough = true;
    opts.extension.autolink = true;
    opts.extension.tasklist = true;
    opts.render.r#unsafe = true;
    colour_diffs(&comrak::markdown_to_html(md, &opts))
}

const DIFF_OPEN: &str = "<pre><code class=\"language-diff\">";
const CODE_CLOSE: &str = "</code></pre>";

/// Rewrites each diff fence as `<pre class="tfsp-diff"><code>` with one classed
/// span per +/-/~/! line (the newline outside the span).
fn colour_diffs(html: &str) -> String {
    let mut out = String::with_capacity(html.len());
    let mut rest = html;
    while let Some(start) = rest.find(DIFF_OPEN) {
        let body_start = start + DIFF_OPEN.len();
        let Some(len) = rest[body_start..].find(CODE_CLOSE) else {
            break;
        };
        out.push_str(&rest[..start]);
        out.push_str("<pre class=\"tfsp-diff\"><code>");
        for line in rest[body_start..body_start + len].split_inclusive('\n') {
            let (text, nl) = match line.strip_suffix('\n') {
                Some(t) => (t, "\n"),
                None => (line, ""),
            };
            let class = match text.as_bytes().first() {
                Some(b'+') => Some("diff-add"),
                Some(b'-') => Some("diff-del"),
                Some(b'~' | b'!') => Some("diff-chg"),
                _ => None,
            };
            match class {
                Some(c) => {
                    out.push_str("<span class=\"");
                    out.push_str(c);
                    out.push_str("\">");
                    out.push_str(text);
                    out.push_str("</span>");
                }
                None => out.push_str(text),
            }
            out.push_str(nl);
        }
        out.push_str(CODE_CLOSE);
        rest = &rest[body_start + len + CODE_CLOSE.len()..];
    }
    out.push_str(rest);
    out
}

#[cfg(test)]
mod tests;
