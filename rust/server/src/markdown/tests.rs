// Ported from internal/server/mddiff_test.go.
use super::*;

#[test]
fn diff_fence_lines_are_classed() {
    let html = render("```diff\n+ add\n- del\n~ chg\n! chg2\n  ctx\n```\n");
    assert_eq!(
        html,
        "<pre class=\"tfsp-diff\"><code><span class=\"diff-add\">+ add</span>\n<span class=\"diff-del\">- del</span>\n<span class=\"diff-chg\">~ chg</span>\n<span class=\"diff-chg\">! chg2</span>\n  ctx\n</code></pre>\n"
    );
}

#[test]
fn other_fences_keep_the_default_output() {
    assert_eq!(
        render("```hcl\na = <b>\n```\n"),
        "<pre><code class=\"language-hcl\">a = &lt;b&gt;\n</code></pre>\n"
    );
}

#[test]
fn raw_details_pass_through() {
    let html = render("<details><summary>x</summary>\n\nbody\n\n</details>\n");
    assert!(html.contains("<details><summary>x</summary>"), "{html}");
    assert!(html.contains("<p>body</p>"), "{html}");
}

#[test]
fn diff_content_is_escaped() {
    let out = render("```diff\n+ <script>alert(1)</script>\n```\n");
    assert!(!out.contains("<script>"), "{out}");
    assert!(out.contains("&lt;script&gt;"), "{out}");
}
