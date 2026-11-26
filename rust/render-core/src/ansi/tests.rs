use super::*;

#[test]
fn strip_sgr() {
    let cases = [
        ("no codes here", "no codes here"),
        ("\x1b[1m\x1b[32mApply complete!\x1b[0m", "Apply complete!"),
        ("0 \x1b[31madded\x1b[0m, 2 changed", "0 added, 2 changed"),
        ("a\x1b[0m\r\nb\n", "a\r\nb\n"),
        (
            "\x1b[1mApply complete! Resources: 0 added, 0 changed, 0 destroyed.\x1b[0m",
            "Apply complete! Resources: 0 added, 0 changed, 0 destroyed.",
        ),
    ];
    for (input, want) in cases {
        assert_eq!(strip(input), want);
    }
}
