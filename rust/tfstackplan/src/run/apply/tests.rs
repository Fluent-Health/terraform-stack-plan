//! Go's apply orchestration tests, against a recording fake control plane, a fake
//! terramate and stubbed seams.

use std::cell::{Cell, RefCell};
use std::collections::BTreeMap;
use std::rc::Rc;

use casekit::{FakeServer, Route};
use tfstackplan_cli::run::{ChangeReason, Counts, Edge, GateTarget};
use tfstackplan_clients::server::Client;

use super::*;
use crate::run::terramate::ScriptOpts;

fn args(a: &[&str]) -> Vec<String> {
    a.iter().map(|s| s.to_string()).collect()
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

/// A control plane that accepts everything; the gate answers `gate`.
fn server(gate: (u16, &str)) -> FakeServer {
    let mut routes: Vec<Route> = [
        "/api/init",
        "/api/phase",
        "/api/finalize",
        "/api/gate/revoke",
        "/api/logs",
        "/api/update",
    ]
    .iter()
    .map(|p| route(p, 200, ""))
    .collect();
    routes.push(route("/api/gate/check", gate.0, gate.1));
    FakeServer::start(routes).unwrap()
}

#[derive(Default)]
struct FakeTm {
    changed: Vec<String>,
    all: Vec<String>,
    script_err: Option<String>,
    ran: RefCell<Vec<(ScriptOpts, Vec<(String, String)>)>>,
}

impl Terramate for Rc<FakeTm> {
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
        Ok(Vec::new())
    }
    fn script_run(&self, o: &ScriptOpts, env: &[(String, String)]) -> Result<(), String> {
        self.ran.borrow_mut().push((o.clone(), env.to_vec()));
        self.script_err.clone().map_or(Ok(()), Err)
    }
}

fn changed(stacks: &[&str]) -> Rc<FakeTm> {
    Rc::new(FakeTm {
        changed: args(stacks),
        ..FakeTm::default()
    })
}

/// What a test run did besides the server calls.
#[derive(Default)]
struct Trace {
    classify_parallel: Cell<Option<i64>>,
    classify_at: Cell<Option<usize>>,
    minted: RefCell<Vec<String>>,
}

struct Stubs {
    classify: Result<ClassifyResult, String>,
    moves: Result<(), String>,
    mint: Result<String, String>,
}

impl Default for Stubs {
    fn default() -> Self {
        Self {
            classify: Ok(ClassifyResult {
                report_no_table: "classified".into(),
                ..ClassifyResult::default()
            }),
            moves: Ok(()),
            mint: Ok("tok-123".into()),
        }
    }
}

/// Runs `run apply` with `vars`, the server, the fake terramate and the stubs.
fn run_apply(
    a: &[&str],
    vars: &[(&str, &str)],
    srv: &FakeServer,
    tm: Rc<FakeTm>,
    stubs: Stubs,
    trace: &Trace,
) -> i32 {
    let mut vars: BTreeMap<String, String> = vars
        .iter()
        .map(|(k, v)| (k.to_string(), v.to_string()))
        .collect();
    vars.insert("TFSTACKPLAN_SERVER".into(), srv.url());
    let getenv = move |k: &str| vars.get(k).cloned().unwrap_or_default();
    let url = srv.url();
    let client = move || Client::new(&url, None).unwrap();
    let tmf = move |_: &str| -> Box<dyn Terramate> { Box::new(tm.clone()) };
    let classify = |_: &dyn Terramate,
                    _: &str,
                    _: &[String],
                    _: &str,
                    _: bool,
                    _: &str,
                    parallel: i64| {
        trace.classify_parallel.set(Some(parallel));
        trace.classify_at.set(Some(srv.requests().len()));
        stubs.classify.clone()
    };
    let moves = |_: &str, _: Option<&StateLock>, _: &mut dyn FnMut(&str, &str)| stubs.moves.clone();
    let mint = |sa: &str| {
        trace.minted.borrow_mut().push(sa.to_string());
        stubs.mint.clone()
    };
    let lock = || Err("no lock in tests".to_string());
    apply(
        &args(a),
        &Os {
            getenv: &getenv,
            client: &client,
            tm: &tmf,
        },
        &Seams {
            classify: &classify,
            moves: &moves,
            mint: &mint,
            lock: &lock,
        },
    )
}

fn paths(srv: &FakeServer) -> Vec<String> {
    srv.requests().into_iter().map(|r| r.target).collect()
}

fn first(srv: &FakeServer, p: &str) -> Option<usize> {
    paths(srv).iter().position(|x| x == p)
}

fn before(srv: &FakeServer, a: &str, b: &str) -> bool {
    matches!((first(srv, a), first(srv, b)), (Some(x), Some(y)) if x < y)
}

fn finalizes(srv: &FakeServer) -> Vec<serde_json::Value> {
    srv.requests()
        .into_iter()
        .filter(|r| r.target == "/api/finalize")
        .map(|r| serde_json::from_slice(&r.body).unwrap())
        .collect()
}

const ENV: &[(&str, &str)] = &[("TFSTACKPLAN_PR", "7"), ("TFSTACKPLAN_ENVIRONMENT", "staging")];

// Go: TestRunApplyRequiresDir.
#[test]
fn requires_dir() {
    let srv = server((200, ""));
    assert_eq!(run_apply(&[], ENV, &srv, changed(&[]), Stubs::default(), &Trace::default()), 2);
}

// Go: TestRunApplyFailsClosedOnUnsatisfiedGate.
#[test]
fn fails_closed_on_an_unsatisfied_gate() {
    let srv = server((409, "gate not satisfied\n"));
    let tm = changed(&["stacks/a"]);
    let code = run_apply(&["--dir", "x"], ENV, &srv, tm.clone(), Stubs::default(), &Trace::default());
    assert_eq!(code, 1);
    assert_eq!(paths(&srv).iter().filter(|p| *p == "/api/gate/check").count(), 1);
    assert!(tm.ran.borrow().is_empty(), "apply script ran despite the gate");
    assert!(first(&srv, "/api/init").is_some());
}

// Go: TestRunApplyAbortsDeferredFinalize.
#[test]
fn refused_gate_sends_the_abort_finalize() {
    let srv = server((409, r#"{"code":"gate_not_satisfied","message":"gate not satisfied"}"#));
    assert_eq!(
        run_apply(&["--dir", "x"], ENV, &srv, changed(&["stacks/a"]), Stubs::default(), &Trace::default()),
        1
    );
    let last = finalizes(&srv).pop().unwrap();
    assert_eq!(last["failed"], true);
    assert_eq!(
        last["report_markdown"],
        "tfstackplan run apply: run aborted prematurely or failed during pre-flight validation."
    );
}

// Go: TestApplyZeroStacksSkipsGate.
#[test]
fn zero_stacks_skip_the_gate() {
    let srv = server((200, ""));
    assert_eq!(run_apply(&["--dir", "x"], ENV, &srv, changed(&[]), Stubs::default(), &Trace::default()), 0);
    assert!(first(&srv, "/api/gate/check").is_none());
    assert!(first(&srv, "/api/init").is_some());
    let fins = finalizes(&srv);
    assert_eq!(fins.len(), 1);
    assert!(fins[0].get("failed").is_none());
}

// Go: TestApplyClassifiesBeforeGate.
#[test]
fn classifies_before_the_gate() {
    let srv = server((200, ""));
    run_apply(&["--dir", "x"], ENV, &srv, changed(&["cluster/test"]), Stubs::default(), &Trace::default());
    assert!(before(&srv, "/api/finalize", "/api/gate/check"));
}

// Go: TestApplyFailureFinalizes.
#[test]
fn apply_failure_finalizes_failed() {
    let srv = server((200, ""));
    let tm = Rc::new(FakeTm {
        changed: args(&["cluster/test"]),
        script_err: Some("exit status 1".into()),
        ..FakeTm::default()
    });
    assert_eq!(run_apply(&["--dir", "x"], ENV, &srv, tm, Stubs::default(), &Trace::default()), 1);
    assert!(finalizes(&srv).iter().any(|f| f["failed"] == true));
}

// Go: TestApplyStateMoveFailureFinalizes.
#[test]
fn state_move_failure_finalizes_and_skips_the_script() {
    let srv = server((200, ""));
    let tm = changed(&["cluster/test"]);
    let stubs = Stubs {
        moves: Err("boom".into()),
        ..Stubs::default()
    };
    assert_eq!(run_apply(&["--dir", "x"], ENV, &srv, tm.clone(), stubs, &Trace::default()), 1);
    let last = finalizes(&srv).pop().unwrap();
    assert_eq!(last["failed"], true);
    assert_eq!(last["report_markdown"], "cross-state move failed: boom");
    assert!(tm.ran.borrow().is_empty());
    assert!(first(&srv, "/api/gate/revoke").is_some());
}

// Go: TestApplyHappyPathOrder.
#[test]
fn happy_path_order() {
    let srv = server((200, ""));
    assert_eq!(
        run_apply(&["--dir", "x"], ENV, &srv, changed(&["cluster/prod"]), Stubs::default(), &Trace::default()),
        0
    );
    assert!(before(&srv, "/api/init", "/api/finalize"));
    assert!(before(&srv, "/api/finalize", "/api/gate/check"));
    assert!(before(&srv, "/api/gate/check", "/api/gate/revoke"));
    let fins = finalizes(&srv);
    assert_eq!(fins.len(), 2);
    assert_eq!(fins[0]["report_markdown"], "classified");
    assert!(fins[1].get("failed").is_none());
}

// Go: TestApplyPassesParallel, TestApplyDefaultParallelIsZero,
// TestApplyClassifyPassUsesParallel.
#[test]
fn parallel_reaches_the_script_and_the_classify_pass() {
    let srv = server((200, ""));
    let tm = changed(&["cluster/prod"]);
    let trace = Trace::default();
    assert_eq!(run_apply(&["--dir", "x", "--parallel", "4"], ENV, &srv, tm.clone(), Stubs::default(), &trace), 0);
    assert_eq!(tm.ran.borrow()[0].0.parallel, 4);
    assert_eq!(trace.classify_parallel.get(), Some(4));
    let tm = changed(&["cluster/prod"]);
    run_apply(&["--dir", "x"], ENV, &srv, tm.clone(), Stubs::default(), &Trace::default());
    assert_eq!(tm.ran.borrow()[0].0.parallel, 0);
    assert_eq!(tm.ran.borrow()[0].0.args(), args(&["script", "run", "--changed", "apply"]));
}

// Go: TestApplyFinalizeCarriesCounts.
#[test]
fn classify_finalize_carries_counts_and_gates() {
    let srv = server((200, ""));
    let mut res = ClassifyResult {
        report_no_table: "classified".into(),
        gates: vec![GateTarget {
            class: "iam".into(),
            target: "proj-a".into(),
        }],
        ..ClassifyResult::default()
    };
    res.counts.insert(
        "a".into(),
        Counts {
            add: 3,
            ..Counts::default()
        },
    );
    let stubs = Stubs {
        classify: Ok(res),
        ..Stubs::default()
    };
    assert_eq!(run_apply(&["--dir", "x"], ENV, &srv, changed(&["stacks/a"]), stubs, &Trace::default()), 0);
    let fin = &finalizes(&srv)[0];
    assert_eq!(fin["counts"]["a"]["add"], 3);
    assert_eq!(fin["gates"], serde_json::json!([{"class":"iam","target":"proj-a"}]));
}

// Go: TestApplyFailsClosedOnClassifyFailureWhenImpersonating.
#[test]
fn classify_failure_under_impersonation_fails_closed() {
    let srv = server((200, r#"{"requester":"pool@x"}"#));
    let tm = changed(&["stacks/gcs-migration"]);
    let trace = Trace::default();
    let stubs = Stubs {
        classify: Err("boom".into()),
        ..Stubs::default()
    };
    assert_eq!(run_apply(&["--dir", "x", "--impersonate-requester"], ENV, &srv, tm.clone(), stubs, &trace), 1);
    assert!(tm.ran.borrow().is_empty());
    assert!(trace.minted.borrow().is_empty());
    assert!(first(&srv, "/api/gate/check").is_none());
    assert!(first(&srv, "/api/gate/revoke").is_none());
    assert_eq!(finalizes(&srv).pop().unwrap()["failed"], true);
}

// Go: TestApplyContinuesOnClassifyFailureWithoutImpersonate.
#[test]
fn classify_failure_without_impersonation_continues() {
    let srv = server((200, ""));
    let tm = changed(&["stacks/a"]);
    let stubs = Stubs {
        classify: Err("boom".into()),
        ..Stubs::default()
    };
    assert_eq!(run_apply(&["--dir", "x"], ENV, &srv, tm.clone(), stubs, &Trace::default()), 0);
    assert!(first(&srv, "/api/gate/check").is_some());
    assert_eq!(tm.ran.borrow().len(), 1);
}

// Go: TestApplyPhaseEmittedAfterGate.
#[test]
fn applying_phase_after_the_gate() {
    let srv = server((200, ""));
    run_apply(&["--dir", "x"], ENV, &srv, changed(&["cluster/prod"]), Stubs::default(), &Trace::default());
    assert!(before(&srv, "/api/gate/check", "/api/phase"));
}

// Go: TestRunApplyImpersonatesRequester.
#[test]
fn impersonates_the_requester_in_the_scripts_env() {
    let srv = server((200, r#"{"requester":"poolA@x"}"#));
    let tm = changed(&["stacks/a"]);
    let trace = Trace::default();
    assert_eq!(
        run_apply(&["--dir", "x", "--impersonate-requester"], ENV, &srv, tm.clone(), Stubs::default(), &trace),
        0
    );
    assert_eq!(*trace.minted.borrow(), vec!["poolA@x"]);
    let env = tm.ran.borrow()[0].1.clone();
    assert!(env.contains(&("GOOGLE_OAUTH_ACCESS_TOKEN".to_string(), "tok-123".to_string())));
}

// Go: TestRunApplyMintFailClosedImpersonate.
#[test]
fn mint_failure_fails_closed() {
    let srv = server((200, r#"{"requester":"poolA@x"}"#));
    let tm = changed(&["stacks/a"]);
    let stubs = Stubs {
        mint: Err("credentials unavailable".into()),
        ..Stubs::default()
    };
    assert_eq!(
        run_apply(&["--dir", "x", "--impersonate-requester"], ENV, &srv, tm.clone(), stubs, &Trace::default()),
        1
    );
    assert!(tm.ran.borrow().is_empty());
}

// Go: TestRunApplyNoImpersonateWhenFlagAbsent.
#[test]
fn no_impersonation_without_the_flag() {
    let srv = server((200, r#"{"requester":"poolA@x"}"#));
    let tm = changed(&["stacks/a"]);
    let trace = Trace::default();
    assert_eq!(run_apply(&["--dir", "x"], ENV, &srv, tm.clone(), Stubs::default(), &trace), 0);
    assert!(trace.minted.borrow().is_empty());
    assert!(tm.ran.borrow()[0].1.iter().all(|(k, _)| k != "GOOGLE_OAUTH_ACCESS_TOKEN"));
}

// Go: TestRunApplyWarmsBeforeClassifyPass. The warming phase needs a cache block;
// with no usable credentials warm stops after the tick, before the classify pass.
#[test]
fn warms_before_the_classify_pass() {
    let tmp = tempfile::tempdir().unwrap();
    let cfg = tmp.path().join(".tfstackplan.hcl");
    std::fs::write(&cfg, "cache {\n  bucket = \"b\"\n  prefix = \"p\"\n}\n").unwrap();
    let srv = server((200, ""));
    let tm = Rc::new(FakeTm {
        all: args(&["stacks/a"]),
        ..FakeTm::default()
    });
    let trace = Trace::default();
    let dir = tmp.path().to_string_lossy().into_owned();
    let cfg = cfg.to_string_lossy().into_owned();
    assert_eq!(
        run_apply(&["--dir", &dir, "--changed=false", "--config", &cfg], ENV, &srv, tm, Stubs::default(), &trace),
        0
    );
    let phase_at = first(&srv, "/api/phase").expect("warming phase");
    assert!(phase_at < trace.classify_at.get().unwrap());
}

// Go: TestRunApplyAbortsOnPendingStateMove, with the real move pre-phase on a
// directory holding a pending xmove (it needs terraform, which the test does
// not give it a usable state for, so the pre-phase fails closed).
#[test]
fn pending_state_move_without_terraform_state_fails_closed() {
    let tmp = tempfile::tempdir().unwrap();
    let dst = tmp.path().join("stacks/b");
    std::fs::create_dir_all(&dst).unwrap();
    let xm = tfstackplan_cli::statemove::XMove {
        source_stack: "stacks/a".into(),
        pairs: vec![tfstackplan_cli::statemove::Move::new("x.y", "x.y")],
    };
    std::fs::write(
        dst.join(tfstackplan_cli::statemove::xmove_file_name("PR-1")),
        tfstackplan_cli::statemove::render_xmove("PR-1", &xm),
    )
    .unwrap();
    let dir = tmp.path().to_string_lossy().into_owned();
    let mut moved = Vec::new();
    let mut sink = |s: &str, l: &str| moved.push(format!("{s} {l}"));
    // No terraform on a stack without config or state: the pre-phase must error.
    assert!(apply_moves(&dir, true, None, &mut std::io::sink(), Some(&mut sink)).is_err());
}
