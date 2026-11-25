// Ported from internal/server/wirecompat_test.go: every case sends a fixed request
// and byte-compares status, Content-Type and body against Go's committed goldens
// in internal/server/testdata/wire (read in place, not copied).
use std::sync::Arc;

use tfstackplan_gate::events::{Init, StackState, Status};
use tfstackplan_server_clients::github::MockGitHub;
use tfstackplan_store::PrMeta;

use crate::testutil::{fake_oidc, seed_init, seed_projection_target, send};
use crate::{App, Config};

/// Go's `fixed` time as its SQLite driver stores a UTC time.Time.
const FIXED: &str = "2030-01-02 03:04:05 +0000 UTC";

fn golden(name: &str) -> Vec<u8> {
    let p = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../internal/server/testdata/wire")
        .join(format!("{name}.golden"));
    std::fs::read(&p).unwrap_or_else(|e| panic!("read {}: {e}", p.display()))
}

fn render(status: u16, ct: &str, body: &[u8]) -> Vec<u8> {
    let mut out = format!("HTTP {status}\nContent-Type: {ct}\n\n").into_bytes();
    out.extend_from_slice(body);
    out
}

#[tokio::test]
async fn api_wire_compat() {
    let (_dir, s) = crate::testutil::store();
    let mut app = App::new(
        Arc::clone(&s),
        Arc::new(MockGitHub::default()),
        Config {
            api_principals: [(
                "runner@x.iam.gserviceaccount.com".to_string(),
                vec![
                    "report".to_string(),
                    "read".to_string(),
                    "admin".to_string(),
                ],
            )]
            .into(),
            ..Config::default()
        },
    );
    app.api_verifier = Some(fake_oidc(&[
        ("tok", "runner@x.iam.gserviceaccount.com"),
        ("tok-noscopes", "stranger@x.iam.gserviceaccount.com"),
    ]));
    let app = Arc::new(app);

    let steps: &[(&str, &str, &str, &str, &str)] = &[
        (
            "01-init",
            "POST",
            "/api/init",
            "tok",
            r#"{"id":"e1","repo":"o/r","sha":"cafe1234cafe","pr":7,"environment":"staging","log_url":"https://ci/logs/1","stacks":[{"path":"stacks/a","project":"proj-a","status":"pending"},{"path":"stacks/b","project":"proj-b","status":"pending"}],"edges":[{"from":"stacks/a","to":"stacks/b"}]}"#,
        ),
        (
            "02-phase",
            "POST",
            "/api/phase",
            "tok",
            r#"{"id":"e1","phase":"planning"}"#,
        ),
        (
            "03-update",
            "POST",
            "/api/update",
            "tok",
            r#"{"id":"e1","stack":"stacks/a","status":"planned"}"#,
        ),
        (
            "04-update-unknown-status",
            "POST",
            "/api/update",
            "tok",
            r#"{"id":"e1","stack":"stacks/a","status":"bogus"}"#,
        ),
        (
            "05-logs",
            "POST",
            "/api/logs",
            "tok",
            "{\"id\":\"e1\",\"stack\":\"stacks/a\",\"data\":\"terraform plan says hi\\n\"}",
        ),
        (
            "06-finalize",
            "POST",
            "/api/finalize",
            "tok",
            "{\"id\":\"e1\",\"report_markdown\":\"## report\\n\",\"projects\":{\"stacks/a\":\"proj-a\"},\"stack_reports\":{\"stacks/a\":\"### stacks/a\\n\"},\"categories\":{\"stacks/a\":[{\"name\":\"iam\",\"icon\":\"🔐\"}]},\"counts\":{\"stacks/a\":{\"add\":1,\"change\":2}}}",
        ),
        (
            "07-gate-check-clean",
            "POST",
            "/api/gate/check",
            "tok",
            r#"{"pr":7,"environment":"staging"}"#,
        ),
        (
            "08-gate-check-not-classified",
            "POST",
            "/api/gate/check",
            "tok",
            r#"{"pr":99,"environment":"staging"}"#,
        ),
        (
            "09-gate-revoke",
            "POST",
            "/api/gate/revoke",
            "tok",
            r#"{"pr":7,"environment":"staging"}"#,
        ),
        (
            "10-claims-list",
            "POST",
            "/api/claims/list",
            "tok",
            r#"{"environment":"staging"}"#,
        ),
        (
            "11-claims-list-empty",
            "POST",
            "/api/claims/list",
            "tok",
            r#"{"environment":"deserted"}"#,
        ),
        (
            "12-claims-release",
            "POST",
            "/api/claims/release",
            "tok",
            r#"{"environment":"staging","pr":7,"stack":"stacks/a"}"#,
        ),
        ("13-execution-get", "GET", "/api/execution/e2", "tok", ""),
        (
            "14-execution-get-lifecycle",
            "GET",
            "/api/execution/e1",
            "tok",
            "",
        ),
        ("15-execution-404", "GET", "/api/execution/nope", "tok", ""),
        (
            "15b-execution-zero-stacks",
            "GET",
            "/api/execution/e3",
            "tok",
            "",
        ),
        (
            "16-unauthorized",
            "POST",
            "/api/claims/list",
            "",
            r#"{"environment":"staging"}"#,
        ),
        (
            "17-forbidden",
            "POST",
            "/api/claims/list",
            "tok-noscopes",
            r#"{"environment":"staging"}"#,
        ),
        ("18-executions-list", "GET", "/api/executions", "tok", ""),
        (
            "19-executions-pr-filter",
            "GET",
            "/api/executions?pr=7&limit=1",
            "tok",
            "",
        ),
        (
            "20-executions-bad-limit",
            "GET",
            "/api/executions?limit=0",
            "tok",
            "",
        ),
        ("21-approvals", "GET", "/api/approvals", "tok", ""),
        ("22-pr-get", "GET", "/api/pr/7", "tok", ""),
        ("23-pr-404", "GET", "/api/pr/424242", "tok", ""),
        (
            "24-pr-get-meta-only-no-execution",
            "GET",
            "/api/pr/555",
            "tok",
            "",
        ),
    ];

    let mut failures = Vec::new();
    for &(name, method, path, token, body) in steps {
        match name {
            "10-claims-list" => {
                for sp in ["stacks/a", "stacks/b"] {
                    s.exec(|c| {
                        c.execute(
                            "INSERT INTO apply_claims (environment, stack_path, owner_pr, execution_id, expires_at) VALUES (?1,?2,?3,?4,?5)",
                            rusqlite::params!["staging", sp, 7, "e1", FIXED],
                        )
                    })
                    .unwrap();
                }
            }
            "13-execution-get" => {
                seed_init(
                    &app,
                    Init {
                        id: "e2".into(),
                        repo: "o/r".into(),
                        sha: "beef5678beef".into(),
                        pr: 8,
                        environment: "staging".into(),
                        log_url: "https://ci/logs/2".into(),
                        stacks: vec![StackState {
                            path: "stacks/c".into(),
                            project: "proj-c".into(),
                            status: Status::Planned,
                            ..StackState::default()
                        }],
                        ..Init::default()
                    },
                )
                .await;
                seed_projection_target(
                    &s,
                    8,
                    "staging",
                    "iam",
                    "proj-c",
                    "projects/proj-c/locations/global/entitlements/iam/grants/g1",
                    "ACTIVE",
                    "req@x.iam.gserviceaccount.com",
                );
            }
            "15b-execution-zero-stacks" => {
                seed_init(
                    &app,
                    Init {
                        id: "e3".into(),
                        repo: "o/r".into(),
                        sha: "0000aaaa0000".into(),
                        pr: 9,
                        environment: "staging".into(),
                        log_url: "https://ci/logs/3".into(),
                        ..Init::default()
                    },
                )
                .await;
                s.exec(|c| {
                    c.execute(
                        "UPDATE executions SET created_at = ?1 WHERE id = 'e3'",
                        [FIXED],
                    )
                })
                .unwrap();
            }
            "21-approvals" => {
                seed_projection_target(
                    &s,
                    8,
                    "staging",
                    "destructive",
                    "proj-c",
                    "",
                    "AWAITING",
                    "req@x.iam.gserviceaccount.com",
                );
            }
            "22-pr-get" | "24-pr-get-meta-only-no-execution" => {
                let (pr, title, body, head, auto) = if name.starts_with("22") {
                    (
                        7,
                        "Add widget",
                        "Does widget things.",
                        "feature/widget",
                        true,
                    )
                } else {
                    (
                        555,
                        "Pre-execution PR",
                        "Meta arrives before init.",
                        "feature/pre-execution",
                        false,
                    )
                };
                s.upsert_pr_meta(&PrMeta {
                    repo: "o/r".into(),
                    pr,
                    title: title.into(),
                    body: body.into(),
                    author_login: "octocat".into(),
                    head_ref: head.into(),
                    url: format!("https://github.com/o/r/pull/{pr}"),
                    auto_merge: auto,
                    updated_at: None,
                })
                .unwrap();
            }
            _ => {}
        }
        if name.starts_with("13-") || name.starts_with("14-") {
            s.exec(|c| c.execute("UPDATE executions SET created_at = ?1", [FIXED]))
                .unwrap();
        }
        let resp = send(&app, method, path, token, body).await;
        let got = render(resp.status, &resp.content_type, &resp.body);
        let want = golden(name);
        if got != want {
            failures.push(format!(
                "{name}\n--- got ---\n{}\n--- want ---\n{}",
                String::from_utf8_lossy(&got),
                String::from_utf8_lossy(&want)
            ));
        }
    }
    assert!(
        failures.is_empty(),
        "wire mismatches:\n{}",
        failures.join("\n\n")
    );
}
