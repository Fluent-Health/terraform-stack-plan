// Ported from internal/server/logs_test.go and objects_test.go.
use std::path::Path;
use std::sync::Arc;
use std::time::Duration;

use serde_json::json;
use tfstackplan_gate::events::{Init, StackState, Status};
use tfstackplan_server_clients::github::MockGitHub;

use super::*;
use crate::testutil::{
    MemObjects, fake_oidc, open_stream, seed_init, seed_terminal_status, send, store,
};
use crate::{App, Config};

const WAIT: Duration = Duration::from_secs(2);

/// An app with a fresh logs dir; `objects` wires an in-memory store.
fn logs_app(objects: Option<Arc<MemObjects>>) -> (tempfile::TempDir, tempfile::TempDir, App) {
    let (d, s) = store();
    let logs = tempfile::tempdir().expect("logs dir");
    let mut app = App::new(
        s,
        Arc::new(MockGitHub::default()),
        Config {
            logs_dir: logs.path().to_string_lossy().into_owned(),
            ..Config::default()
        },
    );
    if let Some(o) = objects {
        app.objects = Some(o);
    }
    (d, logs, app)
}

fn init_with(id: &str, stacks: Vec<StackState>) -> Init {
    Init {
        id: id.into(),
        repo: "o/r".into(),
        stacks,
        ..Init::default()
    }
}

fn planned(path: &str) -> StackState {
    StackState {
        path: path.into(),
        status: Status::Planned,
        ..StackState::default()
    }
}

// --- objects_test.go ---

#[test]
fn object_key_sanitizes() {
    let k = object_key("e1", "stacks/a");
    assert!(
        k.starts_with("executions/e1/") && k.ends_with("/log"),
        "{k}"
    );
    assert!(
        !k.contains("stacks/a"),
        "stack slashes should be sanitized: {k}"
    );
}

// --- logs_test.go ---

#[test]
fn log_file_path_sanitizes_and_contains() {
    let base_dir = tempfile::tempdir().expect("dir");
    let base = base_dir.path().to_string_lossy().into_owned();
    let p = log_file_path(&base, "e1", "stacks/a").expect("ok");
    assert!(p.starts_with(&base), "{}", p.display());
    for (exec, stack) in [
        ("../../etc", "x"),
        ("e1", "../../../etc/passwd"),
        ("e1", "a/../../b"),
        ("..", ".."),
        ("e1", "....//....//etc"),
    ] {
        let Some(p) = log_file_path(&base, exec, stack) else {
            continue;
        };
        assert!(
            clean(&p).starts_with(clean(Path::new(&base))) && clean(&p) != clean(Path::new(&base)),
            "traversal escaped base: exec={exec:?} stack={stack:?} → {}",
            p.display()
        );
        assert!(
            !p.to_string_lossy().contains("../"),
            "parent ref left: exec={exec:?} stack={stack:?} → {}",
            p.display()
        );
    }
}

#[tokio::test]
async fn append_log_writes_buffer_and_excerpt() {
    let (_d, _l, a) = logs_app(None);
    a.append_log("e1", "stacks/a", "line1\n")
        .await
        .expect("append");
    a.append_log("e1", "stacks/a", "line2\n")
        .await
        .expect("append");
    let p = log_file_path(&a.cfg.logs_dir, "e1", "stacks/a").expect("path");
    assert_eq!(std::fs::read_to_string(p).expect("read"), "line1\nline2\n");
    let (_, excerpt) = a
        .store()
        .get_stack_output("e1", "stacks/a", "log")
        .expect("get")
        .expect("ok");
    assert!(excerpt.contains("line2"), "{excerpt:?}");
}

#[tokio::test]
async fn append_log_disabled_without_logs_dir() {
    let (_d, a) = crate::testutil::app();
    a.append_log("e1", "stacks/a", "x")
        .await
        .expect("no-op without a logs dir");
}

#[tokio::test]
async fn logs_e2e() {
    let (_d, s) = store();
    let logs = tempfile::tempdir().expect("logs dir");
    let mut a = App::new(
        s,
        Arc::new(MockGitHub::default()),
        Config {
            logs_dir: logs.path().to_string_lossy().into_owned(),
            api_principals: [(
                "runner@x.iam.gserviceaccount.com".to_string(),
                vec!["report".to_string()],
            )]
            .into(),
            ..Config::default()
        },
    );
    a.api_verifier = Some(fake_oidc(&[(
        "tok-runner",
        "runner@x.iam.gserviceaccount.com",
    )]));
    let a = Arc::new(a);
    for data in ["one\n", "two\n"] {
        let w = send(
            &a,
            "POST",
            "/api/logs",
            "tok-runner",
            &json!({"id":"e1","stack":"stacks/a","data":data}).to_string(),
        )
        .await;
        assert_eq!(w.status, 200, "{}", w.text());
    }
    let w = send(&a, "GET", "/logs/e1/stacks/a", "tok-runner", "").await;
    assert_eq!((w.status, w.text().as_str()), (200, "one\ntwo\n"));
    let w = send(&a, "POST", "/api/logs", "", "{}").await;
    assert_eq!(w.status, 401, "/api/logs without bearer");
    let w = send(&a, "GET", "/logs/e1/stacks/a", "", "").await;
    assert_eq!(w.status, 401, "anonymous /logs");
    let w = send(&a, "GET", "/logs/e1/stacks/nope", "tok-runner", "").await;
    assert_eq!(w.status, 404, "unknown log");
}

#[tokio::test]
async fn log_stream_sse() {
    let (_d, _l, a) = logs_app(None);
    let a = Arc::new(a);
    a.append_log("e1", "stacks/a", "before\n")
        .await
        .expect("append");
    let mut st = open_stream(&a, "/logs/e1/stacks/a?follow=1", "", &[]).await;
    assert_eq!(st.content_type, "text/event-stream");
    st.wait_for("data: before", WAIT).await;
    a.append_log("e1", "stacks/a", "after\n")
        .await
        .expect("append");
    st.wait_for("data: after", WAIT).await;
}

#[tokio::test]
async fn log_stream_sse_falls_back_to_object_store_after_finalize() {
    let (_d, _l, a) = logs_app(Some(Arc::new(MemObjects::default())));
    let a = Arc::new(a);
    seed_init(&a, init_with("e1", vec![planned("stacks/a")])).await;
    a.append_log("e1", "stacks/a", "archived line\n")
        .await
        .expect("append");
    a.finalize_logs("e1", &[planned("stacks/a")]).await;
    let mut st = open_stream(&a, "/logs/e1/stacks/a?follow=1", "", &[]).await;
    st.read_to_end(WAIT).await;
    assert!(st.seen.contains("archived line"), "{}", st.seen);
    assert!(st.seen.contains("event: done"), "{}", st.seen);
}

#[tokio::test]
async fn log_stream_resume_from_last_event_id() {
    let (_d, _l, a) = logs_app(None);
    let a = Arc::new(a);
    a.append_log("e1", "s/a", "line1\nline2\nline3\n")
        .await
        .expect("append");
    let mut st = open_stream(&a, "/logs/e1/s/a?follow=1", "", &[("Last-Event-ID", "6")]).await;
    st.wait_for("line3", WAIT).await;
    let data: String = st
        .seen
        .lines()
        .filter_map(|l| l.strip_prefix("data: "))
        .collect();
    assert!(
        st.seen.lines().any(|l| l.starts_with("id: ")),
        "{}",
        st.seen
    );
    assert!(!data.contains("line1"), "{data:?}");
    assert!(data.contains("line2") && data.contains("line3"), "{data:?}");
}

#[tokio::test]
async fn offload_and_serve_from_store() {
    let (_d, _l, a) = logs_app(Some(Arc::new(MemObjects::default())));
    let a = Arc::new(a);
    a.append_log("e1", "stacks/a", "archived log\n")
        .await
        .expect("append");
    let _ = a.offload_log("e1", "stacks/a").await;
    let (pointer, _) = a
        .store()
        .get_stack_output("e1", "stacks/a", "log")
        .expect("get")
        .expect("ok");
    assert!(!pointer.is_empty(), "pointer not set");
    let buf = log_file_path(&a.cfg.logs_dir, "e1", "stacks/a").expect("path");
    let _ = std::fs::remove_file(buf);
    let w = send(&a, "GET", "/logs/e1/stacks/a", "tok-runner", "").await;
    assert_eq!((w.status, w.text().as_str()), (200, "archived log\n"));
}

#[tokio::test]
async fn handle_update_triggers_offload_on_terminal() {
    let (_d, _l, a) = logs_app(Some(Arc::new(MemObjects::default())));
    let a = Arc::new(a);
    seed_init(
        &a,
        Init {
            environment: "staging".into(),
            ..init_with(
                "e1",
                vec![StackState {
                    path: "stacks/a".into(),
                    ..StackState::default()
                }],
            )
        },
    )
    .await;
    a.append_log("e1", "stacks/a", "done log\n")
        .await
        .expect("append");
    send(
        &a,
        "POST",
        "/api/update",
        "",
        &json!({"id":"e1","stack":"stacks/a","status":"planned"}).to_string(),
    )
    .await;
    let pointer = a
        .store()
        .get_stack_output("e1", "stacks/a", "log")
        .expect("get")
        .map(|(p, _)| p)
        .unwrap_or_default();
    assert!(
        !pointer.is_empty(),
        "terminal update should offload the log"
    );
}

#[tokio::test]
async fn finalize_logs_offloads_and_deletes_buffers() {
    let mem = Arc::new(MemObjects::default());
    let (_d, _l, a) = logs_app(Some(Arc::clone(&mem)));
    let a = Arc::new(a);
    seed_init(
        &a,
        Init {
            sha: "s".into(),
            ..init_with("e1", vec![planned("stacks/a")])
        },
    )
    .await;
    a.append_log("e1", "stacks/a", "hello\n")
        .await
        .expect("append");
    a.finalize_logs(
        "e1",
        &[StackState {
            path: "stacks/a".into(),
            ..StackState::default()
        }],
    )
    .await;
    assert!(
        mem.data
            .lock()
            .expect("lock")
            .contains_key(&object_key("e1", "stacks/a")),
        "log not offloaded"
    );
    let exec_dir = Path::new(&a.cfg.logs_dir).join(sanitize_component("e1"));
    assert!(!exec_dir.exists(), "buffer dir survived finalize");
    let w = send(&a, "GET", "/logs/e1/stacks/a", "", "").await;
    assert!(
        w.status == 200 && w.text().contains("hello"),
        "{} {}",
        w.status,
        w.text()
    );
}

#[tokio::test]
async fn finalize_logs_keeps_buffers_when_offload_fails() {
    let (_d, _l, a) = logs_app(None);
    a.append_log("e1", "stacks/a", "hello\n")
        .await
        .expect("append");
    a.finalize_logs(
        "e1",
        &[StackState {
            path: "stacks/a".into(),
            ..StackState::default()
        }],
    )
    .await;
    let p = log_file_path(&a.cfg.logs_dir, "e1", "stacks/a").expect("path");
    assert!(p.exists(), "buffer deleted without offload");
}

fn set_mtime(p: &Path, t: std::time::SystemTime) {
    std::fs::File::open(p)
        .expect("open")
        .set_modified(t)
        .expect("set mtime");
}

#[tokio::test]
async fn clean_log_buffers_sweeps_old_dirs() {
    let (_d, _l, a) = logs_app(None);
    let old = Path::new(&a.cfg.logs_dir).join("old-exec");
    let fresh = Path::new(&a.cfg.logs_dir).join("fresh-exec");
    for d in [&old, &fresh] {
        std::fs::create_dir_all(d).expect("mkdir");
    }
    set_mtime(
        &old,
        std::time::SystemTime::now() - Duration::from_secs(48 * 3600),
    );
    a.clean_log_buffers(Duration::from_secs(24 * 3600)).await;
    assert!(!old.exists(), "stale dir survived");
    assert!(fresh.exists(), "fresh dir swept");
}

#[tokio::test]
async fn clean_log_buffers_uses_injected_clock() {
    let (_d, _l, mut a) = logs_app(None);
    let old = Path::new(&a.cfg.logs_dir).join("exec-old");
    std::fs::create_dir_all(&old).expect("mkdir");
    let mod_time: jiff::Timestamp = "2026-01-01T00:00:00Z".parse().expect("time");
    set_mtime(
        &old,
        std::time::UNIX_EPOCH + Duration::try_from(mod_time.as_duration()).expect("after epoch"),
    );

    let soon = mod_time + jiff::SignedDuration::from_mins(1);
    a.now = Arc::new(move || soon);
    a.clean_log_buffers(Duration::from_secs(3600)).await;
    assert!(old.exists(), "dir removed too early");

    let later = mod_time + jiff::SignedDuration::from_hours(48);
    a.now = Arc::new(move || later);
    a.clean_log_buffers(Duration::from_secs(3600)).await;
    assert!(!old.exists(), "dir should have been removed");
}

#[tokio::test]
async fn handle_plan_serve() {
    let (_d, a) = crate::testutil::app();
    let id = "exec-plan";
    seed_init(
        &a,
        Init {
            context: "plan/nonprod".into(),
            environment: "nonprod".into(),
            ..init_with(id, vec![planned("svc/a")])
        },
    )
    .await;
    a.store()
        .upsert_stack_output(
            id,
            "svc/a",
            "plan",
            "",
            "## changes\n\n- `+ create` thing\n",
        )
        .expect("upsert");
    let w = send(&a, "GET", &format!("/plan/{id}/svc/a"), "", "").await;
    assert_eq!(w.status, 200);
    assert!(w.content_type.contains("text/html"), "{}", w.content_type);
    assert!(w.text().contains("create"), "{}", w.text());
    let w = send(&a, "GET", &format!("/plan/{id}/svc/none"), "", "").await;
    assert_eq!(w.status, 200);
    assert_eq!(w.text().trim(), "", "missing plan should be empty");
}

#[tokio::test]
async fn stream_log_concludes_on_finalize() {
    let (_d, a) = crate::testutil::app();
    let (exec, stack) = ("e-test-1", "stacks/a");
    seed_init(
        &a,
        Init {
            id: exec.into(),
            pr: 42,
            environment: "prod".into(),
            repo: "owner/repo".into(),
            context: "apply".into(),
            ..Init::default()
        },
    )
    .await;
    let mut st = open_stream(&a, &format!("/logs/{exec}/{stack}?follow=1"), "", &[]).await;
    // Go sleeps 10ms so the handler subscribes before the publish.
    tokio::time::sleep(Duration::from_millis(50)).await;
    a.hub
        .publish(&format!("{exec}|{stack}"), "starting apply\n");
    seed_terminal_status(&a, exec, "success").await;
    a.hub.publish(&format!("exec:{exec}"), "changed");
    st.read_to_end(WAIT).await;
    assert!(st.seen.contains("starting apply"), "{}", st.seen);
    assert!(st.seen.contains("event: done"), "{}", st.seen);
}

#[tokio::test]
async fn stream_log_already_finished_at_start() {
    let (_d, a) = crate::testutil::app();
    let exec = "e-test-already-done";
    seed_init(
        &a,
        Init {
            id: exec.into(),
            pr: 42,
            environment: "prod".into(),
            repo: "owner/repo".into(),
            context: "apply".into(),
            ..Init::default()
        },
    )
    .await;
    seed_terminal_status(&a, exec, "success").await;
    let mut st = open_stream(&a, &format!("/logs/{exec}/stacks/a?follow=1"), "", &[]).await;
    st.read_to_end(WAIT).await;
    assert!(st.seen.contains("event: done"), "{}", st.seen);
}
