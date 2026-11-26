use super::*;

#[test]
fn resolve_templates() {
    let vars: HashMap<String, String> = [
        ("sha", "abc1234567"),
        ("file", "main.tf"),
        ("line", "12"),
        ("empty", ""),
    ]
    .into_iter()
    .map(|(k, v)| (k.to_string(), v.to_string()))
    .collect();
    let cases = [
        ("b/{sha}/{file}#L{line}", "b/abc1234567/main.tf#L12"),
        ("https://x/y", "https://x/y"),
        ("b/{sha}/{nope}", ""),
        ("b/{empty}", ""),
        ("{sha}-{sha}", "abc1234567-abc1234567"),
        ("unclosed {sha", "unclosed {sha"),
    ];
    for (tmpl, want) in cases {
        assert_eq!(resolve(tmpl, &vars), want, "{tmpl}");
    }
}
