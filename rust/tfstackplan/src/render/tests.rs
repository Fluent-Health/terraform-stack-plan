use super::*;

use std::path::Path;

fn tempdir() -> tempfile::TempDir {
    tempfile::Builder::new()
        .prefix("tfsp-render-")
        .tempdir()
        .unwrap()
}

fn p(path: &Path) -> String {
    path.to_string_lossy().into_owned()
}

/// Creates `<root>/<name>/tfplan.json` and returns its path.
fn write_plan(root: &Path, name: &str) -> String {
    let dir = root.join(name);
    std::fs::create_dir_all(&dir).unwrap();
    let f = dir.join(PLAN_FILE);
    std::fs::write(&f, "{}").unwrap();
    p(&f)
}

// plandir_test.go

#[test]
fn scan_names_and_sorts() {
    let dir = tempdir();
    let platform = write_plan(dir.path(), "platform/nonprod");
    write_plan(dir.path(), "data/warehouse");
    write_plan(dir.path(), "service-projects/app-dev");
    let got = scan(&p(dir.path())).unwrap();
    let names: Vec<&str> = got.iter().map(|(n, _)| n.as_str()).collect();
    assert_eq!(
        names,
        [
            "data/warehouse",
            "platform/nonprod",
            "service-projects/app-dev"
        ]
    );
    assert_eq!(got[1].1, platform);
}

#[test]
fn scan_ignores_other_files() {
    let dir = tempdir();
    write_plan(dir.path(), "stackA");
    std::fs::create_dir_all(dir.path().join("stackB")).unwrap();
    std::fs::write(dir.path().join("stackB/plan.json"), "{}").unwrap();
    let got = scan(&p(dir.path())).unwrap();
    assert_eq!(got.len(), 1);
    assert_eq!(got[0].0, "stackA");
}

#[test]
fn scan_empty_dir_no_error() {
    let dir = tempdir();
    assert!(scan(&p(dir.path())).unwrap().is_empty());
}

#[test]
fn scan_missing_dir_errors() {
    let dir = tempdir();
    let missing = p(&dir.path().join("does-not-exist"));
    assert_eq!(
        scan(&missing).unwrap_err(),
        format!("stat {missing}: no such file or directory")
    );
}

#[test]
fn scan_non_dir_errors() {
    let dir = tempdir();
    let f = dir.path().join("not-a-dir");
    std::fs::write(&f, "x").unwrap();
    let f = p(&f);
    assert_eq!(
        scan(&f).unwrap_err(),
        format!("plans-dir \"{f}\" is not a directory")
    );
}

#[test]
fn scan_root_plan_errors() {
    let dir = tempdir();
    std::fs::write(dir.path().join(PLAN_FILE), "{}").unwrap();
    let err = scan(&p(dir.path())).unwrap_err();
    assert_eq!(
        err,
        "plandir: tfplan.json sits at the scan root; plans must live in a subdirectory (<stack>/tfplan.json)"
    );
}

// config_test.go: TestDiscoverWalksUpToRepoRoot

#[test]
fn discover_walks_up_to_repo_root() {
    let root = tempdir();
    std::fs::create_dir(root.path().join(".git")).unwrap();
    let cfg = root.path().join(CONFIG_FILE);
    std::fs::write(&cfg, "classification {}\n").unwrap();
    let sub = root.path().join("stacks/prod");
    std::fs::create_dir_all(&sub).unwrap();
    let got = discover(&p(&sub)).expect("the repo-root config");
    assert_eq!(gofs::abs(&got), gofs::abs(&p(&cfg)));

    // A config above the repo root (past .git) is not discovered.
    let above = tempdir();
    std::fs::write(above.path().join(CONFIG_FILE), "classification {}\n").unwrap();
    let repo = above.path().join("repo");
    std::fs::create_dir_all(repo.join(".git")).unwrap();
    let deep = repo.join("a/b");
    std::fs::create_dir_all(&deep).unwrap();
    assert_eq!(discover(&p(&deep)), None);
}

// source_test.go

const SOURCE_TESTDATA: &str = concat!(
    env!("CARGO_MANIFEST_DIR"),
    "/../../internal/source/testdata"
);

fn testdata(rel: &str) -> String {
    gofs::clean(&format!("{SOURCE_TESTDATA}/{rel}"))
}

#[test]
fn index_root_and_local_module() {
    let idx = build_index(&testdata("stack"), &testdata(""));
    let loc = idx
        .lookup("", "google_storage_bucket", "state")
        .expect("root resource");
    assert_eq!((loc.file.as_str(), loc.line), ("stack/main.tf", 1));
    assert_eq!(
        idx.lookup("", "google_project_iam_member", "editor")
            .map(|l| l.line),
        Some(5)
    );
    assert_eq!(
        idx.lookup("module.net", "google_compute_firewall", "web")
            .map(|l| l.file.as_str()),
        Some("stack/modules/net/net.tf")
    );
    assert!(idx.lookup("", "google_storage_bucket", "missing").is_none());
    assert!(idx.lookup("module.remote", "anything", "x").is_none());
}

#[test]
fn rel_to_escape() {
    assert_eq!(
        rel_inside(&testdata("stack"), &testdata("elsewhere/x.tf")),
        None
    );
    assert_eq!(
        rel_inside(&testdata(""), &testdata("stack/main.tf")).as_deref(),
        Some("stack/main.tf")
    );
}

#[test]
fn build_missing_modules_json() {
    let idx = build_index(&testdata("stack/modules/net"), &testdata(""));
    assert!(idx.lookup("", "google_compute_firewall", "web").is_some());
}

#[test]
fn build_skips_unparseable_file() {
    // The only file is partly broken, so it is skipped and nothing is indexed.
    let idx = build_index(&testdata("bad"), &testdata(""));
    assert!(idx.lookup("", "google_storage_bucket", "ok").is_none());
}

/// Go's usage text for the render flag set, recorded from the Go binary.
const GO_USAGE: &str = r#"Usage of tfstackplan render:
  -config string
    	HCL policy file (default: auto-discover .tfstackplan.hcl)
  -details string
    	details disclosure: auto|open|closed (default "closed")
  -emit-classification-json string
    	write computed classes as JSON
  -link-var value
    	link template variable as key=value (repeatable); sha=<sha> also derives sha_short
  -marker string
    	HTML-comment marker for CI upsert (default "tfstackplan")
  -max-bytes int
    	document byte budget (0 disables) (default 60000)
  -output string
    	output file ('-' = stdout) (default "-")
  -plans-dir string
    	directory of per-stack plans (each <stack>/tfplan.json)
  -repo-root string
    	repo root for computing link file paths (default ".")
  -state-moves string
    	JSON manifest of pending cross-state move targets per stack ({"<stack>":["<addr>",...]}); their planned creates classify as moves (non-iam). Keys must match the --plans-dir stack name.
  -title string
    	report title (default "Terraform plan")
  -version
    	print version and exit
"#;

#[test]
fn usage_matches_go() {
    assert_eq!(flags().usage(), GO_USAGE);
}
