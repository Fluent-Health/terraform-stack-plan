//! These run real commands; they assume no TFSTACKPLAN_SERVER/EXECUTION in the
//! test environment (the Go tests unset them; Rust 2024 makes that unsafe).

use super::*;

fn args(a: &[&str]) -> Vec<String> {
    a.iter().map(|s| s.to_string()).collect()
}

fn ptmx() -> bool {
    std::path::Path::new("/dev/ptmx").exists()
}

// Go: TestRunStepPassthroughExitCode.
#[test]
fn passthrough_exit_code() {
    assert_eq!(
        wrap(&args(&["--stack", "s", "--", "sh", "-c", "exit 7"])),
        7
    );
    assert_eq!(
        wrap(&args(&["--stack", "s", "--", "sh", "-c", "exit 0"])),
        0
    );
}

// Go: TestRunStepRequiresSeparator.
#[test]
fn requires_separator() {
    assert_eq!(wrap(&args(&["--stack", "s"])), 2);
    assert_eq!(wrap(&args(&["--stack", "s", "--"])), 2);
}

// Go: TestRunWrapRunningFlagValidation.
#[test]
fn running_flag_validation() {
    assert_eq!(
        wrap(&args(&["--stack", "a", "--running", "bogus", "--", "true"])),
        2
    );
}

// Go: TestRunWrapCanonicalVerb (exit 0, no warning).
#[test]
fn canonical_verb() {
    assert_eq!(wrap(&args(&["--stack", "s", "--", "true"])), 0);
}

// Go: TestRunStepTTYExitCodePropagates.
#[test]
fn tty_exit_code_propagates() {
    if !ptmx() {
        eprintln!("skipping: no /dev/ptmx");
        return;
    }
    assert_eq!(
        wrap(&args(&[
            "--stack", "s", "--tty", "--", "sh", "-c", "exit 5"
        ])),
        5
    );
}

// Go: TestRunStepTTYMakesCommandSeeATTY. The child's output goes to this
// process's stdout, so the check is made by the child's exit code instead.
#[test]
fn tty_makes_command_see_a_tty() {
    if !ptmx() {
        eprintln!("skipping: no /dev/ptmx");
        return;
    }
    assert_eq!(
        wrap(&args(&[
            "--stack",
            "s",
            "--tty",
            "--",
            "sh",
            "-c",
            "test -t 1"
        ])),
        0
    );
    assert_ne!(
        wrap(&args(&["--stack", "s", "--", "sh", "-c", "test -t 1"])),
        0
    );
}

#[test]
fn killed_by_a_signal_is_255() {
    assert_eq!(code_of(wrap(&args(&["--", "sh", "-c", "kill -9 $$"]))), 255);
}

fn code_of(c: i32) -> i32 {
    c & 0xff
}

#[test]
fn missing_command_exits_1() {
    assert_eq!(wrap(&args(&["--", "definitely-not-a-command-xyz"])), 1);
}
