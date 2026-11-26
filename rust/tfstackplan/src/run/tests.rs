//! The run verbs against a recording fake server and a fake terramate.

use std::cell::RefCell;
use std::collections::BTreeMap;

use casekit::{FakeServer, Route};
use tfstackplan_cli::run::ChangeReason;

use super::*;

fn args(a: &[&str]) -> Vec<String> {
    a.iter().map(|s| s.to_string()).collect()
}

fn ok(path: &str) -> Route {
    route(path, 200, "")
}

fn route(path: &str, status: u16, body: &str) -> Route {
    Route {
        method: "POST".into(),
        path: path.into(),
        status,
        body: body.as_bytes().to_vec(),
        then: Vec::new(),
    }
}

/// A fake terramate: fixed stacks, a recorded script run with a chosen result.
#[derive(Default)]
pub struct FakeTm {
    pub changed: Vec<String>,
    pub all: Vec<String>,
    pub edges: Vec<Edge>,
    pub script_err: Option<String>,
    pub ran: RefCell<Vec<(ScriptOpts, Vec<(String, String)>)>>,
}

impl Terramate for FakeTm {
    fn changed_stacks(&self, _: &str) -> Result<Vec<String>, String> {
        Ok(self.changed.clone())
    }
    fn list(&self) -> Result<Vec<String>, String> {
        Ok(self.all.clone())
    }
    fn why_changed(&self, _: &str) -> Result<Vec<ChangeReason>, String> {
        Ok(Vec::new())
    }
    fn run_graph(&self) -> Result<Vec<Edge>, String> {
        Ok(self.edges.clone())
    }
    fn script_run(&self, o: &ScriptOpts, env: &[(String, String)]) -> Result<(), String> {
        self.ran.borrow_mut().push((o.clone(), env.to_vec()));
        self.script_err.clone().map_or(Ok(()), Err)
    }
}

impl Terramate for std::rc::Rc<FakeTm> {
    fn changed_stacks(&self, b: &str) -> Result<Vec<String>, String> {
        self.as_ref().changed_stacks(b)
    }
    fn list(&self) -> Result<Vec<String>, String> {
        self.as_ref().list()
    }
    fn why_changed(&self, b: &str) -> Result<Vec<ChangeReason>, String> {
        self.as_ref().why_changed(b)
    }
    fn run_graph(&self) -> Result<Vec<Edge>, String> {
        self.as_ref().run_graph()
    }
    fn script_run(&self, o: &ScriptOpts, e: &[(String, String)]) -> Result<(), String> {
        self.as_ref().script_run(o, e)
    }
}

/// Runs a verb with `vars` as the environment, a client for `srv` (or none) and `tm`.
fn with_os<R>(
    vars: &[(&str, &str)],
    srv: Option<&FakeServer>,
    tm: std::rc::Rc<FakeTm>,
    f: impl FnOnce(&Os<'_>) -> R,
) -> R {
    let vars: BTreeMap<String, String> = vars
        .iter()
        .map(|(k, v)| (k.to_string(), v.to_string()))
        .collect();
    let getenv = move |k: &str| vars.get(k).cloned().unwrap_or_default();
    let url = srv.map(FakeServer::url).unwrap_or_default();
    let client = move || {
        let mut c = Client::new(&url, None).unwrap();
        c.set_update_backoff(std::time::Duration::from_millis(1));
        c
    };
    let tmf = move |_: &str| -> Box<dyn Terramate> { Box::new(tm.clone()) };
    f(&Os {
        getenv: &getenv,
        client: &client,
        tm: &tmf,
    })
}

fn paths(srv: &FakeServer) -> Vec<String> {
    srv.requests().into_iter().map(|r| r.target).collect()
}

fn body(srv: &FakeServer, path: &str) -> serde_json::Value {
    let r = srv
        .requests()
        .into_iter()
        .rfind(|r| r.target == path)
        .unwrap();
    serde_json::from_slice(&r.body).unwrap()
}

fn tm() -> std::rc::Rc<FakeTm> {
    std::rc::Rc::new(FakeTm::default())
}

// Go: TestRunTickPostsUpdate.
#[test]
fn tick_posts_update() {
    let srv = FakeServer::start(vec![ok("/api/update")]).unwrap();
    let code = with_os(&[("TFSTACKPLAN_EXECUTION", "e1")], Some(&srv), tm(), |os| {
        tick(&args(&["--stack", "stacks/a", "--status", "failed", "--detail", "boom"]), os)
    });
    assert_eq!(code, 0);
    assert_eq!(
        body(&srv, "/api/update"),
        serde_json::json!({"id":"e1","stack":"stacks/a","status":"failed","detail":"boom"})
    );
}

// Go: TestRunTickStackFromEnv.
#[test]
fn tick_stack_from_env() {
    let srv = FakeServer::start(vec![ok("/api/update")]).unwrap();
    with_os(
        &[("TFSTACKPLAN_EXECUTION", "e1"), ("TFSTACKPLAN_STACK", "stacks/env")],
        Some(&srv),
        tm(),
        |os| tick(&args(&["--status", "running"]), os),
    );
    assert_eq!(body(&srv, "/api/update")["stack"], "stacks/env");
}

// Go: TestRunTickOfflineIsNoop, TestRunTickBestEffortOnServerError.
#[test]
fn tick_offline_and_server_errors_exit_0() {
    assert_eq!(
        with_os(&[("TFSTACKPLAN_EXECUTION", "e1")], None, tm(), |os| tick(
            &args(&["--stack", "a", "--status", "running"]),
            os
        )),
        0
    );
    let srv = FakeServer::start(vec![route("/api/update", 500, "boom")]).unwrap();
    assert_eq!(
        with_os(&[("TFSTACKPLAN_EXECUTION", "e1")], Some(&srv), tm(), |os| tick(
            &args(&["--stack", "a", "--status", "running"]),
            os
        )),
        0
    );
    assert_eq!(paths(&srv).len(), 3, "retried 5xx");
    assert_eq!(with_os(&[], None, tm(), |os| tick(&args(&["--bogus"]), os)), 2);
}

// Go: TestRunUnknownSubcommand.
#[test]
fn unknown_verb() {
    assert_eq!(run(&args(&["bogus"])), ExitCode::from(2));
    assert_eq!(run(&args(&[])), ExitCode::from(2));
}

// Go: TestRunPhasePostsEvent, TestRunPhaseNoExecIsNoop.
#[test]
fn phase_posts_event_or_noops() {
    let srv = FakeServer::start(vec![ok("/api/phase")]).unwrap();
    with_os(
        &[
            ("TFSTACKPLAN_EXECUTION", "e1"),
            ("TFSTACKPLAN_REPO", "acme/infra"),
            ("TFSTACKPLAN_PR", "7"),
            ("TFSTACKPLAN_ENVIRONMENT", "staging"),
        ],
        Some(&srv),
        tm(),
        |os| phase(&args(&["--phase", "warming", "--label", "warming…", "--progress-pct", "40"]), os),
    );
    assert_eq!(
        body(&srv, "/api/phase"),
        serde_json::json!({"id":"e1","repo":"acme/infra","pr":7,"environment":"staging","phase":"warming","label":"warming…","progress_pct":40})
    );
    let srv = FakeServer::start(vec![ok("/api/phase")]).unwrap();
    with_os(&[], Some(&srv), tm(), |os| phase(&args(&["--phase", "warming"]), os));
    with_os(&[("TFSTACKPLAN_EXECUTION", "e1")], Some(&srv), tm(), |os| {
        phase(&args(&["--phase", "x", "--progress-pct", "101"]), os)
    });
    assert_eq!(paths(&srv), vec!["/api/phase"]);
    assert!(body(&srv, "/api/phase").get("progress_pct").is_none());
}

// Go: TestRunRegisterOfflineNoop.
#[test]
fn register_offline_noop_and_online_init() {
    let t = tm();
    assert_eq!(with_os(&[("TFSTACKPLAN_EXECUTION", "e1")], None, t.clone(), |os| register(&args(&[]), os)), 0);
    let srv = FakeServer::start(vec![ok("/api/init")]).unwrap();
    let t = std::rc::Rc::new(FakeTm {
        all: args(&["stacks/a", "stacks/b"]),
        edges: vec![Edge {
            from: "/x/stacks/a".into(),
            to: "stacks/b".into(),
        }],
        ..FakeTm::default()
    });
    with_os(&[("TFSTACKPLAN_EXECUTION", "e1")], Some(&srv), t, |os| register(&args(&[]), os));
    let init = body(&srv, "/api/init");
    assert_eq!(init["stacks"][1], serde_json::json!({"path":"stacks/b","status":"pending"}));
    assert_eq!(init["edges"], serde_json::json!([{"from":"stacks/a","to":"stacks/b"}]));
}

// Go: TestRunExecRequiresCommand, TestRunExecSuccess, TestRunExecFailure,
// TestRunExecCustomPhase, TestRunExecOfflineCommandStillRuns,
// TestRunExecNoExecIDSkipsServer, TestRunExecNoPhaseSkipsPhasePost.
#[test]
fn exec_runs_ticks_and_finalizes() {
    assert_eq!(with_os(&[], None, tm(), |os| exec(&args(&["--phase", "planning"]), os)), 2);
    let srv = FakeServer::start(vec![ok("/api/phase"), ok("/api/finalize")]).unwrap();
    let e1 = [("TFSTACKPLAN_EXECUTION", "e1")];
    assert_eq!(with_os(&e1, Some(&srv), tm(), |os| exec(&args(&["--phase", "testing", "--", "true"]), os)), 0);
    assert_eq!(paths(&srv), vec!["/api/phase"]);
    assert_eq!(body(&srv, "/api/phase")["phase"], "testing");
    assert_eq!(with_os(&e1, Some(&srv), tm(), |os| exec(&args(&["sh", "-c", "exit 3"]), os)), 3);
    assert_eq!(paths(&srv), vec!["/api/phase", "/api/finalize"]);
    assert_eq!(body(&srv, "/api/finalize"), serde_json::json!({"id":"e1","report_markdown":"","failed":true}));
    assert_eq!(with_os(&[], None, tm(), |os| exec(&args(&["sh", "-c", "exit 4"]), os)), 4);
    let quiet = FakeServer::start(vec![]).unwrap();
    assert_eq!(with_os(&[], Some(&quiet), tm(), |os| exec(&args(&["--phase", "x", "false"]), os)), 1);
    assert!(quiet.requests().is_empty());
}

// Go: TestRunLintRequiresDir, TestRunLintSuccess, TestRunLintFailure.
#[test]
fn lint_runs_the_script_and_finalizes_failure() {
    assert_eq!(with_os(&[], None, tm(), |os| lint(&args(&[]), os)), 2);
    let t = std::rc::Rc::new(FakeTm {
        changed: args(&["stacks/a"]),
        ..FakeTm::default()
    });
    let srv = FakeServer::start(vec![ok("/api/init"), ok("/api/phase"), ok("/api/finalize")]).unwrap();
    let e1 = [("TFSTACKPLAN_EXECUTION", "e1")];
    assert_eq!(with_os(&e1, Some(&srv), t.clone(), |os| lint(&args(&["--dir", "x", "--parallel", "3"]), os)), 0);
    let (opts, env) = t.ran.borrow()[0].clone();
    assert_eq!(opts.args(), args(&["script", "run", "--changed", "--parallel", "3", "lint"]));
    assert_eq!(env, vec![("TFSTACKPLAN_EXECUTION".to_string(), "e1".to_string())]);
    assert_eq!(paths(&srv), vec!["/api/init", "/api/phase"]);
    let t = std::rc::Rc::new(FakeTm {
        changed: args(&["stacks/a"]),
        script_err: Some("exit status 1".into()),
        ..FakeTm::default()
    });
    assert_eq!(with_os(&e1, Some(&srv), t, |os| lint(&args(&["--dir", "x"]), os)), 1);
    assert_eq!(paths(&srv).last().unwrap(), "/api/finalize");
}

// Go: TestRunVerifyRequiresDir, TestRunVerifyE2E (against the fake terramate).
#[test]
fn verify_reports_its_own_check_run() {
    assert_eq!(with_os(&[], None, tm(), |os| verify(&args(&[]), os)), 2);
    let srv = FakeServer::start(vec![ok("/api/init"), ok("/api/phase"), ok("/api/finalize"), ok("/api/logs")]).unwrap();
    let t = std::rc::Rc::new(FakeTm {
        all: args(&["stacks/a", "stacks/b"]),
        ..FakeTm::default()
    });
    let vars = [("TFSTACKPLAN_EXECUTION", "v1"), ("TFSTACKPLAN_ENVIRONMENT", "prod")];
    assert_eq!(with_os(&vars, Some(&srv), t, |os| verify(&args(&["--dir", "x", "--changed=false"]), os)), 0);
    assert_eq!(body(&srv, "/api/init")["context"], "verify/prod");
    assert_eq!(body(&srv, "/api/phase")["phase"], "verifying");
    assert_eq!(
        body(&srv, "/api/finalize"),
        serde_json::json!({"id":"v1","report_markdown":"✅ Verification passed (2 stacks)."})
    );
    let t = std::rc::Rc::new(FakeTm {
        all: args(&["stacks/a"]),
        script_err: Some("exit status 2".into()),
        ..FakeTm::default()
    });
    assert_eq!(with_os(&vars, Some(&srv), t, |os| verify(&args(&["--dir", "x", "--changed=false"]), os)), 1);
    assert_eq!(
        body(&srv, "/api/finalize"),
        serde_json::json!({"id":"v1","report_markdown":"❌ Verification failed.","failed":true})
    );
}

// Go: TestTerramateScriptRun (the argv; running it is the recorded cases' job).
#[test]
fn script_run_argv() {
    let o = ScriptOpts {
        script: "plan".into(),
        changed: true,
        parallel: 4,
        base: "origin/main".into(),
    };
    assert_eq!(
        o.args(),
        args(&["script", "run", "--changed", "--parallel", "4", "-B", "origin/main", "plan"])
    );
    assert_eq!(ScriptOpts { script: "apply".into(), ..ScriptOpts::default() }.args(), args(&["script", "run", "apply"]));
}
