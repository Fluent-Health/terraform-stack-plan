use std::sync::{Arc, Mutex};

use super::*;
use crate::testserver::{Req, Resp, TestServer, serve};

async fn test_backend<F>(handler: F) -> (CloudBuild, TestServer, Arc<Mutex<Vec<Req>>>)
where
    F: Fn(&Req) -> Resp + Send + Sync + 'static,
{
    let seen = Arc::new(Mutex::new(Vec::new()));
    let s = Arc::clone(&seen);
    let srv = serve(move |r| {
        let resp = handler(&r);
        s.lock().unwrap().push(r);
        resp
    })
    .await
    .unwrap();
    let triggers = BTreeMap::from([
        ("plan".to_string(), "nonprod-plan".to_string()),
        ("apply".to_string(), "nonprod-apply".to_string()),
    ]);
    let token: TokenFunc = Arc::new(|| async { Ok::<_, Error>("tok-1".to_string()) });
    let cb = CloudBuild::new("proj-1", "asia-south1", triggers, token)
        .unwrap()
        .with_base_url(&srv.url);
    (cb, srv, seen)
}

#[tokio::test]
async fn cloud_build_start() {
    let (cb, _srv, seen) = test_backend(|_| {
        Resp::ok(r#"{"name":"operations/build/proj-1/xyz","metadata":{"@type":"type.googleapis.com/google.devtools.cloudbuild.v1.BuildOperationMetadata","build":{"id":"build-123","status":"QUEUED"}}}"#)
    })
    .await;
    let r = cb
        .start(&RunRequest {
            kind: "plan".into(),
            environment: "nonprod".into(),
            sha: "abc123".into(),
            branch: "feat/x".into(),
            execution_id: "run-7-nonprod-plan-abc123-a1".into(),
            pr: 7,
        })
        .await
        .unwrap();
    assert_eq!(r.id, "build-123");
    let req = seen.lock().unwrap()[0].clone();
    assert_eq!(
        req.path,
        "/v1/projects/proj-1/locations/asia-south1/triggers/nonprod-plan:run"
    );
    assert_eq!(req.header("authorization"), "Bearer tok-1");
    let source = &req.json()["source"];
    assert_eq!(source["commitSha"], "abc123");
    assert!(
        source.get("branchName").is_none(),
        "branchName must not ride with commitSha"
    );
    assert_eq!(
        source["substitutions"]["_EXECUTION_ID"],
        "run-7-nonprod-plan-abc123-a1"
    );
    assert_eq!(source["substitutions"]["_PR_NUMBER"], "7");
}

#[tokio::test]
async fn cloud_build_start_branch_when_no_sha() {
    let (cb, _srv, seen) = test_backend(|_| Resp::ok(r#"{"metadata":{"build":{"id":"b"}}}"#)).await;
    cb.start(&RunRequest {
        kind: "apply".into(),
        branch: "main".into(),
        ..RunRequest::default()
    })
    .await
    .unwrap();
    let source = seen.lock().unwrap()[0].json()["source"].clone();
    assert_eq!(source["branchName"], "main");
    assert!(source.get("commitSha").is_none());
}

#[tokio::test]
async fn cloud_build_start_errors() {
    let (cb, _srv, _) =
        test_backend(|_| Resp::status(404, "{\"error\":{\"message\":\"trigger not found\"}}\n"))
            .await;
    let plan = RunRequest {
        kind: "plan".into(),
        sha: "x".into(),
        ..RunRequest::default()
    };
    assert_eq!(
        cb.start(&plan).await.unwrap_err().to_string(),
        r#"cloudbuild: triggers.run nonprod-plan: 404: {"error":{"message":"trigger not found"}}"#
    );
    let deploy = RunRequest {
        kind: "deploy".into(),
        ..plan
    };
    assert_eq!(
        cb.start(&deploy).await.unwrap_err().to_string(),
        r#"cloudbuild: no trigger configured for kind "deploy""#
    );
}

#[tokio::test]
async fn cloud_build_start_without_build_id() {
    let (cb, _srv, _) = test_backend(|_| Resp::ok(r#"{"name":"op"}"#)).await;
    let err = cb
        .start(&RunRequest {
            kind: "plan".into(),
            ..RunRequest::default()
        })
        .await
        .unwrap_err();
    assert_eq!(
        err.to_string(),
        r#"cloudbuild: triggers.run nonprod-plan: no build id in operation: {"name":"op"}"#
    );
}

#[tokio::test]
async fn cloud_build_cancel() {
    let status = Arc::new(Mutex::new(200u16));
    let st = Arc::clone(&status);
    let (cb, _srv, seen) = test_backend(move |_| Resp::status(*st.lock().unwrap(), "{}")).await;
    let r = Ref {
        id: "build-123".into(),
    };
    cb.cancel(&r).await.unwrap();
    assert_eq!(
        seen.lock().unwrap()[0].path,
        "/v1/projects/proj-1/locations/asia-south1/builds/build-123:cancel"
    );
    for s in [400, 404] {
        *status.lock().unwrap() = s;
        cb.cancel(&r).await.unwrap();
    }
    *status.lock().unwrap() = 403;
    assert!(cb.cancel(&r).await.is_err());
    cb.cancel(&Ref::default()).await.unwrap();
}

#[tokio::test]
async fn cloud_build_probe() {
    for (status, want) in [
        ("PENDING", Phase::Queued),
        ("QUEUED", Phase::Queued),
        ("WORKING", Phase::Working),
        ("SUCCESS", Phase::Done),
        ("FAILURE", Phase::Failed),
        ("INTERNAL_ERROR", Phase::Failed),
        ("TIMEOUT", Phase::Failed),
        ("EXPIRED", Phase::Failed),
        ("CANCELLED", Phase::Failed),
    ] {
        let (cb, _srv, _) =
            test_backend(move |_| Resp::ok(format!(r#"{{"id":"build-123","status":"{status}"}}"#)))
                .await;
        let got = cb
            .probe(&Ref {
                id: "build-123".into(),
            })
            .await
            .unwrap();
        assert_eq!(got, want, "{status}");
    }
    let (cb, _srv, _) = test_backend(|_| Resp::status(404, "not found")).await;
    let r = Ref { id: "gone".into() };
    assert_eq!(cb.probe(&r).await.unwrap(), Phase::NotFound);
    let (cb, _srv, _) = test_backend(|_| Resp::ok(r#"{"status":"WEIRD"}"#)).await;
    assert_eq!(
        cb.probe(&r).await.unwrap_err().to_string(),
        r#"cloudbuild: builds.get gone: unknown status "WEIRD""#
    );
}

#[test]
fn truncate_cuts_at_300() {
    assert_eq!(truncate(b"  hi \n"), "hi");
    let long = "x".repeat(400);
    assert_eq!(truncate(long.as_bytes()), format!("{}…", "x".repeat(300)));
}
