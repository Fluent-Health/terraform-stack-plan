//! `run plan` against the recording fake control plane, with real terramate on a
//! copy of Go's plan fixture and a stub terraform (skipped without terramate).

use std::collections::BTreeMap;
use std::path::Path;

use casekit::{FakeServer, Route};
use tfstackplan_cli::run::{ChangeReason, Edge};

use super::*;
use crate::run::terramate::{Real, Terramate};

fn args(a: &[&str]) -> Vec<String> {
    a.iter().map(|s| s.to_string()).collect()
}

fn have(bin: &str) -> bool {
    std::env::var_os("PATH")
        .is_some_and(|p| std::env::split_paths(&p).any(|d| d.join(bin).is_file()))
}

/// The real driver with a stub-terraform directory first on the scripts' PATH.
struct WithPath(Real, String);

impl Terramate for WithPath {
    fn changed_stacks(&self, b: &str) -> Result<Vec<String>, String> {
        self.0.changed_stacks(b)
    }
    fn list(&self) -> Result<Vec<String>, String> {
        self.0.list()
    }
    fn why_changed(&self, b: &str) -> Result<Vec<ChangeReason>, String> {
        self.0.why_changed(b)
    }
    fn run_graph(&self) -> Result<Vec<Edge>, String> {
        self.0.run_graph()
    }
    fn script_run(&self, o: &ScriptOpts, env: &[(String, String)]) -> Result<(), String> {
        let mut env = env.to_vec();
        env.push(("PATH".into(), self.1.clone()));
        self.0.script_run(o, &env)
    }
}

const PLAN_JSON: &str = r#"{"format_version":"1.2","resource_changes":[{"address":"google_project_iam_member.x","type":"google_project_iam_member","name":"x","change":{"actions":["create"],"before":null,"after":{"project":"proj-a"}}}]}"#;

/// Go's plan fixture copied into `dir`, made a project root without git, with a
/// stub terraform; returns the scripts' PATH.
fn fixture(dir: &Path) -> String {
    let src =
        Path::new(env!("CARGO_MANIFEST_DIR")).join("../../cmd/tfstackplan/testdata/planfixture");
    for rel in [
        ".tfstackplan.hcl",
        "terramate.tm.hcl",
        "stacks/a/stack.tm.hcl",
        "stacks/b/stack.tm.hcl",
    ] {
        let to = dir.join(rel);
        std::fs::create_dir_all(to.parent().unwrap()).unwrap();
        let mut text = std::fs::read_to_string(src.join(rel)).unwrap();
        if rel == "terramate.tm.hcl" {
            text = text.replacen(
                "terramate {",
                "terramate {\n  required_version = \">= 0.17.0\"",
                1,
            );
        }
        std::fs::write(to, text).unwrap();
    }
    let bin = dir.join("bin");
    std::fs::create_dir_all(&bin).unwrap();
    let stub = format!(
        "#!/bin/sh\ncase \"$1 $2\" in\n  \"show -json\") cat <<'J'\n{PLAN_JSON}\nJ\n  ;;\n  *) : ;;\nesac\n"
    );
    let tf = bin.join("terraform");
    std::fs::write(&tf, stub).unwrap();
    use std::os::unix::fs::PermissionsExt;
    std::fs::set_permissions(&tf, std::fs::Permissions::from_mode(0o755)).unwrap();
    format!(
        "{}:{}",
        bin.display(),
        std::env::var("PATH").unwrap_or_default()
    )
}

fn server() -> FakeServer {
    FakeServer::start(
        ["/api/init", "/api/phase", "/api/finalize", "/api/logs"]
            .iter()
            .map(|p| Route {
                method: "POST".into(),
                path: p.to_string(),
                status: 200,
                body: Vec::new(),
                then: Vec::new(),
            })
            .collect(),
    )
    .unwrap()
}

fn run_plan(a: &[&str], srv: &FakeServer, path: &str) -> i32 {
    let vars: BTreeMap<String, String> = [
        ("TFSTACKPLAN_SERVER".to_string(), srv.url()),
        ("TFSTACKPLAN_ENVIRONMENT".to_string(), "staging".to_string()),
    ]
    .into_iter()
    .collect();
    let getenv = move |k: &str| vars.get(k).cloned().unwrap_or_default();
    let url = srv.url();
    let client = move || Client::new(&url, None).unwrap();
    let path = path.to_string();
    let tmf =
        move |d: &str| -> Box<dyn Terramate> { Box::new(WithPath(Real::new(d), path.clone())) };
    plan(
        &args(a),
        &Os {
            getenv: &getenv,
            client: &client,
            tm: &tmf,
        },
    )
}

fn bodies(srv: &FakeServer, p: &str) -> Vec<serde_json::Value> {
    srv.requests()
        .into_iter()
        .filter(|r| r.target == p)
        .map(|r| serde_json::from_slice(&r.body).unwrap())
        .collect()
}

// Go: TestRunPlanRequiresDir.
#[test]
fn requires_dir() {
    let srv = server();
    assert_eq!(run_plan(&[], &srv, ""), 2);
}

// Go: TestRunPlanE2E.
#[test]
fn plans_classifies_and_finalizes() {
    if !have("terramate") {
        eprintln!("skipping: terramate not on PATH");
        return;
    }
    let tmp = tempfile::tempdir().unwrap();
    let path = fixture(tmp.path());
    let srv = server();
    let dir = tmp.path().to_string_lossy().into_owned();
    assert_eq!(
        run_plan(&["--dir", &dir, "--changed=false"], &srv, &path),
        0
    );
    assert_eq!(
        bodies(&srv, "/api/init")[0]["stacks"]
            .as_array()
            .unwrap()
            .len(),
        2
    );
    let fin = bodies(&srv, "/api/finalize").pop().unwrap();
    assert!(!fin["report_markdown"].as_str().unwrap().is_empty());
    assert_eq!(
        fin["gates"],
        serde_json::json!([{"class":"iam","target":"proj-a"}])
    );
    assert!(
        fin["stack_reports"]
            .as_object()
            .is_some_and(|m| !m.is_empty())
    );
    assert!(fin["categories"].as_object().is_some_and(|m| !m.is_empty()));
    let logs = bodies(&srv, "/api/logs");
    for s in ["stacks/a", "stacks/b"] {
        let text: String = logs
            .iter()
            .filter(|l| l["stack"] == s)
            .map(|l| l["data"].as_str().unwrap_or_default().to_string())
            .collect();
        assert!(text.contains(s), "logs for {s}: {text:?}");
    }
}

// Go: TestRunPlanAbortsDeferredFinalize.
#[test]
fn render_failure_sends_the_abort_finalize() {
    if !have("terramate") {
        eprintln!("skipping: terramate not on PATH");
        return;
    }
    let tmp = tempfile::tempdir().unwrap();
    let path = fixture(tmp.path());
    let bad = tmp.path().join("bad.hcl");
    std::fs::write(&bad, "malformed_hcl_syntax {").unwrap();
    let srv = server();
    let dir = tmp.path().to_string_lossy().into_owned();
    assert_eq!(
        run_plan(
            &[
                "--dir",
                &dir,
                "--changed=false",
                "--config",
                &bad.to_string_lossy()
            ],
            &srv,
            &path
        ),
        1
    );
    assert_eq!(
        bodies(&srv, "/api/init")[0]["stacks"]
            .as_array()
            .unwrap()
            .len(),
        2
    );
    let fin = bodies(&srv, "/api/finalize").pop().unwrap();
    assert_eq!(fin["failed"], true);
    assert_eq!(
        fin["report_markdown"],
        "tfstackplan run plan: run aborted prematurely or failed during pre-flight validation."
    );
}

// Go: TestClassifyForGateReturnsGates (classifypass_test.go).
#[test]
fn classify_for_gate_returns_gates() {
    if !have("terramate") {
        eprintln!("skipping: terramate not on PATH");
        return;
    }
    let tmp = tempfile::tempdir().unwrap();
    let path = fixture(tmp.path());
    let dir = tmp.path().to_string_lossy().into_owned();
    let tm = WithPath(Real::new(&dir), path);
    let res = crate::run::classify::classify_for_gate(
        &tm,
        &dir,
        &args(&["stacks/a", "stacks/b"]),
        "",
        false,
        "",
        0,
    )
    .unwrap();
    assert!(
        res.gates
            .iter()
            .any(|g| g.class == "iam" && g.target == "proj-a")
    );
    assert!(!res.report.is_empty());
    assert!(!res.categories.is_empty());
}
