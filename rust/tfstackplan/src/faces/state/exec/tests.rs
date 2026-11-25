use std::cell::RefCell;
use std::rc::Rc;

use tfstackplan_cli::statemove::plan::{StateModule, StateValues};
use tfstackplan_cli::statemove::{StateResource, render_xmove, xmove_file_name};

use super::*;

/// Counters a fake runner shares with its test (Go's `fakeRunner` fields).
#[derive(Default)]
struct Fake {
    state_json: String,
    show: Option<State>,
    inits: usize,
    mvs: usize,
    pushes: usize,
    pulls: usize,
    push_force: Vec<bool>,
    push_err: Option<fn(usize) -> Option<String>>,
}

type Shared = Rc<RefCell<Fake>>;

struct FakeRunner(Shared);

impl Runner for FakeRunner {
    fn init(&mut self) -> Result<(), String> {
        self.0.borrow_mut().inits += 1;
        Ok(())
    }
    fn state_pull(&mut self) -> Result<String, String> {
        let mut f = self.0.borrow_mut();
        f.pulls += 1;
        Ok(f.state_json.clone())
    }
    fn state_push(&mut self, _path: &Path, force: bool) -> Result<(), String> {
        let mut f = self.0.borrow_mut();
        f.pushes += 1;
        f.push_force.push(force);
        match f.push_err.and_then(|e| e(f.pushes)) {
            Some(e) => Err(e),
            None => Ok(()),
        }
    }
    fn show_state_file(&mut self, _path: &Path) -> Result<State, String> {
        self.0
            .borrow()
            .show
            .clone()
            .ok_or_else(|| "ShowStateFile called on empty state".to_string())
    }
    fn state_mv(&mut self, _: &str, _: &str, _: &Path, _: &Path) -> Result<(), String> {
        self.0.borrow_mut().mvs += 1;
        Ok(())
    }
}

/// A state whose root module holds `addrs` (Go's `stateWith`).
fn state_with(addrs: &[&str]) -> State {
    State {
        format_version: "1.0".into(),
        values: Some(StateValues {
            root_module: Some(StateModule {
                resources: addrs
                    .iter()
                    .map(|a| StateResource {
                        address: a.to_string(),
                        ..Default::default()
                    })
                    .collect(),
                child_modules: vec![],
            }),
        }),
    }
}

fn fake(state_json: &str, show: Option<State>) -> Shared {
    Rc::new(RefCell::new(Fake {
        state_json: state_json.into(),
        show,
        ..Default::default()
    }))
}

/// Source runner for dirs ending in `/a` (or `a`), destination otherwise (Go's `depsFor`).
fn deps_for<'a>(src: &Shared, dst: &Shared) -> ExecDeps<'a> {
    let (src, dst) = (src.clone(), dst.clone());
    ExecDeps {
        new_tf: Box::new(move |wd: &str| {
            let r = if wd.ends_with("/a") || wd == "a" {
                src.clone()
            } else {
                dst.clone()
            };
            let b: Box<dyn Runner> = Box::new(FakeRunner(r));
            Ok(b)
        }),
        backup_dir: None,
        locker: None,
    }
}

fn tmp() -> tempfile::TempDir {
    tempfile::Builder::new()
        .prefix("tfsp-exec-")
        .tempdir()
        .unwrap()
}

fn root(t: &tempfile::TempDir) -> String {
    t.path().to_string_lossy().into_owned()
}

fn single(from: &str, to: &str) -> XMove {
    XMove {
        source_stack: "a".into(),
        pairs: vec![Move::new(from, to)],
    }
}

// Go: TestExecuteMovesSourceOnly.
#[test]
fn execute_moves_source_only() {
    let src = fake("non-empty", Some(state_with(&["aws_s3_bucket.x"])));
    let dst = fake("non-empty", Some(state_with(&[])));
    let xm = single("aws_s3_bucket.x", "aws_s3_bucket.x");
    let t = tmp();
    execute(&deps_for(&src, &dst), &root(&t), "b", &xm, true).unwrap();
    assert_eq!(src.borrow().mvs + dst.borrow().mvs, 0);
    assert_eq!(src.borrow().pushes + dst.borrow().pushes, 0);

    let acts = execute(&deps_for(&src, &dst), &root(&t), "b", &xm, false).unwrap();
    assert_eq!(acts.len(), 1);
    assert_eq!(acts[0].decision, Decision::Move);
    assert_eq!(dst.borrow().mvs, 1);
    assert_eq!((src.borrow().pushes, dst.borrow().pushes), (1, 1));
}

// Go: TestExecuteEmptyDestState.
#[test]
fn execute_empty_dest_state() {
    let src = fake("non-empty", Some(state_with(&["aws_s3_bucket.x"])));
    let dst = fake("", None);
    let t = tmp();
    let acts = execute(
        &deps_for(&src, &dst),
        &root(&t),
        "b",
        &single("aws_s3_bucket.x", "aws_s3_bucket.x"),
        false,
    )
    .unwrap();
    assert_eq!(acts.len(), 1);
    assert_eq!(acts[0].decision, Decision::Move);
    assert_eq!(
        (dst.borrow().mvs, dst.borrow().pushes, src.borrow().pushes),
        (1, 1, 1)
    );
}

// Go: TestExecuteSkipAndAmbiguous.
#[test]
fn execute_skip_and_ambiguous() {
    let src = fake("non-empty", Some(state_with(&[])));
    let dst = fake("non-empty", Some(state_with(&["a.b"])));
    let xm = single("a.b", "a.b");
    let t = tmp();
    let acts = execute(&deps_for(&src, &dst), &root(&t), "b", &xm, false).unwrap();
    assert_eq!(acts.len(), 1);
    assert_eq!(acts[0].decision, Decision::Skip);
    assert_eq!(
        (dst.borrow().mvs, src.borrow().pushes, dst.borrow().pushes),
        (0, 0, 0)
    );

    let src2 = fake("non-empty", Some(state_with(&["a.b"])));
    let dst2 = fake("non-empty", Some(state_with(&["a.b"])));
    assert!(execute(&deps_for(&src2, &dst2), &root(&t), "b", &xm, false).is_err());
    assert_eq!((dst2.borrow().mvs, src2.borrow().pushes), (0, 0));
}

struct FakeLocker {
    acquired: RefCell<Vec<String>>,
    released: Rc<RefCell<usize>>,
    fail_on: &'static str,
}

impl Locker for FakeLocker {
    fn acquire(&self, stack_dir: &str) -> Result<Release<'_>, String> {
        if !self.fail_on.is_empty() && stack_dir.ends_with(self.fail_on) {
            return Err(format!("locked: {stack_dir}"));
        }
        self.acquired.borrow_mut().push(stack_dir.to_string());
        let released = self.released.clone();
        Ok(Box::new(move || *released.borrow_mut() += 1))
    }
}

fn locker(fail_on: &'static str) -> FakeLocker {
    FakeLocker {
        acquired: RefCell::new(vec![]),
        released: Rc::new(RefCell::new(0)),
        fail_on,
    }
}

// Go: TestExecuteAcquiresLockBeforePull.
#[test]
fn execute_acquires_lock_before_pull() {
    let src = fake("non-empty", Some(state_with(&["aws_s3_bucket.x"])));
    let dst = fake("non-empty", Some(state_with(&[])));
    let lk = locker("");
    let mut deps = deps_for(&src, &dst);
    deps.locker = Some(&lk);
    let t = tmp();
    execute(
        &deps,
        &root(&t),
        "b",
        &single("aws_s3_bucket.x", "aws_s3_bucket.x"),
        false,
    )
    .unwrap();
    assert_eq!(lk.acquired.borrow().len(), 2);
    assert_eq!(*lk.released.borrow(), 2);
}

// Go: TestExecuteFailsBeforePullWhenLocked.
#[test]
fn execute_fails_before_pull_when_locked() {
    let src = fake("", Some(state_with(&["aws_s3_bucket.x"])));
    let dst = fake("", Some(state_with(&[])));
    let lk = locker("/a");
    let mut deps = deps_for(&src, &dst);
    deps.locker = Some(&lk);
    let t = tmp();
    let e = execute(
        &deps,
        &root(&t),
        "b",
        &single("aws_s3_bucket.x", "aws_s3_bucket.x"),
        false,
    )
    .unwrap_err();
    assert!(e.starts_with("lock source a: locked: "), "{e}");
    assert_eq!((src.borrow().pulls, dst.borrow().pulls), (0, 0));
}

// Go: TestExecuteMovesWholeModule.
#[test]
fn execute_moves_whole_module() {
    let src = fake(
        "non-empty",
        Some(state_with(&[
            "module.a[0].google_x.one",
            "module.a[0].google_y.two[\"k\"]",
            "module.a[0].module.sub.google_z.three",
        ])),
    );
    let dst = fake("", Some(state_with(&[])));
    let acts = execute(
        &deps_for(&src, &dst),
        ".",
        "b",
        &single("module.a[0]", "module.b"),
        false,
    )
    .unwrap();
    assert_eq!(acts.len(), 3);
    assert!(acts.iter().all(|a| a.decision == Decision::Move));
    assert_eq!(dst.borrow().mvs, 3);
}

// Go: TestExecuteRollsBackSourceOnDestPushFailure.
#[test]
fn execute_rolls_back_source_on_dest_push_failure() {
    let src = fake("non-empty", Some(state_with(&["aws_s3_bucket.x"])));
    let dst = fake("non-empty", Some(state_with(&[])));
    dst.borrow_mut().push_err = Some(|_| Some("dest push boom".into()));
    let t = tmp();
    let e = execute(
        &deps_for(&src, &dst),
        &root(&t),
        "b",
        &single("aws_s3_bucket.x", "aws_s3_bucket.x"),
        false,
    )
    .unwrap_err();
    assert_eq!(
        e,
        "push dest state failed, rolled the source back to its pre-move state: dest push boom"
    );
    assert_eq!(src.borrow().pushes, 2);
    assert_eq!(dst.borrow().pushes, 1);
    assert_eq!(src.borrow().push_force, vec![false, true]);
}

// Go: TestExecuteLoudErrorWhenRollbackAlsoFails.
#[test]
fn execute_loud_error_when_rollback_also_fails() {
    let src = fake("non-empty", Some(state_with(&["aws_s3_bucket.x"])));
    src.borrow_mut().push_err = Some(|n| (n == 2).then(|| "rollback push boom".to_string()));
    let dst = fake("non-empty", Some(state_with(&[])));
    dst.borrow_mut().push_err = Some(|_| Some("dest push boom".into()));
    let backups = tmp();
    let mut deps = deps_for(&src, &dst);
    deps.backup_dir = Some(backups.path().to_path_buf());
    let t = tmp();
    let e = execute(
        &deps,
        &root(&t),
        "b",
        &single("aws_s3_bucket.x", "aws_s3_bucket.x"),
        false,
    )
    .unwrap_err();
    assert!(e.contains("orphaned"), "{e}");
    assert!(
        e.contains(&backups.path().to_string_lossy().into_owned()),
        "{e}"
    );
    assert_eq!((src.borrow().pushes, dst.borrow().pushes), (2, 1));
    // Both pre-move states were backed up.
    assert!(backups.path().join("a").is_dir());
    assert!(backups.path().join("b").is_dir());
}

// Go: TestApplyPendingMovesLogsPerStack.
#[test]
fn apply_pending_moves_logs_per_stack() {
    let t = tmp();
    let dir = t.path().join("stacks/b");
    std::fs::create_dir_all(&dir).unwrap();
    let xm = XMove {
        source_stack: "stacks/a".into(),
        pairs: vec![Move::new("x.y", "x.y")],
    };
    std::fs::write(dir.join(xmove_file_name("PR-1")), render_xmove("PR-1", &xm)).unwrap();

    let src = fake("non-empty", Some(state_with(&["x.y"])));
    let dst = fake("", None);
    let build = || -> Result<ExecDeps<'static>, String> {
        let (src, dst) = (src.clone(), dst.clone());
        Ok(ExecDeps {
            new_tf: Box::new(move |wd: &str| {
                let r = if wd.ends_with("/stacks/a") {
                    src.clone()
                } else {
                    dst.clone()
                };
                let b: Box<dyn Runner> = Box::new(FakeRunner(r));
                Ok(b)
            }),
            backup_dir: None,
            locker: None,
        })
    };
    let mut got = Vec::new();
    let mut sink = |stack: &str, line: &str| got.push(format!("{stack}|{line}"));
    let mut out = Vec::new();
    apply_pending_moves(&root(&t), false, &build, &mut out, Some(&mut sink)).unwrap();
    assert_eq!(
        got,
        vec!["stacks/b|PR-1\tstacks/a → stacks/b\twould move x.y → x.y"]
    );
    assert_eq!(
        String::from_utf8(out).unwrap(),
        "PR-1\tstacks/a → stacks/b\twould move x.y → x.y\n"
    );
}

#[test]
fn apply_pending_moves_needs_terraform_only_when_pending() {
    let t = tmp();
    let build =
        || -> Result<ExecDeps<'static>, String> { Err("terraform not found on PATH: x".into()) };
    let mut out = Vec::new();
    apply_pending_moves(&root(&t), false, &build, &mut out, None).unwrap();
    std::fs::create_dir_all(t.path().join("b")).unwrap();
    std::fs::write(
        t.path().join("b").join(xmove_file_name("PR-1")),
        render_xmove("PR-1", &single("x.y", "x.y")),
    )
    .unwrap();
    assert_eq!(
        apply_pending_moves(&root(&t), false, &build, &mut out, None).unwrap_err(),
        "1 pending cross-state move(s): terraform not found on PATH: x"
    );
}
