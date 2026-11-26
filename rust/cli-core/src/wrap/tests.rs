use super::*;

// Go: TestClassifyStep.
#[test]
fn classify_step() {
    let no_change = "...\nApply complete! Resources: 0 added, 0 changed, 0 destroyed.\n";
    let changed = "...\nApply complete! Resources: 0 added, 20 changed, 0 destroyed.\n";
    let plan_out = "...\nPlan: 0 to add, 2 to change, 0 to destroy.\nSaved the plan to: plan.bin\n";
    let cases = [
        ("apply no changes", 0, no_change, "safe", "nochange"),
        ("apply with changes", 0, changed, "safe", "safe"),
        ("plan success not split", 0, plan_out, "planned", "planned"),
        ("failure", 1, changed, "safe", "failed"),
        (
            "intermediate success (init)",
            0,
            "Terraform has been successfully initialized!",
            "",
            "",
        ),
        (
            "apply summary without onSuccess defaults safe",
            0,
            changed,
            "",
            "safe",
        ),
    ];
    for (name, exit, output, on_success, want) in cases {
        assert_eq!(classify_outcome(exit, output, on_success), want, "{name}");
    }
}

// Go: TestClassifyStepColoredApplyComplete.
#[test]
fn classify_step_colored_apply_complete() {
    let colored = "\x1b[0m\x1b[1m\x1b[32mApply complete! Resources: 0 added, 0 changed, 0 destroyed.\x1b[0m\n";
    assert_eq!(
        classify_outcome(0, &crate::ansi::strip(colored), "safe"),
        "nochange"
    );
    let colored_chg =
        "\x1b[32mApply complete! Resources: 0 added, 20 changed, 0 destroyed.\x1b[0m\n";
    assert_eq!(
        classify_outcome(0, &crate::ansi::strip(colored_chg), "safe"),
        "safe"
    );
}

// Go: TestLogStreamer.
#[test]
fn log_batcher_holds_small_writes_until_flush() {
    let mut b = LogBatcher::default();
    assert_eq!(b.write(b"hello "), None);
    assert_eq!(b.flush().as_deref(), Some("hello "));
    assert_eq!(b.flush(), None);
}

// Go: TestLogStreamerThresholdFlush.
#[test]
fn log_batcher_flushes_at_threshold() {
    let mut b = LogBatcher::default();
    let first = b.write(&[b'x'; 5000]).unwrap();
    let rest = b.flush().unwrap_or_default();
    assert_eq!(first.len() + rest.len(), 5000);
}

#[test]
fn known_statuses_and_last_line() {
    assert!(known_status("running"));
    assert!(known_status("aborted"));
    assert!(!known_status("bogus"));
    assert_eq!(last_line("a\n  b  \n\n \n"), "b");
    assert_eq!(last_line(""), "");
}

#[test]
fn capture_keeps_the_tail() {
    let mut c = Capture::default();
    c.write(&vec![b'a'; CAPTURE_MAX]);
    c.write(b"tail");
    let t = c.text();
    assert_eq!(t.len(), CAPTURE_MAX);
    assert!(t.ends_with("tail"));
}
