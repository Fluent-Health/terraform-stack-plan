use super::*;

const BODY: &str = r#"{"ID":"exec-1","Repo":"owner/repo","SHA":"","PR":101,"Environment":"production","CheckRunID":{"Int64":0,"Valid":false},"Rev":0,"ReportMarkdown":"","LogURL":"","Status":"success","StatusContext":"","Phase":"planning","CreatedAt":"0001-01-01T00:00:00Z","SupersededBy":"","graph":{"stacks":[{"path":"stacks/db","status":"planned","counts":{"add":3,"change":1}}],"edges":null},"gates":[{"Class":"iam","Target":"proj-a","GrantName":"","State":"active","Requester":""}]}"#;

// Go: TestRunStatusText.
#[test]
fn text_view() {
    let e = decode(BODY.as_bytes()).unwrap();
    let out = text(&e);
    for want in [
        "Execution ID: exec-1",
        "Repo/PR:      owner/repo #101",
        "Status:       SUCCESS",
        "Phase:        planning",
        "Environment:  production",
        "  stacks/db                     : PLANNED      | +3, -0, ~1",
        "iam (proj-a): active",
    ] {
        assert!(out.contains(want), "missing {want:?} in\n{out}");
    }
    assert_eq!(e.exit_code(), 0);
    assert!(e.terminal());
}

// Go: TestRunStatusJSON (round trip in Go's field order).
#[test]
fn json_view_round_trips() {
    let e = decode(BODY.as_bytes()).unwrap();
    let out = json(&e).unwrap();
    let compact: Value = serde_json::from_str(&out).unwrap();
    let want: Value = serde_json::from_str(BODY).unwrap();
    assert_eq!(compact, want);
    assert!(out.starts_with("{\n  \"ID\": \"exec-1\",\n  \"Repo\""));
}

// Go: TestRunStatusTerminalExitCode.
#[test]
fn failure_exits_1() {
    let e = decode(br#"{"ID":"exec-failed","Status":"failure"}"#).unwrap();
    assert_eq!(e.exit_code(), 1);
    let e = decode(br#"{"id":"x","status":"in_progress"}"#).unwrap();
    assert!(!e.terminal());
    assert_eq!(e.get("ID"), "x");
}

#[test]
fn times_and_bad_statuses() {
    assert_eq!(
        time_field("2026-01-02T03:04:05.120Z").unwrap(),
        "2026-01-02T03:04:05.12Z"
    );
    assert_eq!(
        time_field("2026-01-02T03:04:05+05:30").unwrap(),
        "2026-01-02T03:04:05+05:30"
    );
    assert!(decode(br#"{"graph":{"stacks":[{"path":"a","status":"bogus"}]}}"#).is_err());
}
