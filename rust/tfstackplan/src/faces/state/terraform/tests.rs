use std::path::PathBuf;

use super::*;
use crate::faces::state::exec::{ExecDeps, execute};
use tfstackplan_cli::statemove::decide::Decision;
use tfstackplan_cli::statemove::{Move, XMove, state_addresses};

// The argv terraform-exec v0.25.1 builds (tfexec/init.go, state_pull.go,
// state_push.go, show.go, state_mv.go) for terraform >= 0.15.
#[test]
fn argv_matches_terraform_exec() {
    assert_eq!(
        argv::init(),
        [
            "init",
            "-no-color",
            "-input=false",
            "-backend=true",
            "-get=true",
            "-upgrade=false"
        ]
    );
    assert_eq!(argv::state_pull(), ["state", "pull"]);
    assert_eq!(
        argv::state_push("/t/s.tfstate", false),
        [
            "state",
            "push",
            "-lock=false",
            "-lock-timeout=0s",
            "/t/s.tfstate"
        ]
    );
    assert_eq!(
        argv::state_push("/t/s.tfstate", true),
        [
            "state",
            "push",
            "-force",
            "-lock=false",
            "-lock-timeout=0s",
            "/t/s.tfstate"
        ]
    );
    assert_eq!(
        argv::show_state_file("f"),
        ["show", "-json", "-no-color", "f"]
    );
    assert_eq!(
        argv::state_mv("a.b", "c.d", "s", "o"),
        [
            "state",
            "mv",
            "-no-color",
            "-lock-timeout=0s",
            "-state=s",
            "-state-out=o",
            "-lock=true",
            "a.b",
            "c.d"
        ]
    );
}

#[test]
fn user_agent_merge() {
    assert_eq!(merge_user_agent(&["", TFEXEC_UA]), TFEXEC_UA);
    assert_eq!(
        merge_user_agent(&[" mine ", TFEXEC_UA, "mine"]),
        format!("mine {TFEXEC_UA}")
    );
}

#[test]
fn new_requires_an_existing_workdir() {
    assert_eq!(
        Terraform::new("terraform", "/no/such/dir").err().unwrap(),
        "error initialising Terraform with workdir /no/such/dir: stat /no/such/dir: no such file or directory"
    );
}

#[test]
fn failing_command_reports_exit_status_and_stderr() {
    let t = Terraform::new("/bin/sh", "/").unwrap();
    assert_eq!(
        t.run(&["-c", "echo oops >&2; exit 3"]).unwrap_err(),
        "exit status 3\noops\n"
    );
}

/// The real terraform binary, or `None` (the integration tests then skip).
fn terraform() -> Option<String> {
    match look_path("terraform") {
        Ok(p) => Some(p),
        Err(_) => {
            eprintln!(
                "skipping: terraform not on PATH (the Go integration tests skip the same way)"
            );
            None
        }
    }
}

fn write(dir: &Path, name: &str, body: &str) {
    std::fs::create_dir_all(dir).unwrap();
    std::fs::write(dir.join(name), body).unwrap();
}

fn tf_stack(tf: &str, dir: &Path, config: &str) -> Terraform {
    write(
        dir,
        "main.tf",
        &format!("terraform {{\n  backend \"local\" {{}}\n}}\n{config}"),
    );
    let mut t = Terraform::new(tf, &dir.to_string_lossy()).unwrap();
    t.init().unwrap();
    t
}

fn addrs(t: &Terraform) -> Vec<String> {
    let out = t.run(&["show", "-json", "-no-color"]).unwrap();
    let s = parse_state(out.as_bytes()).unwrap();
    state_addresses(&s).into_keys().collect()
}

fn deps<'a>(tf: &str, backups: PathBuf) -> ExecDeps<'a> {
    let tf = tf.to_string();
    ExecDeps {
        new_tf: Box::new(move |wd: &str| {
            let b: Box<dyn Runner> = Box::new(Terraform::new(&tf, wd)?);
            Ok(b)
        }),
        backup_dir: Some(backups),
        locker: None,
    }
}

// Go: TestExecuteIntegration (build tag `integration`; skips without terraform).
#[test]
fn execute_integration() {
    let Some(tf) = terraform() else { return };
    let root = tempfile::Builder::new()
        .prefix("tfsp-int-")
        .tempdir()
        .unwrap();
    let backups = tempfile::Builder::new()
        .prefix("tfsp-int-bk-")
        .tempdir()
        .unwrap();
    let src = tf_stack(
        &tf,
        &root.path().join("src"),
        "resource \"terraform_data\" \"thing\" {}\n",
    );
    let dst = tf_stack(&tf, &root.path().join("dst"), "provider \"terraform\" {}\n");
    src.run(&["apply", "-auto-approve", "-input=false", "-no-color"])
        .unwrap();
    assert_eq!(addrs(&src), vec!["terraform_data.thing"]);
    assert!(addrs(&dst).is_empty());

    let xm = XMove {
        source_stack: "src".into(),
        pairs: vec![Move::new("terraform_data.thing", "terraform_data.thing")],
    };
    let r = root.path().to_string_lossy().into_owned();
    let acts = execute(&deps(&tf, backups.path().into()), &r, "dst", &xm, false).unwrap();
    assert_eq!(acts.len(), 1);
    assert_eq!(acts[0].decision, Decision::Move);
    assert!(addrs(&src).is_empty());
    assert_eq!(addrs(&dst), vec!["terraform_data.thing"]);

    let again = execute(&deps(&tf, backups.path().into()), &r, "dst", &xm, false).unwrap();
    assert_eq!(again.len(), 1);
    assert_eq!(again[0].decision, Decision::Skip);
}

// Go: TestExecuteIntegration_ModuleLevelIntent.
#[test]
fn execute_integration_module_level_intent() {
    let Some(tf) = terraform() else { return };
    let root = tempfile::Builder::new()
        .prefix("tfsp-int-")
        .tempdir()
        .unwrap();
    let backups = tempfile::Builder::new()
        .prefix("tfsp-int-bk-")
        .tempdir()
        .unwrap();
    let src_dir = root.path().join("src");
    write(
        &src_dir,
        "main.tf",
        "\nterraform {\n  backend \"local\" {}\n}\nmodule \"perms\" {\n  source = \"./perms_module\"\n}\n",
    );
    write(
        &src_dir.join("perms_module"),
        "main.tf",
        "\nresource \"terraform_data\" \"x\" { input = \"hello\" }\n",
    );
    let mut src = Terraform::new(&tf, &src_dir.to_string_lossy()).unwrap();
    src.init().unwrap();
    src.run(&["apply", "-auto-approve", "-input=false", "-no-color"])
        .unwrap();
    assert_eq!(addrs(&src), vec!["module.perms.terraform_data.x"]);
    let dst = tf_stack(&tf, &root.path().join("dst"), "provider \"terraform\" {}\n");

    let xm = XMove {
        source_stack: "src".into(),
        pairs: vec![Move::new("module.perms", "module.dest")],
    };
    let r = root.path().to_string_lossy().into_owned();
    let acts = execute(&deps(&tf, backups.path().into()), &r, "dst", &xm, false).unwrap();
    assert_eq!(acts.len(), 1);
    assert_eq!(acts[0].decision, Decision::Move);
    assert_eq!(acts[0].from, "module.perms.terraform_data.x");
    assert_eq!(acts[0].to, "module.dest.terraform_data.x");
    assert!(addrs(&src).is_empty());
    assert_eq!(addrs(&dst), vec!["module.dest.terraform_data.x"]);

    let again = execute(&deps(&tf, backups.path().into()), &r, "dst", &xm, false).unwrap();
    assert_eq!(again.len(), 1);
    assert_eq!(again[0].decision, Decision::Skip);
}
