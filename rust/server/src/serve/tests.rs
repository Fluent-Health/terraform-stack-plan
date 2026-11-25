// Ported from cmd/tfstackplan/serve_test.go and internal/config/serve_test.go and
// progress_test.go, plus the shutdown drain and TLS provider checks.
use super::*;

#[test]
fn flags_default_and_parse() {
    assert_eq!(
        parse_flags(&[]).unwrap(),
        Flags {
            config: ".tfstackplan.hcl".into(),
            addr: ":8080".into(),
            demo: false
        }
    );
    let f = parse_flags(&[
        "-config".into(),
        "x.hcl".into(),
        "--addr=:9".into(),
        "-demo".into(),
    ])
    .unwrap();
    assert_eq!(
        (f.config.as_str(), f.addr.as_str(), f.demo),
        ("x.hcl", ":9", true)
    );
    let e = parse_flags(&["-nope".into()]).unwrap_err();
    assert!(
        e.starts_with("flag provided but not defined: -nope\nUsage of serve:\n"),
        "{e}"
    );
}

#[test]
fn default_logs_dir_cases() {
    assert_eq!(default_logs_dir("/explicit", "/data/s.db"), "/explicit");
    assert_eq!(default_logs_dir("", "/data/s.db"), "/data/logs");
}

fn write(dir: &tempfile::TempDir, body: &str) -> String {
    let p = dir.path().join("cfg.hcl");
    std::fs::write(&p, body).unwrap();
    p.to_string_lossy().into_owned()
}

#[test]
fn load_server_class_and_serve_blocks() {
    let dir = tempfile::Builder::new().tempdir().unwrap();
    let f = load(&write(
        &dir,
        r#"
server { environment = "nonprod" }
server "other" { environment = "prod" }
class "iam" {
  backend = "gcp-pam"
  entitlement = "iam-elev"
  entitlement_scope = "folders"
  required = true
}
serve {
  db_path = "/data/s.db"
  public_base_url = "https://srv"
  ui_base_url = "https://ui/"
  github_webhook_secret_env = "GH_SECRET"
  logs_dir = "/data/logs"
  github_app {
    app_id = "1"
    installation_id = "2"
    private_key_path = "/k.pem"
  }
  approval "gcp-pam" {
    location = "global"
    duration = "3600s"
    requester_pool = ["sa0", "sa1"]
  }
  objects {
    bucket = "b"
    prefix = "p"
  }
  pubsub { service_account = "push@x" }
  api_auth {
    extra_audiences = ["gcloud"]
    principal "Runner@x" { scopes = ["report"] }
  }
  executor "cloudbuild" {
    project = "proj"
    region = "r1"
    trigger "plan" { name = "tp" }
    trigger "apply" { name = "ta" }
  }
}
progress {
  plan {
    phase "warming" {}
    phase "planning" { weight = 7 }
  }
}
"#,
    ))
    .unwrap();
    assert_eq!(f.environment, "nonprod");
    assert_eq!(
        f.classes,
        vec![("iam".into(), "iam-elev".into(), "folders".into())]
    );
    assert_eq!(f.ui_base_url, "https://ui");
    assert_eq!(
        f.github_app,
        Some(("1".into(), "2".into(), "/k.pem".into()))
    );
    let a = f.approval.unwrap();
    assert_eq!((a.backend.as_str(), a.requester_pool.len()), ("gcp-pam", 2));
    assert_eq!(f.objects, Some((String::new(), "b".into(), "p".into())));
    assert_eq!(f.pubsub, Some((String::new(), "push@x".into())));
    assert_eq!(
        f.api_auth.unwrap().principals,
        vec![("Runner@x".into(), vec!["report".into()])]
    );
    assert_eq!(
        f.executor
            .unwrap()
            .triggers
            .get("apply")
            .map(String::as_str),
        Some("ta")
    );
    let p = f.progress.unwrap();
    assert_eq!(p.plan.len(), 2);
    assert!((p.plan[0].weight - 1.0).abs() < f64::EPSILON);
    assert!((p.plan[1].weight - 7.0).abs() < f64::EPSILON);
}

#[test]
fn load_rejects_bad_api_auth_and_executor() {
    let dir = tempfile::Builder::new().tempdir().unwrap();
    let cases = [
        (
            "serve {\n  api_auth {\n    principal \"a@x\" {\n      scopes = [\"root\"]\n    }\n  }\n}\n",
            r#"api_auth principal "a@x": unknown scope "root" (valid: report, read, admin)"#,
        ),
        (
            "serve {\n  api_auth {\n    principal \"a@x\" {\n      scopes = [\"read\"]\n    }\n    principal \"A@x\" {\n      scopes = [\"read\"]\n    }\n  }\n}\n",
            r#"api_auth: duplicate principal "A@x""#,
        ),
        (
            "serve {\n  executor \"gha\" {}\n}\n",
            r#"executor: unknown backend "gha" (only "cloudbuild" is implemented)"#,
        ),
        (
            "serve {\n  executor \"cloudbuild\" {\n    project = \"p\"\n  }\n}\n",
            r#"executor "cloudbuild": project and region are required"#,
        ),
        (
            "serve {\n  executor \"cloudbuild\" {\n    project = \"p\"\n    region = \"r\"\n    trigger \"plan\" {\n      name = \"x\"\n    }\n  }\n}\n",
            r#"executor "cloudbuild": both a plan and an apply trigger are required"#,
        ),
    ];
    for (body, want) in cases {
        assert_eq!(load(&write(&dir, body)).unwrap_err(), want);
    }
    let p = write(&dir, "nope {}\n");
    assert_eq!(
        load(&p).unwrap_err(),
        format!("{p}: unknown top-level block \"nope\"")
    );
}

fn fake_creds() -> CredsFactory {
    Arc::new(|| {
        Box::pin(async {
            let token: TokenFunc = Arc::new(|| async {
                Ok::<_, tfstackplan_server_clients::Error>("tok".to_string())
            });
            let imp: Arc<dyn Impersonator> = Arc::new(|_sa: String| async {
                Ok::<_, tfstackplan_server_clients::Error>("imp".to_string())
            });
            Ok((token, imp))
        })
    })
}

fn pem(dir: &tempfile::TempDir) -> String {
    let issuer = gauth::gauthtest::Issuer::new().unwrap();
    let sa = issuer
        .write_service_account_key(dir.path(), "x@example.test", "http://127.0.0.1:1")
        .unwrap();
    let doc: serde_json::Value = serde_json::from_slice(&std::fs::read(sa).unwrap()).unwrap();
    let p = dir.path().join("app.pem");
    std::fs::write(&p, doc["private_key"].as_str().unwrap()).unwrap();
    p.to_string_lossy().into_owned()
}

fn base_file(dir: &tempfile::TempDir) -> ServeFile {
    ServeFile {
        has_serve: true,
        db_path: dir.path().join("s.db").to_string_lossy().into_owned(),
        public_base_url: "https://srv".into(),
        github_app: Some(("1".into(), "2".into(), pem(dir))),
        ..ServeFile::default()
    }
}

#[tokio::test]
async fn build_serve_app_boots_ready() {
    let dir = tempfile::Builder::new().tempdir().unwrap();
    let mut f = base_file(&dir);
    f.approval = Some(Approval {
        backend: "gcp-pam".into(),
        requester_pool: vec!["sa0".into()],
        ..Approval::default()
    });
    f.classes = vec![("iam".into(), "iam-elev".into(), String::new())];
    let app = build_app(&f, String::new(), fake_creds()).await.unwrap();
    assert!(app.approval.is_some());
    assert_eq!(
        crate::testutil::send(&app, "GET", "/ready", "", "")
            .await
            .status,
        200
    );
}

#[tokio::test]
async fn build_serve_app_api_auth_needs_audience() {
    let dir = tempfile::Builder::new().tempdir().unwrap();
    let mut f = base_file(&dir);
    f.public_base_url = String::new();
    f.api_auth = Some(ApiAuth::default());
    assert_eq!(
        build_app(&f, String::new(), fake_creds())
            .await
            .err()
            .unwrap(),
        "serve: api_auth needs an audience (set api_auth.audience or public_base_url)"
    );
}

#[tokio::test]
async fn build_serve_app_wires_api_verifier_and_lowercases_principals() {
    let dir = tempfile::Builder::new().tempdir().unwrap();
    let mut f = base_file(&dir);
    f.api_auth = Some(ApiAuth {
        principals: vec![(
            "Runner@X.iam.gserviceaccount.com".into(),
            vec!["report".into()],
        )],
        ..ApiAuth::default()
    });
    let app = build_app(&f, String::new(), fake_creds()).await.unwrap();
    assert!(app.api_verifier.is_some());
    assert!(
        app.config()
            .api_principals
            .contains_key("runner@x.iam.gserviceaccount.com")
    );
    // An unverifiable token is refused once the verifier is wired.
    assert_eq!(
        crate::testutil::send(&app, "POST", "/api/init", "garbage", "")
            .await
            .status,
        401
    );
}

#[tokio::test]
async fn build_serve_app_requires_serve_block() {
    assert_eq!(
        build_app(&ServeFile::default(), String::new(), fake_creds())
            .await
            .err()
            .unwrap(),
        "serve: no `serve {}` block in config"
    );
}

#[test]
fn building_the_http_client_does_not_panic() {
    tfstackplan_server_clients::http::install_crypto_provider();
    tfstackplan_server_clients::http::install_crypto_provider();
    assert!(tfstackplan_server_clients::http::client(Duration::from_secs(1)).is_ok());
}

#[tokio::test]
async fn healthz_reports_version_and_sha() {
    let (_d, a) = crate::testutil::app();
    let r = crate::testutil::send(&a, "GET", "/healthz", "", "").await;
    assert_eq!(r.status, 200);
    let v: serde_json::Value = serde_json::from_slice(&r.body).unwrap();
    assert_eq!(v["version"], env!("CARGO_PKG_VERSION"));
    assert!(v["git_sha"].is_string());
}

#[tokio::test]
async fn sigterm_drains_an_in_flight_request() {
    use axum::routing::get;
    let (started_tx, started_rx) = tokio::sync::oneshot::channel::<()>();
    let started_tx = Arc::new(std::sync::Mutex::new(Some(started_tx)));
    let router = Router::new().route(
        "/slow",
        get(move || {
            let tx = Arc::clone(&started_tx);
            async move {
                if let Some(tx) = tx.lock().unwrap().take() {
                    let _ = tx.send(());
                }
                tokio::time::sleep(Duration::from_millis(400)).await;
                "done"
            }
        }),
    );
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let (sig_tx, sig_rx) = tokio::sync::oneshot::channel::<()>();
    let server = tokio::spawn(serve_until(
        listener,
        router,
        async move {
            let _ = sig_rx.await;
        },
        Duration::from_millis(50),
        Duration::from_secs(5),
    ));
    let client = tfstackplan_server_clients::http::client(Duration::from_secs(5)).unwrap();
    let req = tokio::spawn(async move { client.get(format!("http://{addr}/slow")).send().await });
    started_rx.await.unwrap();
    sig_tx.send(()).unwrap();
    let resp = req.await.unwrap().unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(resp.text().await.unwrap(), "done");
    server.await.unwrap().unwrap();
}
