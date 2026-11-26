//! Credential-free demo mode (Go: `cmd/tfstackplan/demo.go`, `internal/demo`): a
//! temporary database, a no-op GitHub, the in-memory approval fake, and a seeded
//! plan and apply posted over the real HTTP surface.

use std::sync::Arc;
use std::time::Duration;

use serde_json::{Value, json};
use tfstackplan_server_clients::approval::Fake;
use tfstackplan_server_clients::github::MockGitHub;
use tfstackplan_store::Store;

use crate::{App, Config};

/// The demo app over a fresh database at `db_path`.
pub fn build_demo_app(db_path: &std::path::Path) -> Result<Arc<App>, String> {
    let store = Store::open(db_path).map_err(|e| format!("demo: open store: {e}"))?;
    let logs_dir = db_path
        .parent()
        .unwrap_or(std::path::Path::new("."))
        .join("logs")
        .to_string_lossy()
        .into_owned();
    let mut app = App::new(
        Arc::new(store),
        Arc::new(MockGitHub::default()),
        Config {
            github_webhook_secret: "demo-gh-secret".into(),
            public_base_url: "http://127.0.0.1:8080".into(),
            logs_dir,
            // Must match the environment the seeded events carry.
            environment: "destructive+iam".into(),
            ..Config::default()
        },
    );
    app.approval = Some(Arc::new(Fake::new()));
    Ok(Arc::new(app))
}

async fn post(
    hc: &reqwest::Client,
    url: &str,
    body: &Value,
    ok_conflict: bool,
) -> Result<(), String> {
    let resp = hc
        .post(url)
        .json(body)
        .send()
        .await
        .map_err(|e| e.to_string())?;
    let status = resp.status().as_u16();
    if status == 200 || (ok_conflict && status == 409) {
        return Ok(());
    }
    let text = resp.text().await.unwrap_or_default();
    Err(format!("status {status}: {text}"))
}

fn nanos() -> u128 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos())
        .unwrap_or(0)
}

/// Posts the demo plan and apply executions to `base`; returns (plan id, apply id).
pub async fn seed_scenario(base: &str) -> Result<(String, String), String> {
    let hc = tfstackplan_server_clients::http::client(Duration::from_secs(5))
        .map_err(|e| e.to_string())?;
    let plan_id = format!("demo-plan-{}", nanos() % 1_000_000);
    let apply_id = format!("demo-apply-{}", (nanos() + 12345) % 1_000_000);
    let mut ready = false;
    let mut last_err = String::new();
    for _ in 0..20 {
        match hc.get(format!("{base}/ready")).send().await {
            Ok(r) if r.status() == 200 => {
                ready = true;
                break;
            }
            Ok(r) => last_err = format!("status {}", r.status().as_u16()),
            Err(e) => last_err = e.to_string(),
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    if !ready {
        return Err(format!(
            "server at {base} not ready after timeout: {last_err}"
        ));
    }
    let paths = [
        "infra/vpc",
        "infra/dns",
        "apps/iam",
        "apps/db",
        "apps/web",
        "apps/api",
        "apps/frontend",
        "apps/cdn",
    ];
    let stacks: Vec<Value> = paths
        .iter()
        .map(|p| json!({"path": p, "status": "pending"}))
        .collect();
    let edges: Vec<Value> = [
        ("infra/vpc", "infra/dns"),
        ("infra/vpc", "apps/iam"),
        ("infra/vpc", "apps/db"),
        ("apps/iam", "apps/web"),
        ("apps/db", "apps/web"),
        ("apps/iam", "apps/api"),
        ("apps/db", "apps/api"),
        ("apps/web", "apps/frontend"),
        ("apps/api", "apps/frontend"),
        ("apps/frontend", "apps/cdn"),
    ]
    .iter()
    .map(|(f, t)| json!({"from": f, "to": t}))
    .collect();
    let repo = "Fluent-Health/terraform-stack-plan";
    let sha = "8a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p6q7r8s9t";
    let log_url = "https://github.com/Fluent-Health/terraform-stack-plan/actions/runs/123456";
    let env = "destructive+iam";
    let api = |p: &str| format!("{base}/api/{p}");
    let init = json!({"id": plan_id, "repo": repo, "sha": sha, "pr": 42, "environment": env, "log_url": log_url,
                      "context": "plan", "stacks": stacks, "edges": edges});
    post(&hc, &api("init"), &init, false)
        .await
        .map_err(|e| format!("plan init failed: {e}"))?;
    for ph in ["warming", "linting", "warming", "planning"] {
        post(
            &hc,
            &api("phase"),
            &json!({"id": plan_id, "phase": ph}),
            false,
        )
        .await
        .map_err(|e| format!("plan phase {ph} failed: {e}"))?;
    }
    for (stack, status) in [
        ("infra/vpc", "nochange"),
        ("infra/dns", "nochange"),
        ("apps/iam", "planned"),
        ("apps/db", "planned"),
        ("apps/web", "planned"),
        ("apps/api", "planned"),
        ("apps/frontend", "nochange"),
        ("apps/cdn", "nochange"),
    ] {
        post(
            &hc,
            &api("update"),
            &json!({"id": plan_id, "stack": stack, "status": status}),
            false,
        )
        .await
        .map_err(|e| format!("plan update failed for {stack}: {e}"))?;
    }
    let gates =
        json!([{"class": "iam", "target": "proj-a"}, {"class": "destructive", "target": "proj-a"}]);
    let projects = json!({"apps/iam": "proj-a", "apps/db": "proj-a"});
    let categories = json!({"apps/iam": [{"name": "iam", "icon": "🔐"}], "apps/db": [{"name": "destructive", "icon": "💣"}]});
    let counts = json!({"apps/iam": {"add": 1}, "apps/db": {"destroy": 1}});
    let plan_finalize = json!({
        "id": plan_id,
        "report_markdown": "## Plan Summary Report\n\n- No-change: 4 stacks\n- Planned (Gated): 2 stacks\n- Planned (Clean): 2 stacks\n",
        "projects": projects, "gates": gates, "categories": categories, "counts": counts,
        "stack_reports": {
            "apps/iam": "```diff\n+ google_project_iam_member.x\n+ project = \"proj-a\"\n+ role    = \"roles/editor\"\n```\n",
            "apps/db": "```diff\n- google_sql_database_instance.prod\n- name    = \"db-prod\"\n```\n"
        }
    });
    for ph in ["classify", "report"] {
        post(
            &hc,
            &api("phase"),
            &json!({"id": plan_id, "phase": ph}),
            false,
        )
        .await
        .map_err(|e| format!("plan phase {ph} failed: {e}"))?;
    }
    post(&hc, &api("finalize"), &plan_finalize, false)
        .await
        .map_err(|e| format!("plan finalize failed: {e}"))?;
    let apply_init = json!({"id": apply_id, "repo": repo, "sha": sha, "pr": 42, "environment": env, "log_url": log_url,
                            "context": "apply/destructive+iam", "stacks": stacks, "edges": edges});
    post(&hc, &api("init"), &apply_init, false)
        .await
        .map_err(|e| format!("apply init failed: {e}"))?;
    for ph in ["warming", "moves", "applying"] {
        post(
            &hc,
            &api("phase"),
            &json!({"id": apply_id, "phase": ph}),
            false,
        )
        .await
        .map_err(|e| format!("apply phase {ph} failed: {e}"))?;
    }
    for (stack, status) in [
        ("infra/vpc", "running"),
        ("infra/vpc", "safe"),
        ("infra/dns", "running"),
        ("infra/dns", "safe"),
        ("apps/iam", "gated"),
        ("apps/db", "gated"),
        ("apps/web", "planned"),
        ("apps/api", "planned"),
        ("apps/frontend", "running"),
    ] {
        post(
            &hc,
            &api("update"),
            &json!({"id": apply_id, "stack": stack, "status": status}),
            false,
        )
        .await
        .map_err(|e| format!("apply update failed for {stack}: {e}"))?;
    }
    let apply_finalize = json!({
        "id": apply_id,
        "report_markdown": "## Apply Report\n\n- Infrastructure is fully set up.\n- Application deployments are gated for security.\n",
        "projects": projects, "gates": gates, "categories": categories, "counts": counts
    });
    post(&hc, &api("finalize"), &apply_finalize, false)
        .await
        .map_err(|e| format!("apply finalize failed: {e}"))?;
    post(
        &hc,
        &api("gate/check"),
        &json!({"pr": 42, "environment": env}),
        true,
    )
    .await
    .map_err(|e| format!("gate check failed: {e}"))?;
    let log = json!({"id": apply_id, "stack": "apps/frontend",
        "data": "\u{1b}[32m✔ yarn install --silent\u{1b}[0m\n\u{1b}[34mℹ yarn build\u{1b}[0m\nCreating static production bundle...\n\u{1b}[32m✔ Compiled successfully!\u{1b}[0m\n"});
    post(&hc, &api("logs"), &log, false)
        .await
        .map_err(|e| format!("apply logs failed: {e}"))?;
    Ok((plan_id, apply_id))
}
